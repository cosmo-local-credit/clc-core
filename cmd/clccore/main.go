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
	"syscall"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/api"
	"github.com/cosmo-local-credit/clc-core/internal/gas"
	"github.com/cosmo-local-credit/clc-core/internal/store"
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
	pgStore := loadStore()
	chainProvider := loadChainProvider()
	gasOracle := loadGasOracle(chainProvider)
	workerContainer := loadNextWorker(pgStore, chainProvider, gasOracle)

	privateKey, publicKey, err := util.LoadSigningKey(ko.MustString("api.private_key"))
	if err != nil {
		lo.Error("could not load private key", "error", err)
		os.Exit(1)
	}

	apiServer := api.New(api.APIOpts{
		Prod:          ko.Bool("api.prod"),
		Build:         build,
		ListenAddress: ko.MustString("api.address"),
		CORS:          ko.MustStrings("api.cors"),
		SigningKey:    privateKey,
		VerifyingKey:  publicKey,
		Store:         pgStore,
		ChainProvider: chainProvider,
		QueueClient:   workerContainer.Client(),
		Logg:          lo,
	})

	lo.Info("starting deployer", "build", build)
	gasOracle.Start()
	defer gasOracle.Stop()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	go func() {
		if err := workerContainer.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
			lo.Error("failed to start worker queue", "err", fmt.Sprintf("%T: %v", err, err))
			os.Exit(1)
		}
	}()

	go func() {
		if err := apiServer.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			lo.Error("failed to start deployer HTTP server", "err", fmt.Sprintf("%T: %v", err, err))
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	lo.Info("shutdown signal received")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), defaultGracefulShutdownPeriod)
	defer cancel()

	if err := apiServer.Stop(shutdownCtx); err != nil {
		lo.Error("failed to stop deployer HTTP server", "err", err)
	}
	if err := workerContainer.Stop(shutdownCtx); err != nil {
		lo.Error("failed to stop worker queue", "err", err)
	}
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

func loadNextWorker(pgStore store.Store, chainProvider *ethutils.Provider, gasOracle gas.GasOracle) *worker.WorkerContainer {
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
