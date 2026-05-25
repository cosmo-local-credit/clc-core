package relayer

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	gocalibur "github.com/grassrootseconomics/go-calibur"
	"github.com/holiman/uint256"
	"github.com/lmittmann/w3/module/eth"
)

// BuildRelayResult is what BuildAndSignRelay returns so the caller knows
// which OTX type to record against the transaction.
type BuildRelayResult struct {
	Tx          *types.Transaction
	OTXType     string // store.RELAY_EXEC or store.RELAY_DELEGATE_EXEC
	IsDelegated bool
}

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
// When isDelegated is true a normal DynamicFeeTx to userAddr is produced.
// Otherwise a SetCodeTx with an AuthList entry is produced — auth must be
// non-nil in that case (the caller is responsible for validating presence).
func (r *Relayer) BuildAndSignRelay(
	ctx context.Context,
	userAddr common.Address,
	signedBatch apiresp.CaliburSignedBatch,
	signature []byte,
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
	if gocaliburBatch.Executor != (common.Address{}) && gocaliburBatch.Executor != r.address {
		return nil, fmt.Errorf("relay: executor %s != relayer %s",
			gocaliburBatch.Executor.Hex(), r.address.Hex())
	}

	calldata, err := gocalibur.EncodeRelayedCalldata(gocaliburBatch, signature)
	if err != nil {
		return nil, fmt.Errorf("relay: encode calldata: %w", err)
	}

	gasFeeCap, gasTipCap, err := r.gasSettings()
	if err != nil {
		return nil, fmt.Errorf("relay: gas settings: %w", err)
	}

	if isDelegated {
		tx := types.NewTx(&types.DynamicFeeTx{
			ChainID:   r.chainID,
			Nonce:     relayerNonce,
			GasTipCap: gasTipCap,
			GasFeeCap: gasFeeCap,
			Gas:       r.setCodeGas,
			To:        &userAddr,
			Value:     big.NewInt(0),
			Data:      calldata,
		})
		signed, err := types.SignTx(tx, types.LatestSignerForChainID(r.chainID), r.privateKey)
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
		Gas:       r.setCodeGas,
		To:        userAddr,
		Value:     uint256.NewInt(0),
		Data:      calldata,
		AuthList:  []types.SetCodeAuthorization{scAuth},
	})
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(r.chainID), r.privateKey)
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

// ComputeP256KeyHash mirrors how Calibur derives a passkey key-hash from a
// P-256 public key: keccak256(abi.encode(uint8(KeyTypeP256), keccak256(abi.encode(x, y)))).
//
// We use this server-side rather than trusting whatever the client claims.
func ComputeP256KeyHash(x, y *big.Int) common.Hash {
	k := gocalibur.NewWebAuthnP256Key(x, y)
	kh := k.KeyHash()
	return common.BytesToHash(kh[:])
}
