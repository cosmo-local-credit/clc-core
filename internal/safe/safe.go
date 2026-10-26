// Package safe decodes and polices Safe (1.3.0 / 1.4.1) execution calls that
// arrive through the Calibur relay.
//
// /relayer/relay accepts any signed Calibur batch, so nothing stops a signer
// from relaying Safe.execTransaction directly and skipping proposal-time
// validation. Check is therefore the control: it runs on the relay path, and
// everything it cannot positively identify is rejected.
package safe

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

const (
	OperationCall         uint8 = 0
	OperationDelegateCall uint8 = 1

	// Safe 1.3.0/1.4.1 gas model: one signature check is ecrecover plus the
	// surrounding owner bookkeeping; a signature is 65 bytes of calldata.
	baseExecGas      uint64 = 80_000
	perSignatureGas  uint64 = 12_000
	perSubcallGas    uint64 = 140_000
	singleCallGas    uint64 = 160_000
	maxSubcallsCount        = 32
)

var (
	execTransactionSelector = selector("execTransaction(address,uint256,bytes,uint8,uint256,uint256,uint256,address,address,bytes)")
	multiSendSelector       = selector("multiSend(bytes)")

	// DefaultAllowedSignatures are the owner actions clc-app issues against
	// vouchers, pools and their wiring contracts. Anything else is rejected.
	DefaultAllowedSignatures = []string{
		"transferOwnership(address)",
		"add(address)",
		"remove(address)",
		"mintTo(address,uint256)",
		"setLimitFor(address,address,uint256)",
		"setPriceIndexValue(address,uint256)",
		"setOracle(address,address)",
		"setDefaultFee(uint256)",
		"setFeeAddress(address)",
		"setQuoter(address)",
		"withdraw(address)",
		"withdraw(address,uint256)",
		"withdrawLiquidity(address,address,uint256)",
	}

	ErrNotSafeCall   = errors.New("safe: batch is not a single execTransaction call")
	ErrRejected      = errors.New("safe: transaction rejected by policy")
	errMalformedData = errors.New("safe: malformed calldata")
)

type (
	// Resolver answers the two questions Check cannot answer from calldata.
	// Both must fail closed: an error rejects the batch.
	Resolver interface {
		// IsSafe reports whether address is a known Safe (signer cache).
		IsSafe(ctx context.Context, address common.Address) (bool, error)
		// ManagesAsset reports whether target is a catalog asset, or a wiring
		// contract of an asset, owned by safe.
		ManagesAsset(ctx context.Context, safe, target common.Address) (bool, error)
	}

	Call struct {
		To    common.Address
		Value *big.Int
		Data  []byte
	}

	// Exec is a decoded execTransaction call.
	Exec struct {
		To             common.Address
		Value          *big.Int
		Data           []byte
		Operation      uint8
		SafeTxGas      *big.Int
		BaseGas        *big.Int
		GasPrice       *big.Int
		GasToken       common.Address
		RefundReceiver common.Address
		Signatures     []byte
	}

	// SubCall is one packed MultiSend transaction.
	SubCall struct {
		Operation uint8
		To        common.Address
		Value     *big.Int
		Data      []byte
	}

	Policy struct {
		// MultiSend holds the pinned MultiSendCallOnly deployments. A
		// delegatecall to anything else is rejected.
		MultiSend map[common.Address]struct{}
		Selectors map[[4]byte]struct{}
		Resolver  Resolver
	}

	// Verdict is what Check learned about an accepted batch, for gas budgeting.
	Verdict struct {
		Safe       common.Address
		Signatures int
		Subcalls   int
	}
)

func selector(signature string) [4]byte {
	var out [4]byte
	copy(out[:], crypto.Keccak256([]byte(signature))[:4])
	return out
}

// NewPolicy pins the MultiSend addresses and builds the selector allowlist
// from function signatures.
func NewPolicy(resolver Resolver, multiSend []common.Address, signatures []string) *Policy {
	p := &Policy{
		MultiSend: make(map[common.Address]struct{}, len(multiSend)),
		Selectors: make(map[[4]byte]struct{}, len(signatures)),
		Resolver:  resolver,
	}
	for _, a := range multiSend {
		p.MultiSend[a] = struct{}{}
	}
	for _, s := range signatures {
		p.Selectors[selector(s)] = struct{}{}
	}
	return p
}

// IsExecTransaction reports whether data begins with the execTransaction
// selector. Detection is by selector, not by target, so an unknown Safe cannot
// slip past a cold signer cache.
func IsExecTransaction(data []byte) bool {
	return len(data) >= 4 && [4]byte(data[:4]) == execTransactionSelector
}

// Check applies the policy to a Calibur batch. It returns (nil, nil) when the
// batch has nothing to do with a Safe, and ErrRejected-wrapped errors for a
// hostile or unrecognised Safe call.
func (p *Policy) Check(ctx context.Context, calls []Call) (*Verdict, error) {
	touchesSafe := false
	for _, call := range calls {
		if IsExecTransaction(call.Data) {
			touchesSafe = true
			continue
		}
		ok, err := p.Resolver.IsSafe(ctx, call.To)
		if err != nil {
			return nil, fmt.Errorf("safe: resolve target: %w", err)
		}
		if ok {
			touchesSafe = true
		}
	}
	if !touchesSafe {
		return nil, nil
	}
	if len(calls) != 1 {
		return nil, fmt.Errorf("%w: a batch that calls a Safe must contain exactly that call", ErrRejected)
	}
	call := calls[0]
	if call.Value != nil && call.Value.Sign() != 0 {
		return nil, fmt.Errorf("%w: value to a Safe must be zero", ErrRejected)
	}
	if !IsExecTransaction(call.Data) {
		return nil, fmt.Errorf("%w: only execTransaction may be called on a Safe", ErrRejected)
	}
	exec, err := DecodeExec(call.Data)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRejected, err)
	}
	return p.checkExec(ctx, call.To, exec, true)
}

// CheckTransaction applies the same rules to a Safe transaction that has not
// been executed yet, so a proposal is judged by exactly the code the relay
// runs at execution. The transaction carries no signatures to count.
func (p *Policy) CheckTransaction(ctx context.Context, safe common.Address, tx *Exec) error {
	_, err := p.checkExec(ctx, safe, tx, false)
	return err
}

func (p *Policy) checkExec(ctx context.Context, safe common.Address, exec *Exec, signed bool) (*Verdict, error) {
	if exec.SafeTxGas.Sign() != 0 || exec.GasPrice.Sign() != 0 ||
		exec.GasToken != (common.Address{}) || exec.RefundReceiver != (common.Address{}) {
		// safeTxGas = 0 makes an inner failure revert and surface; a gas price
		// would refund the relayer out of the Safe.
		return nil, fmt.Errorf("%w: safeTxGas, gasPrice, gasToken and refundReceiver must be zero", ErrRejected)
	}
	if signed && (len(exec.Signatures) == 0 || len(exec.Signatures)%65 != 0) {
		return nil, fmt.Errorf("%w: signatures must be packed 65-byte entries", ErrRejected)
	}
	verdict := &Verdict{Safe: safe, Signatures: len(exec.Signatures) / 65}

	if exec.Value.Sign() != 0 {
		return nil, fmt.Errorf("%w: inner value must be zero", ErrRejected)
	}

	switch exec.Operation {
	case OperationCall:
		// Cancellation: a zero-value self-call with no data, used to burn a nonce.
		if exec.To == safe && len(exec.Data) == 0 {
			verdict.Subcalls = 1
			return verdict, nil
		}
		if err := p.checkInner(ctx, safe, exec.To, exec.Value, exec.Data); err != nil {
			return nil, err
		}
		verdict.Subcalls = 1
		return verdict, nil
	case OperationDelegateCall:
		if _, pinned := p.MultiSend[exec.To]; !pinned {
			return nil, fmt.Errorf("%w: delegatecall target is not a pinned MultiSendCallOnly", ErrRejected)
		}
		subs, err := DecodeMultiSend(exec.Data)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrRejected, err)
		}
		if len(subs) == 0 || len(subs) > maxSubcallsCount {
			return nil, fmt.Errorf("%w: multisend must carry 1..%d calls", ErrRejected, maxSubcallsCount)
		}
		for _, sub := range subs {
			if sub.Operation != OperationCall {
				return nil, fmt.Errorf("%w: multisend subcall must be a plain call", ErrRejected)
			}
			if err := p.checkInner(ctx, safe, sub.To, sub.Value, sub.Data); err != nil {
				return nil, err
			}
		}
		verdict.Subcalls = len(subs)
		return verdict, nil
	default:
		return nil, fmt.Errorf("%w: unknown operation %d", ErrRejected, exec.Operation)
	}
}

func (p *Policy) checkInner(ctx context.Context, safe, to common.Address, value *big.Int, data []byte) error {
	if value != nil && value.Sign() != 0 {
		return fmt.Errorf("%w: subcall value must be zero", ErrRejected)
	}
	if to == safe {
		return fmt.Errorf("%w: a Safe may not call itself (owner and threshold changes)", ErrRejected)
	}
	if len(data) < 4 {
		return fmt.Errorf("%w: subcall has no selector", ErrRejected)
	}
	if _, ok := p.Selectors[[4]byte(data[:4])]; !ok {
		return fmt.Errorf("%w: selector %x is not allowed", ErrRejected, data[:4])
	}
	managed, err := p.Resolver.ManagesAsset(ctx, safe, to)
	if err != nil {
		return fmt.Errorf("safe: resolve asset: %w", err)
	}
	if !managed {
		return fmt.Errorf("%w: %s is not an asset of this Safe", ErrRejected, to.Hex())
	}
	return nil
}

// GasLimit budgets execution of a verified Safe call. The outer Calibur
// budget counts one call however much the Safe does inside it, so this is
// added on top: base, per-signature, and per decoded subcall.
func (v *Verdict) GasLimit() uint64 {
	if v.Subcalls <= 1 {
		return baseExecGas + singleCallGas + perSignatureGas*uint64(v.Signatures)
	}
	return baseExecGas + perSubcallGas*uint64(v.Subcalls) + perSignatureGas*uint64(v.Signatures)
}

// DecodeExec parses execTransaction calldata (selector included).
func DecodeExec(data []byte) (*Exec, error) {
	if !IsExecTransaction(data) {
		return nil, errMalformedData
	}
	args := data[4:]
	if len(args) < 10*32 {
		return nil, errMalformedData
	}
	word := func(i int) []byte { return args[i*32 : (i+1)*32] }
	addr := func(i int) (common.Address, error) {
		for _, b := range word(i)[:12] {
			if b != 0 {
				return common.Address{}, errMalformedData
			}
		}
		return common.BytesToAddress(word(i)[12:]), nil
	}
	exec := &Exec{
		Value:     new(big.Int).SetBytes(word(1)),
		SafeTxGas: new(big.Int).SetBytes(word(4)),
		BaseGas:   new(big.Int).SetBytes(word(5)),
		GasPrice:  new(big.Int).SetBytes(word(6)),
	}
	var err error
	if exec.To, err = addr(0); err != nil {
		return nil, err
	}
	if exec.GasToken, err = addr(7); err != nil {
		return nil, err
	}
	if exec.RefundReceiver, err = addr(8); err != nil {
		return nil, err
	}
	op := new(big.Int).SetBytes(word(3))
	if !op.IsUint64() || op.Uint64() > 1 {
		return nil, errMalformedData
	}
	exec.Operation = uint8(op.Uint64())
	if exec.Data, err = dynamicBytes(args, word(2)); err != nil {
		return nil, err
	}
	if exec.Signatures, err = dynamicBytes(args, word(9)); err != nil {
		return nil, err
	}
	return exec, nil
}

// dynamicBytes reads an ABI `bytes` whose head word is offset into args.
func dynamicBytes(args, offsetWord []byte) ([]byte, error) {
	off := new(big.Int).SetBytes(offsetWord)
	if !off.IsUint64() || off.Uint64() > uint64(len(args)) || off.Uint64()+32 > uint64(len(args)) {
		return nil, errMalformedData
	}
	start := off.Uint64()
	n := new(big.Int).SetBytes(args[start : start+32])
	if !n.IsUint64() || n.Uint64() > uint64(len(args)) || start+32+n.Uint64() > uint64(len(args)) {
		return nil, errMalformedData
	}
	return args[start+32 : start+32+n.Uint64()], nil
}

// DecodeMultiSend parses the packed transactions inside multiSend(bytes)
// calldata (selector included), as the Safe tx `data` field carries them.
func DecodeMultiSend(data []byte) ([]SubCall, error) {
	if len(data) < 4 || [4]byte(data[:4]) != multiSendSelector {
		return nil, errMalformedData
	}
	args := data[4:]
	if len(args) < 64 {
		return nil, errMalformedData
	}
	packed, err := dynamicBytes(args, args[:32])
	if err != nil {
		return nil, err
	}
	var subs []SubCall
	for i := 0; i < len(packed); {
		if len(packed)-i < 85 {
			return nil, errMalformedData
		}
		sub := SubCall{
			Operation: packed[i],
			To:        common.BytesToAddress(packed[i+1 : i+21]),
			Value:     new(big.Int).SetBytes(packed[i+21 : i+53]),
		}
		n := new(big.Int).SetBytes(packed[i+53 : i+85])
		if !n.IsUint64() || n.Uint64() > uint64(len(packed)-i-85) {
			return nil, errMalformedData
		}
		end := i + 85 + int(n.Uint64())
		sub.Data = packed[i+85 : end]
		subs = append(subs, sub)
		i = end
	}
	return subs, nil
}

// Budget returns the extra gas a relayed batch needs when it is a Safe
// execution, judged from calldata alone, so the relay worker can size the
// limit without a resolver. It is a budget, not a verdict: Check decides
// whether the batch may be relayed at all.
func Budget(calls []Call) (uint64, bool) {
	if len(calls) != 1 || !IsExecTransaction(calls[0].Data) {
		return 0, false
	}
	exec, err := DecodeExec(calls[0].Data)
	if err != nil {
		return 0, false
	}
	v := &Verdict{Signatures: len(exec.Signatures) / 65, Subcalls: 1}
	if exec.Operation == OperationDelegateCall {
		if subs, err := DecodeMultiSend(exec.Data); err == nil {
			v.Subcalls = len(subs)
		}
	}
	return v.GasLimit(), true
}
