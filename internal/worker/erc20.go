package worker

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"strconv"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/protocol/pkg/publish"
	"github.com/cosmo-local-credit/protocol/pkg/publish/contracts/giftabletoken"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/grassrootseconomics/ethutils"
	"github.com/jackc/pgx/v5"
	"github.com/lmittmann/w3"
	"github.com/riverqueue/river"
)

var funcDeployDeterministicAndCall = w3.MustNewFunc("deployDeterministicAndCall(address,address,bytes32,bytes)", "address")

type (
	DeployTokenArgs struct {
		TrackingID            string `json:"trackingId"`
		Name                  string `json:"name"`
		Symbol                string `json:"symbol"`
		Decimals              string `json:"decimals"`
		Owner                 string `json:"owner"`
		ExpiryTimestamp       string `json:"expiryTimestamp"`
		FactoryAddress        string `json:"factoryAddress"`
		ImplementationAddress string `json:"implementationAddress"`
		PlatformContractID    uint64 `json:"platformContractId"`
	}

	DeployTokenWorker struct {
		river.WorkerDefaults[DeployTokenArgs]
		wc *WorkerContainer
	}

	preparedTx struct {
		OTXID  uint64
		Hash   string
		RawHex string
	}
)

func (DeployTokenArgs) Kind() string { return "TOKEN_DEPLOY" }

func (w *DeployTokenWorker) Work(ctx context.Context, job *river.Job[DeployTokenArgs]) error {
	args := job.Args

	decimals, err := strconv.ParseUint(args.Decimals, 10, 8)
	if err != nil {
		return err
	}

	expiresAt := int64(0)
	if args.ExpiryTimestamp != "" {
		expiresAt, err = strconv.ParseInt(args.ExpiryTimestamp, 10, 64)
		if err != nil {
			return err
		}
	}

	privateKey, signerAddress, err := w.wc.loadMasterSigner(ctx)
	if err != nil {
		return err
	}

	gasSettings, err := w.wc.gasOracle.GetSettings()
	if err != nil {
		return err
	}

	initData, err := giftabletoken.EncodeInit(giftabletoken.InitArgs{
		Name:      args.Name,
		Symbol:    args.Symbol,
		Decimals:  uint8(decimals),
		Owner:     common.HexToAddress(args.Owner),
		ExpiresAt: big.NewInt(expiresAt),
	})
	if err != nil {
		return err
	}

	salt := createSalt(signerAddress, args.TrackingID, "token")

	calldata, err := funcDeployDeterministicAndCall.EncodeArgs(
		common.HexToAddress(args.ImplementationAddress),
		signerAddress,
		salt,
		initData,
	)
	if err != nil {
		return err
	}

	proxyTx, err := w.wc.prepareContractExecutionTx(
		ctx,
		args.TrackingID,
		store.TOKEN_PROXY_DEPLOY,
		signerAddress,
		privateKey,
		common.HexToAddress(args.FactoryAddress),
		calldata,
		publish.ProxyGasLimit,
		gasSettings.GasFeeCap,
		gasSettings.GasTipCap,
	)
	if err != nil {
		return err
	}

	metaTx, err := w.wc.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer metaTx.Rollback(ctx)

	if err := w.wc.store.InsertTokenDeployMeta(ctx, metaTx, store.TokenDeployMeta{
		OTXID:              proxyTx.OTXID,
		PlatformContractID: args.PlatformContractID,
		TokenName:          args.Name,
		TokenSymbol:        args.Symbol,
		Admin:              signerAddress.Hex(),
	}); err != nil {
		return err
	}

	if err := metaTx.Commit(ctx); err != nil {
		return err
	}

	_, err = w.wc.queueClient.InsertMany(ctx, []river.InsertManyParams{
		{
			Args: DispatchArgs{
				TrackingID: args.TrackingID,
				OTXID:      proxyTx.OTXID,
				RawTx:      proxyTx.RawHex,
			},
		},
	})
	return err
}

func (w *WorkerContainer) loadMasterSigner(ctx context.Context) (*ecdsa.PrivateKey, common.Address, error) {
	tx, err := w.store.Pool().Begin(ctx)
	if err != nil {
		return nil, common.Address{}, err
	}
	defer tx.Rollback(ctx)

	systemKeypair, err := w.store.LoadMasterSignerKey(ctx, tx)
	if err != nil {
		return nil, common.Address{}, err
	}

	privateKey, err := crypto.HexToECDSA(systemKeypair.Private)
	if err != nil {
		return nil, common.Address{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, common.Address{}, err
	}

	return privateKey, common.HexToAddress(systemKeypair.Public), nil
}

func (w *WorkerContainer) prepareContractExecutionTx(
	ctx context.Context,
	trackingID string,
	otxType string,
	signerAddress common.Address,
	privateKey *ecdsa.PrivateKey,
	contractAddress common.Address,
	inputData []byte,
	gasLimit uint64,
	gasFeeCap *big.Int,
	gasTipCap *big.Int,
) (*preparedTx, error) {
	tx, err := w.store.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	nonce, err := w.store.AcquireNonce(ctx, tx, signerAddress.Hex())
	if err != nil {
		return nil, err
	}

	builtTx, err := w.chainProvider.SignContractExecutionTx(privateKey, ethutils.ContractExecutionTxOpts{
		ContractAddress: contractAddress,
		InputData:       inputData,
		GasFeeCap:       gasFeeCap,
		GasTipCap:       gasTipCap,
		GasLimit:        gasLimit,
		Nonce:           nonce,
	})
	if err != nil {
		return nil, err
	}

	prepared, err := w.insertTrackedTx(ctx, tx, trackingID, otxType, signerAddress.Hex(), nonce, builtTx)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return prepared, nil
}

func (w *WorkerContainer) insertTrackedTx(ctx context.Context, tx pgx.Tx, trackingID string, otxType string, signerAccount string, nonce uint64, builtTx *types.Transaction) (*preparedTx, error) {
	rawTx, err := builtTx.MarshalBinary()
	if err != nil {
		return nil, err
	}

	rawTxHex := hexutil.Encode(rawTx)
	otxID, err := w.store.InsertOTX(ctx, tx, store.OTX{
		TrackingID:    trackingID,
		OTXType:       otxType,
		SignerAccount: signerAccount,
		RawTx:         rawTxHex,
		TxHash:        builtTx.Hash().Hex(),
		Nonce:         nonce,
	})
	if err != nil {
		return nil, err
	}

	if err := w.store.InsertDispatchTx(ctx, tx, store.DispatchTx{OTXID: otxID, Status: store.PENDING}); err != nil {
		return nil, err
	}

	return &preparedTx{
		OTXID:  otxID,
		Hash:   builtTx.Hash().Hex(),
		RawHex: rawTxHex,
	}, nil
}
