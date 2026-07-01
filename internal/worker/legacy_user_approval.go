package worker

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

type (
	ApproveLegacyUserArgs struct {
		Address string `json:"address"`
	}

	ApproveLegacyUserWorker struct {
		river.WorkerDefaults[ApproveLegacyUserArgs]

		wc *WorkerContainer
	}
)

func (ApproveLegacyUserArgs) Kind() string { return "APPROVE_LEGACY_USER" }

func (w *ApproveLegacyUserWorker) Work(ctx context.Context, job *river.Job[ApproveLegacyUserArgs]) error {
	if w.wc.federationStore == nil {
		w.wc.logg.Warn("skipping legacy user approval: federation store unavailable", "address", job.Args.Address)
		return nil
	}

	exists, err := w.wc.federationStore.LegacyUserExistsByBlockchainAddress(ctx, job.Args.Address)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}

	tx, err := w.wc.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := w.wc.store.ApprovePendingUserByAddress(ctx, tx, job.Args.Address); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}

	return tx.Commit(ctx)
}
