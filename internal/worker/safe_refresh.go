package worker

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/cosmo-local-credit/clc-core/internal/safe"
	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

type (
	// SafeSignersRefreshArgs re-reads one address's signer set at the chain
	// head. The subscriber queues it when it cannot read the set at an event's
	// block, most often because it fell behind the RPC's pruning window, so the
	// event's catalog and OTX projections still commit.
	SafeSignersRefreshArgs struct {
		Address string `json:"address"`
		// MinBlock is the event's block. A head below it (a lagging RPC node)
		// cannot reflect the event yet, so the job retries rather than store an
		// older set.
		MinBlock uint64 `json:"minBlock"`
		// Register queues AccountIndex registration for a multisig that owns a
		// catalog asset, as the ownership transfer that queued it would have.
		Register bool `json:"register"`
	}

	// safeHeadReader reads a signer set at the chain head; safe.Reader
	// satisfies it.
	safeHeadReader interface {
		ReadSafeLatest(ctx context.Context, address common.Address) (*safe.State, uint64, error)
	}

	SafeSignersRefreshWorker struct {
		river.WorkerDefaults[SafeSignersRefreshArgs]

		wc     *WorkerContainer
		reader safeHeadReader
	}
)

func (SafeSignersRefreshArgs) Kind() string { return "SAFE_SIGNERS_REFRESH" }

// EnqueueSafeSignersRefresh commits the refresh with the event that needed it.
// Jobs are not deduplicated: a running refresh may already have read a head
// older than a newer event, and a duplicate costs only one more read.
func EnqueueSafeSignersRefresh(ctx context.Context, queue *river.Client[pgx.Tx], tx pgx.Tx, address string, minBlock uint64, register bool) error {
	if queue == nil {
		return errors.New("safe signer refresh: worker queue not configured")
	}
	_, err := queue.InsertTx(ctx, tx, SafeSignersRefreshArgs{
		Address: strings.ToLower(address), MinBlock: minBlock, Register: register,
	}, nil)
	return err
}

// Work stores the head's set at (head, MaxInt64), the position inspection and
// cmd/reconcile-ownership use: it dominates every log in that block and yields
// to any later event.
func (w *SafeSignersRefreshWorker) Work(ctx context.Context, job *river.Job[SafeSignersRefreshArgs]) error {
	if !common.IsHexAddress(job.Args.Address) {
		return river.JobCancel(fmt.Errorf("safe signer refresh: invalid address %q", job.Args.Address))
	}
	state, head, err := w.reader.ReadSafeLatest(ctx, common.HexToAddress(job.Args.Address))
	switch {
	case err != nil && !errors.Is(err, safe.ErrNotSafe):
		return err
	case head < job.Args.MinBlock:
		return fmt.Errorf("safe signer refresh: RPC head %d is behind event block %d", head, job.Args.MinBlock)
	case err != nil:
		return nil // not a multisig at the head
	case head > math.MaxInt64:
		return river.JobCancel(fmt.Errorf("safe signer refresh: head %d out of range", head))
	}

	signers := make([]string, len(state.Owners))
	for i, owner := range state.Owners {
		signers[i] = owner.Hex()
	}
	tx, err := w.wc.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := w.wc.store.ReplaceSafeSigners(ctx, tx, w.wc.chainID, job.Args.Address, signers, state.Threshold, head, math.MaxInt64); err != nil {
		return err
	}
	if job.Args.Register {
		owned, err := w.wc.store.CatalogHasOwner(ctx, tx, w.wc.chainID, job.Args.Address)
		if err != nil {
			return err
		}
		if owned {
			if err := EnqueueSafeRegistration(ctx, w.wc.queueClient, tx, job.Args.Address); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}
