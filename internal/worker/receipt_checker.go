package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/pkg/event"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/lmittmann/w3/module/eth"
	"github.com/riverqueue/river"
)

type (
	ReceiptCheckerArgs struct {
		TrackingID string `json:"trackingId"`
		OTXID      uint64 `json:"otxId"`
		TxHash     string `json:"txHash"`
	}

	ReceiptCheckerWorker struct {
		river.WorkerDefaults[ReceiptCheckerArgs]

		wc *WorkerContainer
	}
)

func (ReceiptCheckerArgs) Kind() string { return "RECEIPT_CHECKER" }

const (
	// receiptCheckerAttempts bounds the poller. Past this the reconciler takes
	// over, so the tail no longer has to be long enough to cover a stuck nonce.
	receiptCheckerAttempts = 20
)

// NextRetry polls quickly at first (one block on Celo and Gnosis alike, ~5s)
// then backs off to avoid hammering the RPC on slow or stuck transactions.
//
// The method name is load-bearing: River's hook is NextRetry(*Job[T]), and
// river.WorkerDefaults[T] already supplies a zero-value implementation of it.
// A differently named method — this was NextRetryAt(job, error) until
// 2026-09-04 — compiles fine, is never called, and River silently falls back
// to DefaultClientRetryPolicy's attempt^4 seconds. That put attempt 6 twenty-
// two minutes out and attempt 7 forty, so a transaction mined at t+10s kept
// reporting IN_NETWORK for the better part of an hour.
func (w *ReceiptCheckerWorker) NextRetry(job *river.Job[ReceiptCheckerArgs]) time.Time {
	switch {
	case job.Attempt <= 6:
		return time.Now().Add(5 * time.Second) // first ~30s: check every block
	case job.Attempt <= 15:
		return time.Now().Add(15 * time.Second) // next ~2min: every 3 blocks
	default:
		return time.Now().Add(60 * time.Second) // long tail: once a minute
	}
}

// Work fetches the transaction receipt from the chain and updates the dispatch
// status to SUCCESS or REVERTED. If the receipt is not yet available it
// returns an error so River retries according to NextRetry.
func (w *ReceiptCheckerWorker) Work(ctx context.Context, job *river.Job[ReceiptCheckerArgs]) error {
	// eth.TxReceipt returns *types.Receipt — pass a pointer-to-pointer.
	var receipt *types.Receipt
	if err := w.wc.chainProvider.Client.CallCtx(ctx,
		eth.TxReceipt(common.HexToHash(job.Args.TxHash)).Returns(&receipt),
	); err != nil || receipt == nil {
		// Out of attempts: hand the signer to the reconciler rather than let
		// the job die and leave the dispatch row at IN_NETWORK forever.
		if job.Attempt >= job.MaxAttempts {
			if _, insErr := w.wc.queueClient.Insert(ctx, NonceReconcilerArgs{}, nil); insErr != nil {
				return insErr
			}
			w.wc.logg.Warn("receipt poller exhausted — handing off to the reconciler",
				"trackingId", job.Args.TrackingID,
				"txHash", job.Args.TxHash,
				"attempts", job.Attempt,
			)
			return nil
		}

		// Not mined yet — River will retry.
		return fmt.Errorf("receipt not yet available for %s (attempt %d)",
			job.Args.TxHash, job.Attempt)
	}

	status := store.SUCCESS
	if receipt.Status == 0 {
		status = store.REVERTED
	}

	tx, err := w.wc.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

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

	w.wc.logg.Info("receipt confirmed",
		"trackingId", job.Args.TrackingID,
		"txHash", job.Args.TxHash,
		"status", status,
		"blockNumber", receipt.BlockNumber.Uint64(),
	)

	return tx.Commit(ctx)
}
