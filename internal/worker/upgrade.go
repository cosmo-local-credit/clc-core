package worker

import (
	"context"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/protocol/pkg/publish"
	"github.com/ethereum/go-ethereum/common"
	"github.com/lmittmann/w3"
	"github.com/riverqueue/river"
)

var (
	funcUpgrade = w3.MustNewFunc("upgrade(address,address)", "")
)

type (
	UpgradeProxyArgs struct {
		TrackingID     string `json:"trackingId"`
		ProxyAddress   string `json:"proxyAddress"`
		NewImplAddress string `json:"newImplAddress"`
		FactoryAddress string `json:"factoryAddress"`
		ContractType   string `json:"contractType"`
		Version        string `json:"version"`
	}

	UpgradeProxyWorker struct {
		river.WorkerDefaults[UpgradeProxyArgs]

		wc *WorkerContainer
	}
)

func (UpgradeProxyArgs) Kind() string { return "PROXY_UPGRADE" }

func (w *UpgradeProxyWorker) Work(ctx context.Context, job *river.Job[UpgradeProxyArgs]) error {
	args := job.Args

	privateKey, signerAddress, err := w.wc.loadMasterSigner(ctx)
	if err != nil {
		return err
	}

	gasSettings, err := w.wc.gasOracle.GetSettings()
	if err != nil {
		return err
	}

	calldata, err := funcUpgrade.EncodeArgs(
		common.HexToAddress(args.ProxyAddress),
		common.HexToAddress(args.NewImplAddress),
	)
	if err != nil {
		return err
	}

	proxyTx, err := w.wc.prepareContractExecutionTx(
		ctx,
		args.TrackingID,
		store.PROXY_UPGRADE,
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

	if err := w.wc.store.InsertUpgradeMeta(ctx, metaTx, store.UpgradeMeta{
		OTXID:             proxyTx.OTXID,
		ProxyAddress:      args.ProxyAddress,
		ContractType:      args.ContractType,
		NewImplementation: args.NewImplAddress,
		Version:           args.Version,
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
				TxHash:     proxyTx.Hash,
			},
		},
	})
	return err
}
