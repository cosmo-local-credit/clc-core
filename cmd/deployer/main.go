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
	"syscall"
	"time"

	nextapi "github.com/cosmo-local-credit/clc-core/internal/next-api"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/internal/util"
	"github.com/ethereum/go-ethereum/common"
	"github.com/grassrootseconomics/ethutils"
	"github.com/jackc/pgx/v5"
	"github.com/knadh/koanf/v2"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
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
	registry := loadRegistry(chainProvider)
	queueClient := loadQueueClient(pgStore)

	privateKey, publicKey, err := util.LoadSigningKey(ko.MustString("api.private_key"))
	if err != nil {
		lo.Error("could not load private key", "error", err)
		os.Exit(1)
	}

	apiServer := nextapi.New(nextapi.APIOpts{
		Prod:          ko.Bool("api.prod"),
		Build:         build,
		ListenAddress: ko.MustString("api.address"),
		CORS:          ko.MustStrings("api.cors"),
		Registry:      registry,
		SigningKey:    privateKey,
		VerifyingKey:  publicKey,
		Store:         pgStore,
		ChainProvider: chainProvider,
		QueueClient:   queueClient,
		Logg:          lo,
		BannedTokens:  ko.Strings("chain.banned_tokens"),
	})

	lo.Info("starting deployer", "build", build)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGINT)
	defer stop()

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

func loadRegistry(chainProvider *ethutils.Provider) map[string]common.Address {
	registry, err := chainProvider.RegistryMap(context.Background(), ethutils.HexToAddress(ko.MustString("chain.ge_registry")))
	if err != nil {
		lo.Error("could not fetch on chain registry", "error", err)
		os.Exit(1)
	}

	return registry
}

func loadQueueClient(pgStore store.Store) *river.Client[pgx.Tx] {
	queueClient, err := river.NewClient(riverpgxv5.New(pgStore.Pool()), &river.Config{
		Logger: lo,
	})
	if err != nil {
		lo.Error("could not initialize river queue client", "error", err)
		os.Exit(1)
	}

	return queueClient
}
