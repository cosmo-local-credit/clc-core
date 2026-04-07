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
	"sync"
	"syscall"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/util"
	"github.com/knadh/koanf/v2"
)

const defaultGracefulShutdownPeriod = 20 * time.Second

var (
	build = "dev"

	confFlag             string
	migrationsFolderFlag string
	queriesFlag          string

	lo *slog.Logger
	ko *koanf.Koanf
)

func init() {
	flag.StringVar(&confFlag, "config", "sarafu-network-config.toml", "Config file location")
	flag.StringVar(&migrationsFolderFlag, "migrations", "migrations/", "Migrations folder location")
	flag.StringVar(&queriesFlag, "queries", "queries.sql", "Queries file location")
	flag.Parse()

	lo = util.InitLogger()
	ko = util.InitConfig(lo, confFlag)
}

func main() {
	var wg sync.WaitGroup

	workerComponent := initWorker()
	subComponent := initSub()
	server := initServer()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	lo.Info("starting sn-gas-approver", "build", build)

	wg.Add(1)
	go func() {
		defer wg.Done()
		loadGasOracle().Start()
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := workerComponent.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
			lo.Error("failed to start worker queue", "err", fmt.Sprintf("%T: %v", err, err))
			os.Exit(1)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		subComponent.Process()
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := server.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			lo.Error("failed to start sn-gas-approver HTTP server", "err", fmt.Sprintf("%T: %v", err, err))
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	lo.Info("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), defaultGracefulShutdownPeriod)

	wg.Add(1)
	go func() {
		defer wg.Done()
		subComponent.Close()
		loadGasOracle().Stop()
		if err := server.Stop(shutdownCtx); err != nil {
			lo.Error("failed to stop sn-gas-approver HTTP server", "err", err)
		}
		if err := workerComponent.Stop(shutdownCtx); err != nil {
			lo.Error("failed to stop worker queue", "err", err)
		}
	}()

	go func() {
		wg.Wait()
		stop()
		cancel()
		os.Exit(0)
	}()

	<-shutdownCtx.Done()
	if errors.Is(shutdownCtx.Err(), context.DeadlineExceeded) {
		stop()
		cancel()
		lo.Error("graceful shutdown period exceeded, forcefully shutting down")
	}
	os.Exit(1)
}
