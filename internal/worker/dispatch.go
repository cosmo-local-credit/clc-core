package worker

import (
	"context"
	"errors"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/pkg/event"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/jackc/pgx/v5"
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

const (
	DispatchID = "DISPATCH"

	// orderingSnooze is how long a transaction waits for its predecessors to
	// leave PENDING before the dispatcher looks again.
	orderingSnooze = 2 * time.Second
)

func (DispatchArgs) Kind() string { return DispatchID }

// Work publishes one signed transaction.
//
// The signer's nonce sequence is serialised with an advisory lock and the
// transaction is held back until every lower nonce has been sent. Relay
// workers sign pairs of transactions inside the same millisecond from separate
// goroutines, and publishing those out of order puts the higher nonce in
// reth's queued sub-pool, where it is neither mined nor gossiped — and every
// EIP-7702 pool rule (gapped-nonce, in-flight limit, authority reservation) is
// order-sensitive on top of that.
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

	otx, err := w.wc.store.GetOTXByTxHash(ctx, tx, job.Args.TxHash)
	if err != nil {
		return err
	}

	if err := w.wc.store.LockSigner(ctx, tx, otx.SignerAccount); err != nil {
		return err
	}

	pending, err := w.wc.store.CountUndispatchedBelowNonce(ctx, tx, otx.SignerAccount, otx.Nonce)
	if err != nil {
		return err
	}
	if pending > 0 {
		// Rollback releases the lock; nothing has been written.
		return river.JobSnooze(orderingSnooze)
	}

	sendErr := w.wc.sendRawTx(ctx, rawTx)
	if sendErr == nil {
		return w.commitInNetwork(ctx, tx, job)
	}

	var dispatchErr *DispatchError
	if !errors.As(sendErr, &dispatchErr) {
		// sendRawTx only ever returns *DispatchError or nil; treat anything
		// else as a bug worth a River retry rather than a silent success.
		w.wc.logg.Error("dispatch: unclassified send failure", "error", sendErr)
		return sendErr
	}

	return w.commitRejection(ctx, tx, job, otx, dispatchErr)
}

func (w *DisptachWorker) commitInNetwork(ctx context.Context, tx pgx.Tx, job *river.Job[DispatchArgs]) error {
	if err := w.wc.store.UpdateDispatchTxStatus(ctx, tx, store.DispatchTx{
		OTXID:  job.Args.OTXID,
		Status: store.IN_NETWORK,
	}); err != nil {
		return err
	}
	if w.wc.pub != nil {
		_ = w.wc.pub.Send(ctx, event.Event{
			TrackingID: job.Args.TrackingID,
			Status:     store.IN_NETWORK,
		})
	}

	// Schedule a receipt poller — inserted atomically so it only runs if the
	// IN_NETWORK status update commits successfully.
	if _, err := w.wc.queueClient.InsertTx(ctx, tx, ReceiptCheckerArgs{
		TrackingID: job.Args.TrackingID,
		OTXID:      job.Args.OTXID,
		TxHash:     job.Args.TxHash,
	}, &river.InsertOpts{MaxAttempts: receiptCheckerAttempts}); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// commitRejection records the classified rejection and decides who picks the
// transaction up next.
//
// Every rejection lands here — there is no path that records IN_NETWORK for a
// transaction the node refused. A republishable verdict hands the nonce to the
// reconciler; a terminal one pages an operator.
func (w *DisptachWorker) commitRejection(
	ctx context.Context,
	tx pgx.Tx,
	job *river.Job[DispatchArgs],
	otx store.OTX,
	dispatchErr *DispatchError,
) error {
	// "already known" is not a failure: the pool holds these exact bytes, so
	// the transaction is in the network and only needs a receipt.
	if dispatchErr.Verdict == VerdictInPool {
		w.wc.logg.Info("dispatch: pool already holds this transaction",
			"txHash", job.Args.TxHash, "nonce", otx.Nonce)
		return w.commitInNetwork(ctx, tx, job)
	}

	status := dispatchStatus(dispatchErr.Err)

	level := w.wc.logg.Error
	if dispatchErr.Verdict.Republishable() {
		level = w.wc.logg.Warn
	}
	level("dispatch rejected",
		"trackingId", job.Args.TrackingID,
		"txHash", job.Args.TxHash,
		"nonce", otx.Nonce,
		"otxType", otx.OTXType,
		"verdict", dispatchErr.Verdict,
		"status", status,
		"error", dispatchErr.Err,
		// Verbatim, so the next unrecognised reth string names itself instead
		// of vanishing into a default branch.
		"rpcError", dispatchErr.OriginalErr,
	)

	if err := w.wc.store.UpdateDispatchTxStatus(ctx, tx, store.DispatchTx{
		OTXID:  job.Args.OTXID,
		Status: status,
	}); err != nil {
		return err
	}
	if w.wc.pub != nil {
		_ = w.wc.pub.Send(ctx, event.Event{
			TrackingID: job.Args.TrackingID,
			Status:     status,
		})
	}

	switch dispatchErr.Verdict {
	case VerdictNetwork:
		// The endpoint failed, not the transaction. Commit the status and let
		// River retry this job with the same bytes.
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return dispatchErr

	case VerdictTerminal, VerdictHold:
		// Nothing automatic can help: either the bytes are invalid or the
		// relayer cannot pay. Page an operator and stop retrying.
		if _, err := w.wc.queueClient.InsertTx(ctx, tx, RetrierArgs{
			TrackingID: job.Args.TrackingID,
		}, &river.InsertOpts{MaxAttempts: 1}); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return river.JobCancel(dispatchErr)

	default:
		// Transient, ordering, fee or unrecognised: the reconciler owns the
		// nonce from here. It republishes or bumps under the same advisory
		// lock, so this job must not also keep retrying.
		if _, err := w.wc.queueClient.InsertTx(ctx, tx, NonceReconcilerArgs{
			Signer: otx.SignerAccount,
		}, nil); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return river.JobCancel(dispatchErr)
	}
}

// sendRawTx publishes rawTx and classifies the outcome.
//
// It returns nil ONLY on a genuine eth_sendRawTransaction success. Every
// failure path returns a *DispatchError carrying a verdict — including
// rejections that match no known string, which become VerdictUnknown.
//
// The previous implementation could reach `return nil` three separate ways
// after a failed send (a non-rpc.Error inside w3.CallErrors, an unmatched
// error string, and the untaken outer branch), which is what recorded thirteen
// never-published transactions as IN_NETWORK on 2026-09-04 and stalled the
// relayer's nonce sequence for 66 minutes.
func (w *WorkerContainer) sendRawTx(ctx context.Context, rawTx []byte) error {
	var txHash common.Hash
	sendRawTxCall := eth.SendRawTx(rawTx).Returns(&txHash)

	err := w.chainProvider.Client.CallCtx(ctx, sendRawTxCall)
	if err == nil {
		return nil
	}

	var errs w3.CallErrors
	if errors.As(err, &errs) && len(errs) > 0 {
		inner := errs[0]

		var jsonErr rpc.Error
		if errors.As(inner, &jsonErr) {
			verdict, classified := classifyRPCError(jsonErr.Error())
			return &DispatchError{
				Err:         classified,
				OriginalErr: jsonErr,
				Verdict:     verdict,
			}
		}

		// Not a JSON-RPC error object. It may still be a transport failure
		// wrapped by w3, so try the network classifier before giving up.
		return handleNetworkError(inner)
	}

	return handleNetworkError(err)
}

func (RetrierArgs) Kind() string { return "RETRIER" }

// Work handles a deferred notification after a rejection nothing automatic can
// resolve — invalid transaction bytes, or a relayer that cannot pay. Statuses
// that the reconciler can act on never reach here.
func (w *RetrierWorker) Work(ctx context.Context, job *river.Job[RetrierArgs]) error {
	w.wc.logg.Error("dispatch permanently failed — operator action required",
		"trackingId", job.Args.TrackingID,
	)
	return nil
}
