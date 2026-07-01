package relayer

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"math/big"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
)

// BuildAndSignDelegate constructs and signs a bare EIP-7702 SetCodeTx
// (no calldata, no value) delegating ephemeralAddr to the Calibur
// implementation. The relayer pays gas.
//
// nonce is the relayer's tx nonce, already acquired through the local nonce
// store so retries stay serial. privateKey is the master signing key loaded
// by the delegate river worker within its Work transaction.
func (r *Relayer) BuildAndSignDelegate(
	ctx context.Context,
	privateKey *ecdsa.PrivateKey,
	ephemeralAddr common.Address,
	authReq apiresp.EIP7702Authorization,
	nonce uint64,
) (*types.Transaction, error) {
	if !common.IsHexAddress(authReq.ContractAddress) {
		return nil, errors.New("delegate: invalid contractAddress in authorization")
	}
	contractAddr := common.HexToAddress(authReq.ContractAddress)
	if contractAddr != r.caliburAddress {
		return nil, fmt.Errorf("delegate: authorization contract %s != configured calibur %s",
			contractAddr.Hex(), r.caliburAddress.Hex())
	}

	authChainID, ok := new(big.Int).SetString(authReq.ChainID, 10)
	if !ok {
		return nil, errors.New("delegate: invalid authorization chainId")
	}
	if authChainID.Cmp(r.chainID) != 0 {
		return nil, fmt.Errorf("delegate: authorization chainId %s != configured %s",
			authChainID.String(), r.chainID.String())
	}

	authNonce, ok := new(big.Int).SetString(authReq.Nonce, 10)
	if !ok {
		return nil, errors.New("delegate: invalid authorization nonce")
	}

	auth, err := convertAuthorization(r.chainID, contractAddr, authNonce.Uint64(), authReq)
	if err != nil {
		return nil, err
	}

	gasFeeCap, gasTipCap, err := r.gasSettings()
	if err != nil {
		return nil, fmt.Errorf("delegate: gas settings: %w", err)
	}

	tx := types.NewTx(&types.SetCodeTx{
		ChainID:   uint256.MustFromBig(r.chainID),
		Nonce:     nonce,
		GasTipCap: uint256.MustFromBig(gasTipCap),
		GasFeeCap: uint256.MustFromBig(gasFeeCap),
		Gas:       r.delegateGas,
		To:        ephemeralAddr,
		Value:     uint256.NewInt(0),
		Data:      nil,
		AuthList:  []types.SetCodeAuthorization{auth},
	})

	signer := types.LatestSignerForChainID(r.chainID)
	signedTx, err := types.SignTx(tx, signer, privateKey)
	if err != nil {
		return nil, fmt.Errorf("delegate: sign: %w", err)
	}
	return signedTx, nil
}

func convertAuthorization(chainID *big.Int, contractAddr common.Address, nonce uint64, a apiresp.EIP7702Authorization) (types.SetCodeAuthorization, error) {
	rHash := common.HexToHash(a.R)
	sHash := common.HexToHash(a.S)

	rUint, overflow := uint256.FromBig(rHash.Big())
	if overflow {
		return types.SetCodeAuthorization{}, errors.New("authorization: r overflow")
	}
	sUint, overflow := uint256.FromBig(sHash.Big())
	if overflow {
		return types.SetCodeAuthorization{}, errors.New("authorization: s overflow")
	}
	chainUint, overflow := uint256.FromBig(chainID)
	if overflow {
		return types.SetCodeAuthorization{}, errors.New("authorization: chainID overflow")
	}

	return types.SetCodeAuthorization{
		ChainID: *chainUint,
		Address: contractAddr,
		Nonce:   nonce,
		V:       a.YParity,
		R:       *rUint,
		S:       *sUint,
	}, nil
}

func (r *Relayer) gasSettings() (gasFeeCap, gasTipCap *big.Int, err error) {
	if r.gasOracle == nil {
		return nil, nil, errors.New("gas oracle not configured")
	}
	settings, err := r.gasOracle.GetSettings()
	if err != nil {
		return nil, nil, err
	}
	if settings.GasFeeCap == nil || settings.GasTipCap == nil {
		return nil, nil, errors.New("gas oracle returned nil fee/tip cap")
	}
	return new(big.Int).Set(settings.GasFeeCap), new(big.Int).Set(settings.GasTipCap), nil
}
