package relayer

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"math/big"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	gocalibur "github.com/grassrootseconomics/go-calibur"
	"github.com/holiman/uint256"
	"github.com/lmittmann/w3/module/eth"
)

type (
	// BuildRelayResult is what BuildAndSignRelay returns so the caller knows
	// which OTX type to record against the transaction.
	BuildRelayResult struct {
	Tx          *types.Transaction
	OTXType     string // store.RELAY_EXEC or store.RELAY_DELEGATE_EXEC
	IsDelegated bool
}
)

// IsDelegated checks whether userAddr currently delegates to the configured
// Calibur implementation (EIP-7702 prefix + matching target).
func (r *Relayer) IsDelegated(ctx context.Context, userAddr common.Address) (bool, error) {
	var code []byte
	if err := r.chainProvider.Client.CallCtx(ctx, eth.Code(userAddr, nil).Returns(&code)); err != nil {
		return false, err
	}
	return gocalibur.IsDelegatedTo(code, r.caliburAddress), nil
}

// EOANonce returns the on-chain transaction nonce for addr.
func (r *Relayer) EOANonce(ctx context.Context, addr common.Address) (uint64, error) {
	return r.caliburProvider.GetNonce(ctx, addr)
}

// CaliburSeq returns the Calibur sequence number for (addr, keyHash).
// keyHash zero == root key.
func (r *Relayer) CaliburSeq(ctx context.Context, addr common.Address, keyHash common.Hash) (uint64, error) {
	seq, err := r.caliburProvider.GetSeq(ctx, addr, new(big.Int).SetBytes(keyHash[:]))
	if err != nil {
		return 0, err
	}
	return seq.Uint64(), nil
}

// BuildAndSignRelay produces a signed transaction relaying signedBatch.
//
// The master signing key and its derived signer address are loaded by the
// relay river worker (within its Work transaction) and passed in — the
// relayer never holds the key.
//
// When isDelegated is true a normal DynamicFeeTx to userAddr is produced.
// Otherwise a SetCodeTx with an AuthList entry is produced — auth must be
// non-nil in that case (the caller is responsible for validating presence).
func (r *Relayer) BuildAndSignRelay(
	ctx context.Context,
	privateKey *ecdsa.PrivateKey,
	signerAddr common.Address,
	userAddr common.Address,
	signedBatch apiresp.CaliburSignedBatch,
	signature []byte,
	signatureType string,
	auth *apiresp.EIP7702Authorization,
	isDelegated bool,
	relayerNonce uint64,
) (*BuildRelayResult, error) {
	_ = ctx

	gocaliburBatch, err := convertSignedBatch(signedBatch)
	if err != nil {
		return nil, err
	}

	// Defence in depth: the contract enforces this too, but rejecting here
	// saves wasted gas if a client misconfigures the executor field.
	if gocaliburBatch.Executor != (common.Address{}) && gocaliburBatch.Executor != signerAddr {
		return nil, fmt.Errorf("relay: executor %s != relayer %s",
			gocaliburBatch.Executor.Hex(), signerAddr.Hex())
	}

	calldata, err := gocalibur.EncodeRelayedCalldata(gocaliburBatch, signature)
	if err != nil {
		return nil, fmt.Errorf("relay: encode calldata: %w", err)
	}

	gasFeeCap, gasTipCap, err := r.gasSettings()
	if err != nil {
		return nil, fmt.Errorf("relay: gas settings: %w", err)
	}

	gasLimit := r.relayGasLimit(len(gocaliburBatch.BatchedCall.Calls), isDelegated, signatureType == "webauthn")

	if isDelegated {
		tx := types.NewTx(&types.DynamicFeeTx{
			ChainID:   r.chainID,
			Nonce:     relayerNonce,
			GasTipCap: gasTipCap,
			GasFeeCap: gasFeeCap,
			Gas:       gasLimit,
			To:        &userAddr,
			Value:     big.NewInt(0),
			Data:      calldata,
		})
		signed, err := types.SignTx(tx, types.LatestSignerForChainID(r.chainID), privateKey)
		if err != nil {
			return nil, fmt.Errorf("relay: sign exec: %w", err)
		}
		return &BuildRelayResult{
			Tx:          signed,
			OTXType:     "RELAY_EXEC",
			IsDelegated: true,
		}, nil
	}

	if auth == nil {
		return nil, errors.New("relay: authorization required for first relay")
	}
	if !common.IsHexAddress(auth.ContractAddress) {
		return nil, errors.New("relay: invalid authorization.contractAddress")
	}
	if common.HexToAddress(auth.ContractAddress) != r.caliburAddress {
		return nil, errors.New("relay: authorization.contractAddress != calibur")
	}
	authChainID, ok := new(big.Int).SetString(auth.ChainID, 10)
	if !ok {
		return nil, errors.New("relay: invalid authorization.chainId")
	}
	if authChainID.Cmp(r.chainID) != 0 {
		return nil, errors.New("relay: authorization.chainId mismatch")
	}
	authNonce, ok := new(big.Int).SetString(auth.Nonce, 10)
	if !ok {
		return nil, errors.New("relay: invalid authorization.nonce")
	}

	scAuth, err := convertAuthorization(r.chainID, common.HexToAddress(auth.ContractAddress), authNonce.Uint64(), *auth)
	if err != nil {
		return nil, err
	}

	tx := types.NewTx(&types.SetCodeTx{
		ChainID:   uint256.MustFromBig(r.chainID),
		Nonce:     relayerNonce,
		GasTipCap: uint256.MustFromBig(gasTipCap),
		GasFeeCap: uint256.MustFromBig(gasFeeCap),
		Gas:       gasLimit,
		To:        userAddr,
		Value:     uint256.NewInt(0),
		Data:      calldata,
		AuthList:  []types.SetCodeAuthorization{scAuth},
	})
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(r.chainID), privateKey)
	if err != nil {
		return nil, fmt.Errorf("relay: sign setcode+exec: %w", err)
	}
	return &BuildRelayResult{
		Tx:          signed,
		OTXType:     "RELAY_DELEGATE_EXEC",
		IsDelegated: false,
	}, nil
}

func convertSignedBatch(in apiresp.CaliburSignedBatch) (gocalibur.SignedBatchedCall, error) {
	nonce, ok := new(big.Int).SetString(in.Nonce, 10)
	if !ok {
		return gocalibur.SignedBatchedCall{}, errors.New("relay: invalid signedBatch.nonce")
	}
	deadline, ok := new(big.Int).SetString(in.Deadline, 10)
	if !ok {
		return gocalibur.SignedBatchedCall{}, errors.New("relay: invalid signedBatch.deadline")
	}

	var keyHash [32]byte
	khBytes := common.FromHex(in.KeyHash)
	if len(khBytes) != 32 {
		return gocalibur.SignedBatchedCall{}, errors.New("relay: invalid signedBatch.keyHash length")
	}
	copy(keyHash[:], khBytes)

	calls := make([]gocalibur.Call, 0, len(in.BatchedCall.Calls))
	for i, call := range in.BatchedCall.Calls {
		value, ok := new(big.Int).SetString(call.Value, 10)
		if !ok {
			return gocalibur.SignedBatchedCall{}, fmt.Errorf("relay: invalid value at call[%d]", i)
		}
		calls = append(calls, gocalibur.Call{
			To:    common.HexToAddress(call.To),
			Value: value,
			Data:  common.FromHex(call.Data),
		})
	}

	return gocalibur.SignedBatchedCall{
		BatchedCall: gocalibur.BatchedCall{
			Calls:           calls,
			RevertOnFailure: in.BatchedCall.RevertOnFailure,
		},
		Nonce:    nonce,
		KeyHash:  keyHash,
		Executor: common.HexToAddress(in.Executor),
		Deadline: deadline,
	}, nil
}

// PreviewBatch resolves the Calibur sequence for (userAddr, keyHash), builds
// the full Calibur nonce, and returns both that nonce and the EIP-712 digest
// the user must sign over the batch. This lets the dapp avoid re-deriving the
// nonce/digest client-side and guarantees the value it signs matches what the
// relayer will submit.
//
// keyHashHex empty / zero == root (secp256k1) key.
func (r *Relayer) PreviewBatch(
	ctx context.Context,
	userAddr common.Address,
	batch apiresp.CaliburBatchedCall,
	keyHashHex string,
	executor string,
	deadline string,
) (nonce string, digest string, err error) {
	var keyHash common.Hash
	if keyHashHex != "" {
		keyHash = common.HexToHash(keyHashHex)
	}

	seq, err := r.CaliburSeq(ctx, userAddr, keyHash)
	if err != nil {
		return "", "", fmt.Errorf("relay: preview seq: %w", err)
	}

	fullNonce := buildCaliburNonce(keyHash, seq)

	gocaliburBatch, err := convertSignedBatch(apiresp.CaliburSignedBatch{
		BatchedCall: batch,
		Nonce:       fullNonce.String(),
		KeyHash:     keyHash.Hex(),
		Executor:    executor,
		Deadline:    deadline,
	})
	if err != nil {
		return "", "", err
	}

	domain := gocalibur.CaliburDomain(r.chainID, userAddr, r.caliburAddress)
	d := gocalibur.ComputeDigest(domain, gocaliburBatch)

	return fullNonce.String(), hexutil.Encode(d[:]), nil
}

// buildCaliburNonce packs a 192-bit key prefix and a 64-bit sequence into the
// single uint256 Calibur uses as a per-key nonce: (keyHash[:24] << 64) | seq.
// Mirrors the dapp's buildCaliburNonce in webauthn/logic.ts.
func buildCaliburNonce(keyHash common.Hash, seq uint64) *big.Int {
	keyBig := new(big.Int).SetBytes(keyHash[:])
	mask := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 192), big.NewInt(1))
	keyBig.And(keyBig, mask)
	keyBig.Lsh(keyBig, 64)
	return keyBig.Or(keyBig, new(big.Int).SetUint64(seq))
}

// ComputeP256KeyHash mirrors how Calibur derives a passkey key-hash from a
// P-256 public key: keccak256(abi.encode(uint8(KeyTypeP256), keccak256(abi.encode(x, y)))).
//
// We use this server-side rather than trusting whatever the client claims.
func ComputeP256KeyHash(x, y *big.Int) common.Hash {
	k := gocalibur.NewWebAuthnP256Key(x, y)
	kh := k.KeyHash()
	return common.BytesToHash(kh[:])
}
