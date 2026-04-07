package main

import (
	"context"
	"os"
	"runtime"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/gas"
	internaljs "github.com/cosmo-local-credit/clc-core/internal/jetstream"
	"github.com/cosmo-local-credit/clc-core/internal/pub"
	"github.com/cosmo-local-credit/clc-core/internal/sngasapprover"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/internal/sub"
	"github.com/cosmo-local-credit/clc-core/internal/worker"
	"github.com/ethereum/go-ethereum/common"
	"github.com/grassrootseconomics/ethutils"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

var (
	pgStore         store.Store
	gasOracle       gas.GasOracle
	chainProvider   *ethutils.Provider
	jsPub           *pub.Pub
	jsSub           *sub.Sub
	registry        map[string]common.Address
	workerContainer *worker.WorkerContainer
	apiServer       *sngasapprover.Server

	js       jetstream.JetStream
	natsConn *nats.Conn
)

func loadJetStream() jetstream.JetStream {
	if js != nil {
		return js
	}

	var err error
	natsConn, js, err = internaljs.NewJetStream(internaljs.JetStreamOpts{
		Endpoints: ko.Strings("jetstream.endpoints"),
	})
	if err != nil {
		lo.Error("could not initialize jetstream", "error", err)
		os.Exit(1)
	}

	return js
}

func loadStore() store.Store {
	if pgStore != nil {
		return pgStore
	}

	var err error
	pgStore, err = store.NewPgStore(store.PgOpts{
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
	if chainProvider != nil {
		return chainProvider
	}

	chainProvider = ethutils.NewProvider(
		ko.MustString("chain.rpc_endpoint"),
		ko.MustInt64("chain.id"),
		ethutils.WithDivviConsumerAddress(ko.MustString("chain.divvi_consumer")),
	)

	return chainProvider
}

func loadRegistry() map[string]common.Address {
	if registry != nil {
		return registry
	}

	var err error
	registry, err = loadChainProvider().RegistryMap(context.Background(), ethutils.HexToAddress(ko.MustString("chain.ge_registry")))
	if err != nil {
		lo.Error("could not fetch on chain registry", "error", err)
		os.Exit(1)
	}

	return registry
}

func loadGasOracle() gas.GasOracle {
	if gasOracle != nil {
		return gasOracle
	}

	switch ko.MustString("gas.oracle_type") {
	case "static":
		gasOracle = &gas.StaticGas{}
	case "rpc":
		rpcOracle, err := gas.NewRPCGasOracle(gas.RPCGasOracleOpts{
			Logg:          lo,
			ChainProvider: loadChainProvider(),
		})
		if err != nil {
			lo.Error("could not initialize rpc gas oracle", "error", err)
			os.Exit(1)
		}
		gasOracle = rpcOracle
	default:
		lo.Error("unknown gas oracle type", "type", ko.MustString("gas.oracle_type"))
		os.Exit(1)
	}

	return gasOracle
}

func loadPub() *pub.Pub {
	if jsPub != nil {
		return jsPub
	}

	jsPub = pub.NewPub(pub.PubOpts{
		PersistDuration: time.Duration(ko.MustInt("jetstream.persist_duration_hrs")) * time.Hour,
		JS:              loadJetStream(),
		NatsConn:        natsConn,
	})

	return jsPub
}

func initWorker() *worker.WorkerContainer {
	if workerContainer != nil {
		return workerContainer
	}

	maxWorkers := ko.Int("workers.max")
	if maxWorkers <= 0 {
		maxWorkers = runtime.NumCPU() * 2
	}

	container, err := worker.New(worker.WorkerOpts{
		Registry:      loadRegistry(),
		MaxWorkers:    maxWorkers,
		GasOracle:     loadGasOracle(),
		Store:         loadStore(),
		Logg:          lo,
		Pub:           loadPub(),
		ChainProvider: loadChainProvider(),
	})
	if err != nil {
		lo.Error("could not initialize worker container", "error", err)
		os.Exit(1)
	}

	workerContainer = container
	return workerContainer
}

func initSub() *sub.Sub {
	if jsSub != nil {
		return jsSub
	}

	instance, err := sub.NewSub(sub.SubObts{
		Store:       loadStore(),
		JS:          loadJetStream(),
		ConsumerID:  ko.MustString("jetstream.id"),
		Pub:         loadPub(),
		Provider:    loadChainProvider(),
		Logg:        lo,
		QueueClient: initWorker().Client(),
		UsersIndex:  loadRegistry()[ethutils.AccountIndex],
	})
	if err != nil {
		lo.Error("could not load jetstream sub", "error", err)
		os.Exit(1)
	}

	jsSub = instance
	return jsSub
}

func initServer() *sngasapprover.Server {
	if apiServer != nil {
		return apiServer
	}

	apiServer = sngasapprover.New(sngasapprover.ServerOpts{
		ListenAddress: ko.MustString("sn_gas_approver.address"),
		Store:         loadStore(),
		Worker:        initWorker(),
		Logg:          lo,
	})

	return apiServer
}
