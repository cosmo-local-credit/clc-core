package nextworker

import (
	"context"
	"log/slog"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/gas"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/grassrootseconomics/ethutils"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

const migrationTimeout = 15 * time.Second

type (
	WorkerOpts struct {
		MaxWorkers    int
		GasOracle     gas.GasOracle
		Store         store.Store
		Logg          *slog.Logger
		ChainProvider *ethutils.Provider
	}

	WorkerContainer struct {
		queueClient   *river.Client[pgx.Tx]
		maxWorkers    int
		gasOracle     gas.GasOracle
		store         store.Store
		logg          *slog.Logger
		chainProvider *ethutils.Provider
	}
)

func New(o WorkerOpts) *WorkerContainer {
	return &WorkerContainer{
		maxWorkers:    o.MaxWorkers,
		gasOracle:     o.GasOracle,
		store:         o.Store,
		logg:          o.Logg,
		chainProvider: o.ChainProvider,
	}
}

func (w *WorkerContainer) Bootstrap() error {
	ctx, cancel := context.WithTimeout(context.Background(), migrationTimeout)
	defer cancel()

	riverPgxDriver := riverpgxv5.New(w.store.Pool())
	riverMigrator, err := rivermigrate.New(riverPgxDriver, &rivermigrate.Config{Logger: w.logg})
	if err != nil {
		return err
	}

	if _, err = riverMigrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return err
	}

	workers, err := setupWorkers(w)
	if err != nil {
		return err
	}

	w.queueClient, err = river.NewClient(riverPgxDriver, &river.Config{
		Queues: map[string]river.QueueConfig{
			river.QueueDefault: {
				MaxWorkers: w.maxWorkers,
			},
		},
		Workers: workers,
		Logger:  w.logg,
	})
	if err != nil {
		return err
	}

	return nil
}

func (w *WorkerContainer) Start(ctx context.Context) error {
	return w.queueClient.Start(ctx)
}

func (w *WorkerContainer) Stop(ctx context.Context) error {
	w.logg.Info("shutting down next-worker queue")
	return w.queueClient.Stop(ctx)
}

func (w *WorkerContainer) Client() *river.Client[pgx.Tx] {
	return w.queueClient
}

func setupWorkers(wc *WorkerContainer) (*river.Workers, error) {
	workers := river.NewWorkers()

	if err := river.AddWorkerSafely(workers, &DeployERC20Worker{wc: wc}); err != nil {
		return nil, err
	}

	if err := river.AddWorkerSafely(workers, &UpgradeERC20Worker{wc: wc}); err != nil {
		return nil, err
	}

	return workers, nil
}
