package main

import (
	"context"
	"io/fs"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/api"
	"github.com/cosmo-local-credit/clc-core/internal/gas"
	internaljs "github.com/cosmo-local-credit/clc-core/internal/jetstream"
	"github.com/cosmo-local-credit/clc-core/internal/pub"
	"github.com/cosmo-local-credit/clc-core/internal/static"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/internal/sub"
	"github.com/cosmo-local-credit/clc-core/internal/util"
	"github.com/cosmo-local-credit/clc-core/internal/worker"
	"github.com/grassrootseconomics/ethutils"
)

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

func loadFederationStore() store.FederationStore {
	fedStore, err := store.NewFederationStore(store.FederationStoreOpts{
		Logg:              lo,
		DSN:               ko.String("federation.dsn"),
		QueriesFolderPath: federationQueriesFlag,
	})
	if err != nil {
		lo.Error("could not initialize federation store", "error", err)
		os.Exit(1)
	}

	return fedStore
}

func loadChainProvider() *ethutils.Provider {
	return ethutils.NewProvider(
		ko.MustString("chain.rpc_endpoint"),
		ko.MustInt64("chain.id"),
		ethutils.WithDivviConsumerAddress(ko.MustString("chain.divvi_consumer")),
	)
}

func loadGasOracle(chainProvider *ethutils.Provider) gas.GasOracle {
	oracleType := ko.MustString("gas.oracle_type")
	if oracleType != "rpc" {
		lo.Error("unsupported gas oracle type", "type", oracleType, "required", "rpc")
		os.Exit(1)
	}

	gasOracle, err := gas.NewRPCGasOracle(gas.RPCGasOracleOpts{
		Logg:          lo,
		ChainProvider: chainProvider,
	})
	if err != nil {
		lo.Error("could not initialize rpc gas oracle", "error", err)
		os.Exit(1)
	}

	return gasOracle
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
		Store:       pgStore,
		JS:          js,
		ConsumerID:  ko.MustString("jetstream.id"),
		SkipBacklog: ko.Bool("jetstream.skip_backlog"),
		Pub:         pubInst,
		Logg:        lo,
	})
	if err != nil {
		lo.Error("could not initialize jetstream subscriber", "error", err)
		os.Exit(1)
	}

	return subInst, pubInst
}

func loadAPI(pgStore store.Store, federationStore store.FederationStore, chainProvider *ethutils.Provider, workerContainer *worker.WorkerContainer) *api.API {
	privateKey, publicKey, err := util.LoadSigningKey(ko.MustString("api.private_key"))
	if err != nil {
		lo.Error("could not load signing key", "error", err)
		os.Exit(1)
	}

	var staticFS fs.FS
	serveStatic := true
	if ko.Exists("api.serve_static") {
		serveStatic = ko.Bool("api.serve_static")
	}

	if serveStatic {
		staticFS = static.WebFS
	}

	return api.New(api.APIOpts{
		Prod:            ko.Bool("api.prod"),
		Build:           build,
		ListenAddress:   ko.MustString("api.address"),
		CORS:            ko.MustStrings("api.cors"),
		SigningKey:      privateKey,
		VerifyingKey:    publicKey,
		Store:           pgStore,
		FederationStore: federationStore,
		ChainProvider:   chainProvider,
		Logg:            lo,
		ChainID:         ko.MustInt64("chain.id"),
		QueueClient:     workerContainer.Client(),
		StaticFS:        staticFS,
	})
}
