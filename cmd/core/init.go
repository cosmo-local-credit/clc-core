package main

import (
	"context"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/api"
	"github.com/cosmo-local-credit/clc-core/internal/gas"
	internaljs "github.com/cosmo-local-credit/clc-core/internal/jetstream"
	"github.com/cosmo-local-credit/clc-core/internal/pretium"
	"github.com/cosmo-local-credit/clc-core/internal/pub"
	"github.com/cosmo-local-credit/clc-core/internal/relayer"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/internal/sub"
	"github.com/cosmo-local-credit/clc-core/internal/util"
	"github.com/cosmo-local-credit/clc-core/internal/worker"
	"github.com/ethereum/go-ethereum/common"
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
	dsn := ko.String("federation.dsn")
	if dsn == "" {
		lo.Warn("federation store disabled: federation.dsn is empty")
		return nil
	}

	fedStore, err := store.NewFederationStore(store.FederationStoreOpts{
		Logg:              lo,
		DSN:               dsn,
		QueriesFolderPath: federationQueriesFlag,
	})
	if err != nil {
		lo.Warn("federation store unavailable; legacy auto-approval disabled", "error", err)
		return nil
	}

	// Account balance and transfer endpoints read live from the federation
	// source tables (see federated_queries.sql); there is no view to bootstrap
	// or maintain.
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

// loadRegistry resolves well-known contract addresses from the CICRegistry
// pointer contract and returns the AccountIndex address. If ge_registry is
// not configured or the registry is unreachable, the zero address is
// returned — onchain user registration is silently disabled.
func loadRegistry(chainProvider *ethutils.Provider) common.Address {
	registryAddr := ko.String("chain.ge_registry")
	if registryAddr == "" {
		lo.Warn("chain.ge_registry not configured — onchain user registration disabled")
		return common.Address{}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	registryMap, err := chainProvider.RegistryMap(ctx, common.HexToAddress(registryAddr))
	if err != nil {
		lo.Error("could not load registry map — onchain user registration disabled", "error", err)
		return common.Address{}
	}

	accountIndex, ok := registryMap[ethutils.AccountIndex]
	if !ok || accountIndex == (common.Address{}) {
		lo.Warn("AccountIndex not found in registry — onchain user registration disabled")
		return common.Address{}
	}

	lo.Info("registry loaded", "account_index", accountIndex.Hex())
	return accountIndex
}

func loadWorker(pgStore store.Store, federationStore store.FederationStore, chainProvider *ethutils.Provider, gasOracle gas.GasOracle, relayerInst *relayer.Relayer, accountIndexAddress common.Address) *worker.WorkerContainer {
	maxWorkers := ko.Int("workers.max")
	if maxWorkers <= 0 {
		maxWorkers = runtime.NumCPU() * 2
	}

	workerContainer := worker.New(worker.WorkerOpts{
		MaxWorkers:          maxWorkers,
		GasOracle:           gasOracle,
		Store:               pgStore,
		FederationStore:     federationStore,
		Logg:                lo,
		ChainProvider:       chainProvider,
		Relayer:             relayerInst,
		AccountIndexAddress: accountIndexAddress,
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

func loadRelayer(pgStore store.Store, chainProvider *ethutils.Provider, gasOracle gas.GasOracle) *relayer.Relayer {
	r, err := relayer.New(relayer.Opts{
		Logg:                  lo,
		ChainProvider:         chainProvider,
		GasOracle:             gasOracle,
		Store:                 pgStore,
		ChainID:               ko.MustInt64("chain.id"),
		RPCEndpoint:           ko.MustString("chain.rpc_endpoint"),
		CaliburAddress:        ko.MustString("relayer.calibur_address"),
		PerCallGas:            uint64(ko.Int("relayer.per_call_gas")),
		BaseExecGas:           uint64(ko.Int("relayer.base_exec_gas")),
		DelegationOverheadGas: uint64(ko.Int("relayer.delegation_overhead_gas")),
		MaxRelayGas:           uint64(ko.Int("relayer.max_relay_gas")),
		WebAuthnVerifyGas:     uint64(ko.Int("relayer.webauthn_verify_gas")),
		DelegateGas:           uint64(ko.MustInt("relayer.delegate_gas_limit")),
		RelyingPartyID:        ko.String("relayer.relying_party_id"),
		AllowedOrigins:        ko.Strings("relayer.allowed_origins"),
	})
	if err != nil {
		lo.Error("could not initialize relayer", "error", err)
		os.Exit(1)
	}

	bootstrapCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := r.Bootstrap(bootstrapCtx); err != nil {
		cancel()
		lo.Error("relayer bootstrap failed", "error", err)
		os.Exit(1)
	}
	cancel()

	return r
}

func loadPretium() *pretium.PretiumClient {
	baseURL := ko.String("pretium.base_url")
	if baseURL == "" {
		lo.Warn("pretium integration disabled: pretium.base_url is empty")
		return nil
	}

	return pretium.New(baseURL)
}

func loadAPI(pgStore store.Store, federationStore store.FederationStore, chainProvider *ethutils.Provider, workerContainer *worker.WorkerContainer, relayerInst *relayer.Relayer, pretiumClient *pretium.PretiumClient) *api.API {
	privateKey, publicKey, err := util.LoadSigningKey(ko.MustString("api.private_key"))
	if err != nil {
		lo.Error("could not load signing key", "error", err)
		os.Exit(1)
	}

	return api.New(api.APIOpts{
		Prod:            ko.Bool("api.prod"),
		Build:           build,
		ListenAddress:   ko.MustString("api.address"),
		CORS:            ko.MustStrings("api.cors"),
		TrustProxy:      ko.Bool("api.trust_proxy"),
		SigningKey:      privateKey,
		VerifyingKey:    publicKey,
		Store:           pgStore,
		FederationStore: federationStore,
		ChainProvider:   chainProvider,
		Logg:            lo,
		ChainID:         ko.MustInt64("chain.id"),
		QueueClient:     workerContainer.Client(),
		Relayer:         relayerInst,
		Pretium:         pretiumClient,
	})
}
