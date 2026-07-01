package worker

import (
	"context"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/grassrootseconomics/ethutils"
	"github.com/lmittmann/w3"
	"github.com/riverqueue/river"
)

var accountIndexAddFunc = w3.MustNewFunc("add(address)", "")

type (
	RegisterUserArgs struct {
		TrackingID  string `json:"trackingId"`
		UserAddress string `json:"userAddress"`
	}

	RegisterUserWorker struct {
		river.WorkerDefaults[RegisterUserArgs]
		wc *WorkerContainer
	}
)

func (RegisterUserArgs) Kind() string { return "REGISTER_USER" }

// Work builds, signs, records, and dispatches an AccountIndex.add(userAddress)
// transaction — registering the user onchain. The master signing key pays gas
// and is the caller; it must be a writer on the AccountIndex contract.
//
// If no AccountIndex address is configured (zero address), the worker is a
// no-op so dev/test environments without a registry contract are unaffected.
func (w *RegisterUserWorker) Work(ctx context.Context, job *river.Job[RegisterUserArgs]) error {
	if w.wc.accountIndexAddress == (common.Address{}) {
		w.wc.logg.Debug("skipping user registration: AccountIndex not configured", "trackingId", job.Args.TrackingID)
		return nil
	}

	tx, err := w.wc.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	systemKeypair, err := w.wc.store.LoadMasterSignerKey(ctx, tx)
	if err != nil {
		return err
	}

	privateKey, err := crypto.HexToECDSA(systemKeypair.Private)
	if err != nil {
		return err
	}

	nonce, err := w.wc.store.AcquireNonce(ctx, tx, systemKeypair.Public)
	if err != nil {
		return err
	}

	calldata, err := accountIndexAddFunc.EncodeArgs(common.HexToAddress(job.Args.UserAddress))
	if err != nil {
		return err
	}

	gasSettings, err := w.wc.gasOracle.GetSettings()
	if err != nil {
		return err
	}

	builtTx, err := w.wc.chainProvider.SignContractExecutionTx(privateKey, ethutils.ContractExecutionTxOpts{
		ContractAddress: w.wc.accountIndexAddress,
		InputData:       calldata,
		GasFeeCap:       gasSettings.GasFeeCap,
		GasTipCap:       gasSettings.GasTipCap,
		GasLimit:        gasSettings.GasLimit,
		Nonce:           nonce,
	})
	if err != nil {
		return err
	}

	prepared, err := w.wc.insertTrackedTx(ctx, tx, job.Args.TrackingID, store.ACCOUNT_REGISTER, systemKeypair.Public, nonce, builtTx)
	if err != nil {
		return err
	}

	if _, err := w.wc.queueClient.InsertTx(ctx, tx, DispatchArgs{
		TrackingID: job.Args.TrackingID,
		OTXID:      prepared.OTXID,
		RawTx:      prepared.RawHex,
		TxHash:     prepared.Hash,
	}, nil); err != nil {
		return err
	}

	return tx.Commit(ctx)
}
