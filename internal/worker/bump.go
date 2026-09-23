package worker

import (
	"crypto/ecdsa"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
)

// reth (and geth) require a replacement to beat the pooled transaction by
// 12.5% on BOTH the fee cap and the tip. On Gnosis the values are single- and
// double-digit wei, where 12.5% of 12 rounds back to 12 — hence the +1 floor,
// without which the replacement is itself rejected as "replacement transaction
// underpriced".
const (
	replacementNumerator   = 1125
	replacementDenominator = 1000
)

// bumpFee returns the smallest value that clears the replacement threshold
// over prev.
func bumpFee(prev *big.Int) *big.Int {
	if prev == nil || prev.Sign() <= 0 {
		return big.NewInt(1)
	}

	bumped := new(big.Int).Mul(prev, big.NewInt(replacementNumerator))
	bumped.Div(bumped, big.NewInt(replacementDenominator))

	if minimum := new(big.Int).Add(prev, big.NewInt(1)); bumped.Cmp(minimum) < 0 {
		bumped.Set(minimum)
	}

	return bumped
}

// rebuildAtHigherFee re-signs an already-signed transaction at the same nonce
// with higher fees, preserving every other field including the EIP-7702
// authorization list.
//
// It works from the stored raw bytes rather than rebuilding the payload
// through the relayer, so a bump needs no knowledge of which OTX type produced
// the transaction and cannot re-derive a different payload than the one the
// user's signature covers. The authorization signatures inside a SetCodeTx are
// over the authority's own tuple, not the outer transaction, so they survive
// re-signing untouched.
//
// target is the fee cap the gas oracle currently wants; the result is the
// higher of that and the replacement threshold over the previous values.
func rebuildAtHigherFee(signer types.Signer, prev *types.Transaction, targetFeeCap, targetTipCap *big.Int, key *ecdsa.PrivateKey) (*types.Transaction, error) {
	feeCap := bumpFee(prev.GasFeeCap())
	if targetFeeCap != nil && targetFeeCap.Cmp(feeCap) > 0 {
		feeCap = new(big.Int).Set(targetFeeCap)
	}

	tipCap := bumpFee(prev.GasTipCap())
	if targetTipCap != nil && targetTipCap.Cmp(tipCap) > 0 {
		tipCap = new(big.Int).Set(targetTipCap)
	}

	// A tip above the cap is invalid ("max priority fee per gas higher than
	// max fee per gas"); clamp rather than emit bytes the pool will reject.
	if tipCap.Cmp(feeCap) > 0 {
		tipCap = new(big.Int).Set(feeCap)
	}

	to := prev.To()
	if to == nil {
		return nil, errors.New("bump: contract creation is not a relayer transaction shape")
	}

	var inner types.TxData

	switch prev.Type() {
	case types.DynamicFeeTxType:
		inner = &types.DynamicFeeTx{
			ChainID:    prev.ChainId(),
			Nonce:      prev.Nonce(),
			GasTipCap:  tipCap,
			GasFeeCap:  feeCap,
			Gas:        prev.Gas(),
			To:         to,
			Value:      prev.Value(),
			Data:       prev.Data(),
			AccessList: prev.AccessList(),
		}
	case types.SetCodeTxType:
		feeCapU, overflow := uint256.FromBig(feeCap)
		if overflow {
			return nil, errors.New("bump: fee cap overflows uint256")
		}
		tipCapU, overflow := uint256.FromBig(tipCap)
		if overflow {
			return nil, errors.New("bump: tip cap overflows uint256")
		}
		chainID, overflow := uint256.FromBig(prev.ChainId())
		if overflow {
			return nil, errors.New("bump: chain id overflows uint256")
		}
		value, overflow := uint256.FromBig(prev.Value())
		if overflow {
			return nil, errors.New("bump: value overflows uint256")
		}

		inner = &types.SetCodeTx{
			ChainID:    chainID,
			Nonce:      prev.Nonce(),
			GasTipCap:  tipCapU,
			GasFeeCap:  feeCapU,
			Gas:        prev.Gas(),
			To:         *to,
			Value:      value,
			Data:       prev.Data(),
			AccessList: prev.AccessList(),
			AuthList:   prev.SetCodeAuthorizations(),
		}
	default:
		return nil, fmt.Errorf("bump: unsupported tx type %d", prev.Type())
	}

	return types.SignNewTx(key, signer, inner)
}
