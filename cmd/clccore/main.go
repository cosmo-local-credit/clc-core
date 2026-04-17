package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/api"
	"github.com/cosmo-local-credit/clc-core/internal/gas"
	internaljs "github.com/cosmo-local-credit/clc-core/internal/jetstream"
	"github.com/cosmo-local-credit/clc-core/internal/pub"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/internal/sub"
	"github.com/cosmo-local-credit/clc-core/internal/util"
	"github.com/cosmo-local-credit/clc-core/internal/worker"
	"github.com/grassrootseconomics/ethutils"
	"github.com/knadh/koanf/v2"
)

const defaultGracefulShutdownPeriod = time.Second * 20

var (
	build = "dev"

	confFlag             string
	migrationsFolderFlag string
	queriesFlag          string

	lo *slog.Logger
	ko *koanf.Koanf
)

func init() {
	flag.StringVar(&confFlag, "config", "config.toml", "Config file location")
	flag.StringVar(&migrationsFolderFlag, "migrations", "migrations/", "Migrations folder location")
	flag.StringVar(&queriesFlag, "queries", "queries.sql", "Queries file location")
	flag.Parse()

	lo = util.InitLogger()
	ko = util.InitConfig(lo, confFlag)
}

func main() {
	var (
		wg sync.WaitGroup

		workerComponent *worker.WorkerContainer
		apiComponent    *api.API
		gasOracleInst   gas.GasOracle
		subComponent    *sub.Sub
		pubComponent    *pub.Pub
	)

	ctx, stop := notifyShutdown()

	pgStore := loadStore()
	chainProvider := loadChainProvider()

	switch mode := ko.MustString("service.mode"); mode {
	case "worker":
		gasOracleInst = loadGasOracle(chainProvider)
		workerComponent = loadWorker(pgStore, chainProvider, gasOracleInst)
	case "api":
		apiComponent = loadAPI(pgStore, chainProvider, nil)
	case "sub":
		subComponent, pubComponent = loadSub(pgStore)
	case "standalone":
		gasOracleInst = loadGasOracle(chainProvider)
		workerComponent = loadWorker(pgStore, chainProvider, gasOracleInst)
		apiComponent = loadAPI(pgStore, chainProvider, workerComponent)
	default:
		lo.Error("service.mode must be one of: standalone, api, worker, sub")
		os.Exit(1)
	}

	lo.Info("starting clc-core", "build", build, "mode", ko.String("service.mode"))

	if workerComponent != nil {
		wg.Go(func() {
			gasOracleInst.Start()
		})

		wg.Go(func() {
			if err := workerComponent.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
				lo.Error("worker queue exited unexpectedly", "err", fmt.Sprintf("%T: %v", err, err))
				os.Exit(1)
			}
		})
	}

	if apiComponent != nil {
		wg.Go(func() {
			if err := apiComponent.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				lo.Error("API server exited unexpectedly", "err", fmt.Sprintf("%T: %v", err, err))
				os.Exit(1)
			}
		})
	}

	if subComponent != nil {
		wg.Go(func() {
			subComponent.Process()
		})
	}

	<-ctx.Done()
	lo.Info("shutdown signal received")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), defaultGracefulShutdownPeriod)

	wg.Go(func() {
		if apiComponent != nil {
			if err := apiComponent.Stop(shutdownCtx); err != nil {
				lo.Error("failed to stop API server", "err", err)
			}
		}
		if workerComponent != nil {
			gasOracleInst.Stop()
			if err := workerComponent.Stop(shutdownCtx); err != nil {
				lo.Error("failed to stop worker queue", "err", err)
			}
		}
		if subComponent != nil {
			subComponent.Close()
			pubComponent.Close()
		}
	})

	go func() {
		wg.Wait()
		stop()
		cancel()
		os.Exit(0)
	}()

	<-shutdownCtx.Done()
	if errors.Is(shutdownCtx.Err(), context.DeadlineExceeded) {
		lo.Error("graceful shutdown period exceeded, forcing exit")
	}
	stop()
	cancel()
	os.Exit(1)
}

func notifyShutdown() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGINT)
}

func loadStore() store.Store {
	pgStore, err := store.NewPgStore(store.PgOpts{
		Logg:                 lo,
		DSN:                  ko.MustString("postgres.dsn"),
		MigrationsFolderPath: migrationsFolderFlag,
		QueriesFolderPath:    queriesFlag,
	})
	if err != nil {
		lo.Error("could not initialize postgres store", "error", err)
		os.Exit(1)
	}
	if err := pgStore.Bootstrap(); err != nil {
		lo.Error("store bootstrap actions failed", "error", err)
		os.Exit(1)
	}

	return pgStore
}

func loadChainProvider() *ethutils.Provider {
	return ethutils.NewProvider(
		ko.MustString("chain.rpc_endpoint"),
		ko.MustInt64("chain.id"),
		ethutils.WithDivviConsumerAddress(ko.MustString("chain.divvi_consumer")),
	)
}

func loadGasOracle(chainProvider *ethutils.Provider) gas.GasOracle {
	switch ko.MustString("gas.oracle_type") {
	case "static":
		return &gas.StaticGas{}
	case "rpc":
		gasOracle, err := gas.NewRPCGasOracle(gas.RPCGasOracleOpts{
			Logg:          lo,
			ChainProvider: chainProvider,
		})
		if err != nil {
			lo.Error("could not initialize rpc gas oracle", "error", err)
			os.Exit(1)
		}
		return gasOracle
	default:
		lo.Error("unknown gas oracle type", "type", ko.MustString("gas.oracle_type"))
		os.Exit(1)
	}

	return nil
}

func loadWorker(pgStore store.Store, chainProvider *ethutils.Provider, gasOracle gas.GasOracle) *worker.WorkerContainer {
	maxWorkers := ko.Int("workers.max")
	if maxWorkers <= 0 {
		maxWorkers = runtime.NumCPU() * 2
	}

	workerContainer := worker.New(worker.WorkerOpts{
		MaxWorkers:    maxWorkers,
		GasOracle:     gasOracle,
		Store:         pgStore,
		Logg:          lo,
		ChainProvider: chainProvider,
	})
	if err := workerContainer.Bootstrap(); err != nil {
		lo.Error("could not initialize worker container", "error", err)
		os.Exit(1)
	}

	return workerContainer
}

func loadSub(pgStore store.Store) (*sub.Sub, *pub.Pub) {
	endpoints := ko.MustStrings("jetstream.endpoints")
	persistDuration := time.Duration(ko.MustInt("jetstream.persist_duration_hrs")) * time.Hour

	natsConn, js, err := internaljs.NewJetStream(internaljs.JetStreamOpts{
		Endpoints: endpoints,
	})
	if err != nil {
		lo.Error("could not initialize jetstream connection", "error", err)
		os.Exit(1)
	}
	lo.Info("connected to NATS", "endpoints", endpoints)

	pubInst := pub.NewPub(pub.PubOpts{
		PersistDuration: persistDuration,
		JS:              js,
		NatsConn:        natsConn,
	})

	subInst, err := sub.NewSub(sub.SubObts{
		Store:      pgStore,
		JS:         js,
		ConsumerID: ko.MustString("jetstream.id"),
		Pub:        pubInst,
		Logg:       lo,
	})
	if err != nil {
		lo.Error("could not initialize jetstream subscriber", "error", err)
		os.Exit(1)
	}

	return subInst, pubInst
}

func loadAPI(pgStore store.Store, chainProvider *ethutils.Provider, workerContainer *worker.WorkerContainer) *api.API {
	privateKey, publicKey, err := util.LoadSigningKey(ko.MustString("api.private_key"))
	if err != nil {
		lo.Error("could not load signing key", "error", err)
		os.Exit(1)
	}

	opts := api.APIOpts{
		Prod:          ko.Bool("api.prod"),
		Build:         build,
		ListenAddress: ko.MustString("api.address"),
		CORS:          ko.MustStrings("api.cors"),
		SigningKey:    privateKey,
		VerifyingKey:  publicKey,
		Store:         pgStore,
		ChainProvider: chainProvider,
		Logg:          lo,
		ChainID:       ko.MustInt64("chain.id"),
	}
	if workerContainer != nil {
		opts.QueueClient = workerContainer.Client()
	}

	return api.New(opts)
}
