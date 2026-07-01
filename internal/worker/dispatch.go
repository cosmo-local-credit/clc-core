package worker

import (
	"context"
	"errors"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/pkg/event"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/lmittmann/w3"
	"github.com/lmittmann/w3/module/eth"
	"github.com/riverqueue/river"
)

type (
	DispatchArgs struct {
		TrackingID string `json:"trackingId"`
		OTXID      uint64 `json:"otxId"`
		RawTx      string `json:"rawTx"`
		TxHash     string `json:"txHash"`
	}

	RetrierArgs struct {
		TrackingID string `json:"trackingId"`
	}

	DisptachWorker struct {
		river.WorkerDefaults[DispatchArgs]
		wc *WorkerContainer
	}

	RetrierWorker struct {
		river.WorkerDefaults[RetrierArgs]
		wc *WorkerContainer
	}
)

const DispatchID = "DISPATCH"

func (DispatchArgs) Kind() string { return DispatchID }

func (w *DisptachWorker) Work(ctx context.Context, job *river.Job[DispatchArgs]) error {
	tx, err := w.wc.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	rawTx, err := hexutil.Decode(job.Args.RawTx)
	if err != nil {
		return err
	}

	updateTxStatus := store.DispatchTx{
		OTXID:  job.Args.OTXID,
		Status: store.UNKNOWN_RPC_ERROR,
	}
	if err := w.sendRawTx(ctx, rawTx); err != nil {
		dispatchErr, ok := err.(*DispatchError)
		if ok {
			if dispatchErr.Err == ErrNetwork {
				updateTxStatus.Status = store.NETWORK_ERROR
				w.wc.logg.Error("Network related dispatch error", "original_error", dispatchErr.OriginalErr)
				if err := w.wc.store.UpdateDispatchTxStatus(ctx, tx, updateTxStatus); err != nil {
					return err
				}
				if w.wc.pub != nil {
					w.wc.pub.Send(ctx, event.Event{
						TrackingID: job.Args.TrackingID,
						Status:     updateTxStatus.Status,
					})
				}
				// Commit the status update before returning — River may retry this
				// job (if MaxAttempts > 1), and the status must be persisted regardless.
				if err := tx.Commit(ctx); err != nil {
					return err
				}
				return dispatchErr
			}

			w.wc.logg.Error("Chain related dispatch error", "error", dispatchErr.Err, "original_error", dispatchErr.OriginalErr)
			switch dispatchErr.Err {
			case ErrGasPriceTooLow:
				updateTxStatus.Status = store.LOW_GAS_PRICE
			case ErrInsufficientGas:
				updateTxStatus.Status = store.NO_GAS
			case ErrNonceTooLow:
				updateTxStatus.Status = store.LOW_NONCE
			case ErrReplacementTxUnderpriced:
				updateTxStatus.Status = store.REPLACEMENT_UNDERPRICED
			}

			if err := w.wc.store.UpdateDispatchTxStatus(ctx, tx, updateTxStatus); err != nil {
				return err
			}
			if w.wc.pub != nil {
				w.wc.pub.Send(ctx, event.Event{
					TrackingID: job.Args.TrackingID,
					Status:     updateTxStatus.Status,
				})
			}

			// RETRIER is inserted inside the same transaction so the ops
			// notification job is only queued if the status update commits.
			if _, err := w.wc.queueClient.InsertTx(ctx, tx, RetrierArgs{
				TrackingID: job.Args.TrackingID,
			}, &river.InsertOpts{MaxAttempts: 1}); err != nil {
				return err
			}

			// Commit before cancelling — JobCancel skips River's retry logic
			// so the deferred Rollback must not undo the status update.
			if err := tx.Commit(ctx); err != nil {
				return err
			}
			return river.JobCancel(dispatchErr)
		}

		w.wc.logg.Error("Unknown dispatch error", "error", err)
		return err
	}

	updateTxStatus.Status = store.IN_NETWORK
	if err := w.wc.store.UpdateDispatchTxStatus(ctx, tx, updateTxStatus); err != nil {
		return err
	}
	if w.wc.pub != nil {
		w.wc.pub.Send(ctx, event.Event{
			TrackingID: job.Args.TrackingID,
			Status:     updateTxStatus.Status,
		})
	}

	// Schedule a receipt poller — inserted atomically so it only runs if the
	// IN_NETWORK status update commits successfully.
	if _, err := w.wc.queueClient.InsertTx(ctx, tx, ReceiptCheckerArgs{
		TrackingID: job.Args.TrackingID,
		OTXID:      job.Args.OTXID,
		TxHash:     job.Args.TxHash,
	}, &river.InsertOpts{MaxAttempts: 30}); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (w *DisptachWorker) sendRawTx(ctx context.Context, rawTx []byte) error {
	var txHash common.Hash
	sendRawTxCall := eth.SendRawTx(rawTx).Returns(&txHash)

	var errs w3.CallErrors
	if err := w.wc.chainProvider.Client.CallCtx(ctx, sendRawTxCall); errors.As(err, &errs) {
		jsonErr, ok := errs[0].(rpc.Error)
		if ok {
			if jsonRPCError := handleJSONRPCError(jsonErr.Error()); jsonRPCError != nil {
				return &DispatchError{
					Err:         jsonRPCError,
					OriginalErr: jsonErr,
				}
			}
		}
	} else if err != nil {
		return handleNetworkError(err)
	}

	return nil
}

func (RetrierArgs) Kind() string { return "RETRIER" }

// Work handles a deferred notification after a terminal chain-level dispatch
// failure. The DispatchWorker has already committed the terminal status
// (NO_GAS, LOW_NONCE, etc.) before inserting this job, so the only
// responsibility here is to surface a clear operator-facing log entry.
func (w *RetrierWorker) Work(ctx context.Context, job *river.Job[RetrierArgs]) error {
	w.wc.logg.Error("dispatch permanently failed — operator action required",
		"trackingId", job.Args.TrackingID,
	)
	return nil
}
