package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/gas"
	"github.com/cosmo-local-credit/clc-core/internal/pub"
	"github.com/cosmo-local-credit/clc-core/internal/relayer"
	"github.com/cosmo-local-credit/clc-core/internal/safe"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/ethereum/go-ethereum/common"
	"github.com/grassrootseconomics/ethutils"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

const (
	migrationTimeout = 15 * time.Second
)

type (
	// infoHandler suppresses debug logs from River without touching the global logger.
	infoHandler struct{ slog.Handler }

	WorkerOpts struct {
		MaxWorkers          int
		GasOracle           gas.GasOracle
		Store               store.Store
		FederationStore     store.FederationStore
		Logg                *slog.Logger
		ChainProvider       *ethutils.Provider
		Pub                 *pub.Pub
		Relayer             *relayer.Relayer
		AccountIndexAddress common.Address
		// ChainID scopes Safe signer projections, as in the subscriber.
		ChainID int64
		// SafeExecutor enables executing confirmed Safe transactions from the
		// system signer; nil leaves execution to the Safe app.
		SafeExecutor *safe.Executor
	}

	WorkerContainer struct {
		queueClient         *river.Client[pgx.Tx]
		maxWorkers          int
		gasOracle           gas.GasOracle
		store               store.Store
		federationStore     store.FederationStore
		logg                *slog.Logger
		chainProvider       *ethutils.Provider
		pub                 *pub.Pub
		relayer             *relayer.Relayer
		accountIndexAddress common.Address
		chainID             int64
		safeExecutor        *safe.Executor
	}
)

func (h infoHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= slog.LevelInfo }

func New(o WorkerOpts) *WorkerContainer {
	return &WorkerContainer{
		maxWorkers:          o.MaxWorkers,
		gasOracle:           o.GasOracle,
		store:               o.Store,
		federationStore:     o.FederationStore,
		logg:                o.Logg,
		chainProvider:       o.ChainProvider,
		pub:                 o.Pub,
		relayer:             o.Relayer,
		accountIndexAddress: o.AccountIndexAddress,
		chainID:             o.ChainID,
		safeExecutor:        o.SafeExecutor,
	}
}

func (w *WorkerContainer) Bootstrap() error {
	ctx, cancel := context.WithTimeout(context.Background(), migrationTimeout)
	defer cancel()

	riverPgxDriver := riverpgxv5.New(w.store.Pool())
	riverMigrator, err := rivermigrate.New(riverPgxDriver, nil)
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
		Logger:  slog.New(infoHandler{w.logg.Handler()}),
		// The periodic sweep is the backstop for anything the dispatcher and
		// the receipt pollers miss: a rejection nobody classified, an RPC that
		// dropped a transaction after accepting it, or a process that died
		// between signing and publishing. Without it a stuck nonce is only
		// noticed when someone reads the otx table.
		PeriodicJobs: []*river.PeriodicJob{
			river.NewPeriodicJob(
				river.PeriodicInterval(reconcileInterval),
				func() (river.JobArgs, *river.InsertOpts) {
					return NonceReconcilerArgs{}, nil
				},
				&river.PeriodicJobOpts{RunOnStart: true},
			),
		},
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
	w.logg.Info("Shutting down worker queue")
	return w.queueClient.Stop(ctx)
}

func (w *WorkerContainer) Client() *river.Client[pgx.Tx] {
	return w.queueClient
}

func setupWorkers(wc *WorkerContainer) (*river.Workers, error) {
	workers := river.NewWorkers()

	if err := river.AddWorkerSafely(workers, &DeployTokenWorker{wc: wc}); err != nil {
		return nil, err
	}

	if err := river.AddWorkerSafely(workers, &DeployPoolWorker{wc: wc}); err != nil {
		return nil, err
	}

	if err := river.AddWorkerSafely(workers, &DisptachWorker{wc: wc}); err != nil {
		return nil, err
	}

	if err := river.AddWorkerSafely(workers, &RetrierWorker{wc: wc}); err != nil {
		return nil, err
	}

	if err := river.AddWorkerSafely(workers, &ReceiptCheckerWorker{wc: wc}); err != nil {
		return nil, err
	}

	if err := river.AddWorkerSafely(workers, &UpgradeProxyWorker{wc: wc}); err != nil {
		return nil, err
	}

	if err := river.AddWorkerSafely(workers, &RelayDelegateWorker{wc: wc}); err != nil {
		return nil, err
	}

	if err := river.AddWorkerSafely(workers, &RelayWorker{wc: wc}); err != nil {
		return nil, err
	}

	if err := river.AddWorkerSafely(workers, &ApproveLegacyUserWorker{wc: wc}); err != nil {
		return nil, err
	}

	if err := river.AddWorkerSafely(workers, &RegisterUserWorker{wc: wc}); err != nil {
		return nil, err
	}

	if err := river.AddWorkerSafely(workers, &NonceReconcilerWorker{wc: wc}); err != nil {
		return nil, err
	}

	if err := river.AddWorkerSafely(workers, &SafeSignersRefreshWorker{wc: wc, reader: safe.NewReader(wc.chainProvider)}); err != nil {
		return nil, err
	}

	if wc.safeExecutor != nil {
		if err := river.AddWorkerSafely(workers, &SafeExecuteWorker{wc: wc, executor: wc.safeExecutor}); err != nil {
			return nil, err
		}
	}

	return workers, nil
}
