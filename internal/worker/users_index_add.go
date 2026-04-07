package worker

import (
	"context"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/pkg/event"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/grassrootseconomics/ethutils"
	"github.com/lmittmann/w3"
	"github.com/riverqueue/river"
)

type (
	UsersIndexAddArgs struct {
		TrackingID string `json:"trackingId"`
		Address    string `json:"address"`
	}

	UsersIndexAddWorker struct {
		river.WorkerDefaults[UsersIndexAddArgs]
		wc *WorkerContainer
	}
)

func (UsersIndexAddArgs) Kind() string { return store.USERS_INDEX_ADD }

func (w *UsersIndexAddWorker) Work(ctx context.Context, job *river.Job[UsersIndexAddArgs]) error {
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

	input, err := Abi[Add].EncodeArgs(w3.A(job.Args.Address))
	if err != nil {
		return err
	}

	gasSettings, err := w.wc.gasOracle.GetSettings()
	if err != nil {
		return err
	}

	usersIndex := w.wc.registry[ethutils.AccountIndex]
	gasFaucet := w.wc.registry[ethutils.GasFaucet]

	builtTx, err := w.wc.chainProvider.SignContractExecutionTx(privateKey, ethutils.ContractExecutionTxOpts{
		ContractAddress: usersIndex,
		InputData:       addDivviRefferalTag(w.wc.chainProvider, input, ethutils.HexToAddress(systemKeypair.Public)),
		GasFeeCap:       gasSettings.GasFeeCap,
		GasTipCap:       gasSettings.GasTipCap,
		GasLimit:        gasSettings.GasLimit,
		Nonce:           nonce,
	})
	if err != nil {
		return err
	}

	rawTx, err := builtTx.MarshalBinary()
	if err != nil {
		return err
	}

	rawTxHex := hexutil.Encode(rawTx)

	otxID, err := w.wc.store.InsertOTX(ctx, tx, store.OTX{
		TrackingID:    job.Args.TrackingID,
		OTXType:       store.USERS_INDEX_ADD,
		SignerAccount: systemKeypair.Public,
		RawTx:         rawTxHex,
		TxHash:        builtTx.Hash().Hex(),
		Nonce:         nonce,
	})
	if err != nil {
		return err
	}

	if err := w.wc.store.InsertDispatchTx(ctx, tx, store.DispatchTx{
		OTXID:  otxID,
		Status: store.PENDING,
	}); err != nil {
		return err
	}

	gasRefillNonce, err := w.wc.store.AcquireNonce(ctx, tx, systemKeypair.Public)
	if err != nil {
		return err
	}

	gasRefillInput, err := Abi[GiveTo].EncodeArgs(w3.A(job.Args.Address))
	if err != nil {
		return err
	}

	gasRefillTx, err := w.wc.chainProvider.SignContractExecutionTx(privateKey, ethutils.ContractExecutionTxOpts{
		ContractAddress: gasFaucet,
		InputData:       addDivviRefferalTag(w.wc.chainProvider, gasRefillInput, ethutils.HexToAddress(systemKeypair.Public)),
		GasFeeCap:       gasSettings.GasFeeCap,
		GasTipCap:       gasSettings.GasTipCap,
		GasLimit:        gasSettings.GasLimit,
		Nonce:           gasRefillNonce,
	})
	if err != nil {
		return err
	}

	rawGasRefillTx, err := gasRefillTx.MarshalBinary()
	if err != nil {
		return err
	}

	rawGasRefillTxHex := hexutil.Encode(rawGasRefillTx)

	gasRefillOTXID, err := w.wc.store.InsertOTX(ctx, tx, store.OTX{
		TrackingID:    job.Args.TrackingID,
		OTXType:       store.GAS_REFILL,
		SignerAccount: systemKeypair.Public,
		RawTx:         rawGasRefillTxHex,
		TxHash:        gasRefillTx.Hash().Hex(),
		Nonce:         gasRefillNonce,
	})
	if err != nil {
		return err
	}

	if err := w.wc.store.InsertDispatchTx(ctx, tx, store.DispatchTx{
		OTXID:  gasRefillOTXID,
		Status: store.PENDING,
	}); err != nil {
		return err
	}

	_, err = w.wc.queueClient.InsertManyTx(ctx, tx, []river.InsertManyParams{
		{
			Args: DispatchArgs{
				TrackingID: job.Args.TrackingID,
				OTXID:      otxID,
				RawTx:      rawTxHex,
			},
			InsertOpts: &river.InsertOpts{
				Priority: 1,
			},
		},
		{
			Args: DispatchArgs{
				TrackingID: job.Args.TrackingID,
				OTXID:      gasRefillOTXID,
				RawTx:      rawGasRefillTxHex,
			},
			InsertOpts: &river.InsertOpts{
				Priority: 2,
			},
		},
	})
	if err != nil {
		return err
	}

	w.wc.pub.Send(ctx, event.Event{
		TrackingID: job.Args.TrackingID,
		Status:     store.PENDING,
	})

	return tx.Commit(ctx)
}
