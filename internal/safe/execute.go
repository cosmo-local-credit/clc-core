package safe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"strings"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/lmittmann/w3"
)

// Execution by clc-core of a Safe transaction its signers have confirmed.
// execTransaction is permissionless once it carries threshold valid owner
// signatures, so the system signer can submit it and pay the gas. Everything
// is re-derived from the chain and the stored transaction; nothing from the
// request that triggered the execution is trusted.

var (
	// ErrSettled: the transaction was executed, or another one used its nonce.
	ErrSettled = errors.New("safe: transaction already executed or replaced")
	// ErrNotReady: fewer valid owner signatures than the threshold.
	ErrNotReady = errors.New("safe: not enough valid confirmations")
	// ErrNotNext: an earlier nonce is still queued; Safe executes in order.
	ErrNotNext = errors.New("safe: an earlier transaction is still pending")

	errUnsupportedSignature = errors.New("safe: unsupported signature type")

	execTransactionFunc = w3.MustNewFunc("execTransaction(address,uint256,bytes,uint8,uint256,uint256,uint256,address,address,bytes)", "bool")
)

type (
	// TransactionSource reads a stored Safe transaction; the STS client
	// satisfies it.
	TransactionSource interface {
		Transaction(ctx context.Context, safeTxHash string) (*Transaction, error)
	}

	// ExecStateReader reads a Safe's signer set, threshold and next nonce at
	// one block; Reader satisfies it.
	ExecStateReader interface {
		ReadExecState(ctx context.Context, safe common.Address) (*ExecState, error)
	}

	// ExecState is a Safe as an execution is judged, all at one block.
	ExecState struct {
		State

		Nonce uint64
		// Contracts verifies owner contract signatures; nil accepts none.
		Contracts ContractVerifier
	}

	Executor struct {
		chainID int64
		policy  *Policy
		sts     TransactionSource
		reader  ExecStateReader
	}

	// Execution is a verified execTransaction call, ready to send.
	Execution struct {
		Safe     common.Address
		Calldata []byte
	}
)

func NewExecutor(chainID int64, policy *Policy, sts TransactionSource, reader ExecStateReader) *Executor {
	return &Executor{chainID: chainID, policy: policy, sts: sts, reader: reader}
}

// Prepare checks a stored transaction end to end and builds its execution: not
// yet executed, its hash reproducible, accepted by the same policy as a
// proposal, next in the Safe's nonce order, and carrying threshold valid
// signatures of current owners.
func (e *Executor) Prepare(ctx context.Context, safeTxHash string) (*Execution, error) {
	tx, err := e.sts.Transaction(ctx, safeTxHash)
	if err != nil {
		return nil, err
	}
	if tx.IsExecuted {
		return nil, ErrSettled
	}
	if !common.IsHexAddress(tx.Safe) {
		return nil, fmt.Errorf("%w: malformed Safe address", ErrRejected)
	}
	safe := common.HexToAddress(tx.Safe)
	hash, err := SafeTxHash(e.chainID, safe, tx.TxFields)
	if err != nil || !strings.EqualFold(hash.Hex(), safeTxHash) {
		return nil, fmt.Errorf("%w: safeTxHash does not match the transaction", ErrRejected)
	}
	exec, err := tx.Exec()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRejected, err)
	}
	if err := e.policy.CheckTransaction(ctx, safe, exec); err != nil {
		return nil, err
	}

	state, err := e.reader.ReadExecState(ctx, safe)
	if errors.Is(err, ErrNotSafe) {
		return nil, fmt.Errorf("%w: owner is no longer a multisig", ErrRejected)
	}
	if err != nil {
		return nil, err
	}
	txNonce, err := uintOf("nonce", tx.Nonce)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRejected, err)
	}
	switch txNonce.Cmp(new(big.Int).SetUint64(state.Nonce)) {
	case -1:
		return nil, ErrSettled
	case 1:
		return nil, ErrNotNext
	}

	exec.Signatures, err = PackSignatures(ctx, hash, tx.Confirmations, state.Owners, state.Threshold, state.Contracts)
	if err != nil {
		return nil, err
	}
	calldata, err := EncodeExecTransaction(exec)
	if err != nil {
		return nil, err
	}
	return &Execution{Safe: safe, Calldata: calldata}, nil
}

// PackSignatures verifies each confirmation against hash and the current owner
// set, and packs threshold of them in ascending owner order, as Safe's
// checkNSignatures requires. Owner ECDSA signatures are typed data (v 27/28)
// and eth_sign (v 31/32). Contract signatures (v 0) from a Safe 1.5.0 or later
// fill in only when ECDSA ones fall short of the threshold, so a transaction
// they already satisfy is packed without an RPC call. Approved-hash (v 1)
// signatures are left to the Safe app.
func PackSignatures(ctx context.Context, hash common.Hash, confirmations []Confirmation, owners []common.Address, threshold int, contracts ContractVerifier) ([]byte, error) {
	if threshold < 1 {
		return nil, fmt.Errorf("%w: unknown threshold", ErrNotReady)
	}
	isOwner := make(map[common.Address]bool, len(owners))
	for _, owner := range owners {
		isOwner[owner] = true
	}
	valid := make(map[common.Address][]byte, len(confirmations))
	for _, c := range confirmations {
		sig, err := hexutil.Decode(c.Signature)
		if err != nil || len(sig) != 65 {
			continue
		}
		signer, err := recoverOwnerSignature(hash, sig)
		if err != nil || !isOwner[signer] || !strings.EqualFold(signer.Hex(), c.Owner) {
			continue
		}
		valid[signer] = sig
	}
	if len(valid) >= threshold {
		signers := sortedSigners(valid)
		chosen := make(map[common.Address][]byte, threshold)
		for _, signer := range signers[:threshold] {
			chosen[signer] = valid[signer]
		}
		return packSignatures(chosen, nil), nil
	}
	contractSigs, err := verifyContractSignatures(ctx, hash, confirmations, isOwner, valid, threshold-len(valid), contracts)
	if err != nil {
		return nil, err
	}
	if len(valid)+len(contractSigs) < threshold {
		return nil, fmt.Errorf("%w: %d of %d", ErrNotReady, len(valid)+len(contractSigs), threshold)
	}
	return packSignatures(valid, contractSigs), nil
}

// verifyContractSignatures returns up to need verified contract signatures from
// owners without an ECDSA one, in ascending owner order. A transport failure is
// returned so the execution is retried rather than judged not ready.
func verifyContractSignatures(ctx context.Context, hash common.Hash, confirmations []Confirmation, isOwner map[common.Address]bool, ecdsa map[common.Address][]byte, need int, contracts ContractVerifier) (map[common.Address][]byte, error) {
	if contracts == nil {
		return nil, nil
	}
	candidates := make(map[common.Address][]byte)
	for _, c := range confirmations {
		sig, err := hexutil.Decode(c.Signature)
		if err != nil || !IsContractSignature(sig) {
			continue
		}
		parsed, err := ParseContractSignature(sig)
		if err != nil || !isOwner[parsed.Owner] || !strings.EqualFold(parsed.Owner.Hex(), c.Owner) {
			continue
		}
		if _, ok := ecdsa[parsed.Owner]; ok {
			continue
		}
		if _, ok := candidates[parsed.Owner]; !ok {
			candidates[parsed.Owner] = parsed.Data
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	accepts, err := contracts.Accepts(ctx)
	if err != nil {
		return nil, fmt.Errorf("safe: read version: %w", err)
	}
	if !accepts {
		return nil, nil
	}
	verified := make(map[common.Address][]byte, need)
	for _, owner := range sortedSigners(candidates) {
		if len(verified) == need {
			break
		}
		ok, err := contracts.Valid(ctx, owner, hash, candidates[owner])
		if err != nil {
			return nil, fmt.Errorf("safe: verify contract signature: %w", err)
		}
		if ok {
			verified[owner] = candidates[owner]
		}
	}
	return verified, nil
}

// packSignatures lays out ECDSA and contract signatures in ascending owner
// order. A contract signature's static part is r = owner, s = the offset of its
// length-prefixed data, appended after all static parts, and v = 0.
func packSignatures(ecdsa, contracts map[common.Address][]byte) []byte {
	all := make(map[common.Address][]byte, len(ecdsa)+len(contracts))
	maps.Copy(all, ecdsa)
	maps.Copy(all, contracts)
	signers := sortedSigners(all)
	static := make([]byte, 0, 65*len(signers))
	var dynamic []byte
	for _, signer := range signers {
		if sig, ok := ecdsa[signer]; ok {
			static = append(static, sig...)
			continue
		}
		data := contracts[signer]
		offset := uint64(65*len(signers) + len(dynamic))
		static = append(static, common.LeftPadBytes(signer[:], 32)...)
		static = append(static, common.LeftPadBytes(new(big.Int).SetUint64(offset).Bytes(), 32)...)
		static = append(static, 0)
		dynamic = append(dynamic, common.LeftPadBytes(big.NewInt(int64(len(data))).Bytes(), 32)...)
		dynamic = append(dynamic, data...)
	}
	return append(static, dynamic...)
}

func sortedSigners(sigs map[common.Address][]byte) []common.Address {
	signers := slices.Collect(maps.Keys(sigs))
	slices.SortFunc(signers, func(a, b common.Address) int { return bytes.Compare(a[:], b[:]) })
	return signers
}

func recoverOwnerSignature(hash common.Hash, sig []byte) (common.Address, error) {
	switch v := sig[64]; v {
	case 27, 28:
		return RecoverSigner(hash, sig)
	case 31, 32:
		// eth_sign: Safe recovers from the prefixed digest with v - 4.
		adjusted := slices.Clone(sig)
		adjusted[64] = v - 4
		return RecoverSigner(common.BytesToHash(accounts.TextHash(hash[:])), adjusted)
	default:
		return common.Address{}, errUnsupportedSignature
	}
}

// EncodeExecTransaction builds execTransaction calldata from a decoded call and
// its packed signatures.
func EncodeExecTransaction(e *Exec) ([]byte, error) {
	return execTransactionFunc.EncodeArgs(e.To, e.Value, e.Data, e.Operation, e.SafeTxGas, e.BaseGas,
		e.GasPrice, e.GasToken, e.RefundReceiver, e.Signatures)
}
