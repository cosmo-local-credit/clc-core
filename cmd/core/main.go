package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/util"
	"github.com/knadh/koanf/v2"
)

const (
	defaultGracefulShutdownPeriod = time.Second * 20
)

var (
	build = "dev"

	confFlag              string
	migrationsFolderFlag  string
	queriesFlag           string
	federationQueriesFlag string

	lo *slog.Logger
	ko *koanf.Koanf
)

func init() {
	flag.StringVar(&confFlag, "config", "config.toml", "Config file location")
	flag.StringVar(&migrationsFolderFlag, "migrations", "migrations/", "Migrations folder location")
	flag.StringVar(&queriesFlag, "queries", "core_queries.sql", "Core queries file location")
	flag.StringVar(&federationQueriesFlag, "federation-queries", "federated_queries.sql", "Federation queries file location")
	flag.Parse()

	lo = util.InitLogger()
	ko = util.InitConfig(lo, confFlag)
}

func main() {
	var wg sync.WaitGroup

	ctx, stop := notifyShutdown()

	var shutdownTelemetry func(context.Context) error
	lo, shutdownTelemetry = loadTelemetry(ctx)
	flushTelemetry := func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTelemetry(flushCtx); err != nil {
			lo.Error("failed to flush telemetry", "err", err)
		}
	}

	pgStore := loadStore()
	federationStore := loadFederationStore()
	chainProvider := loadChainProvider()
	gasOracleInst := loadGasOracle(chainProvider)
	accountIndexAddress := loadRegistry(chainProvider)
	relayerComponent := loadRelayer(pgStore, chainProvider, gasOracleInst)
	workerComponent := loadWorker(pgStore, federationStore, chainProvider, gasOracleInst, relayerComponent, accountIndexAddress)
	pretiumComponent := loadPretium()
	apiComponent := loadAPI(pgStore, federationStore, chainProvider, workerComponent, relayerComponent, pretiumComponent)
	subComponent, pubComponent := loadSub(pgStore, chainProvider, workerComponent)

	lo.Info("starting clc-core", "build", build)

	wg.Go(func() {
		gasOracleInst.Start()
	})

	wg.Go(func() {
		if err := workerComponent.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
			lo.Error("worker queue exited unexpectedly", "err", fmt.Sprintf("%T: %v", err, err))
			os.Exit(1)
		}
	})

	wg.Go(func() {
		if err := apiComponent.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			lo.Error("API server exited unexpectedly", "err", fmt.Sprintf("%T: %v", err, err))
			os.Exit(1)
		}
	})

	wg.Go(func() {
		subComponent.Process()
	})

	<-ctx.Done()
	lo.Info("shutdown signal received")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), defaultGracefulShutdownPeriod)

	wg.Go(func() {
		if err := subComponent.Close(shutdownCtx); err != nil {
			lo.Error("failed to drain jetstream subscriber", "err", err)
		}
		if err := apiComponent.Stop(shutdownCtx); err != nil {
			lo.Error("failed to stop API server", "err", err)
		}
		gasOracleInst.Stop()
		if err := workerComponent.Stop(shutdownCtx); err != nil {
			lo.Error("failed to stop worker queue", "err", err)
		}
		pubComponent.Close()

		if federationStore != nil {
			federationStore.Close()
			lo.Info("federation DB pool closed")
		}
	})

	go func() {
		wg.Wait()
		flushTelemetry()
		stop()
		cancel()
		os.Exit(0)
	}()

	<-shutdownCtx.Done()
	if errors.Is(shutdownCtx.Err(), context.DeadlineExceeded) {
		stop()
		cancel()
		lo.Error("graceful shutdown period exceeded, forcefully shutting down")
		flushTelemetry()
	}
	os.Exit(1)
}
