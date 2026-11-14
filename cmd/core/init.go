package main

import (
	"context"
	"log/slog"
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
	"github.com/cosmo-local-credit/clc-core/internal/router"
	"github.com/cosmo-local-credit/clc-core/internal/safe"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/internal/sub"
	"github.com/cosmo-local-credit/clc-core/internal/telemetry"
	"github.com/cosmo-local-credit/clc-core/internal/util"
	"github.com/cosmo-local-credit/clc-core/internal/worker"
	"github.com/ethereum/go-ethereum/common"
	"github.com/grassrootseconomics/ethutils"
	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go/jetstream"
)

func notifyShutdown() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGINT)
}

// loadTelemetry fans the logger out to the OTLP collector when
// OTEL_EXPORTER_OTLP_ENDPOINT is set. Telemetry is best-effort: a setup failure
// is logged and the service keeps its stderr-only logger.
func loadTelemetry(ctx context.Context) (*slog.Logger, func(context.Context) error) {
	handler, shutdown, err := telemetry.Setup(ctx, lo.Handler(), telemetry.Opts{
		ServiceName: "clc-core",
		Version:     build,
	})
	if err != nil {
		lo.Error("could not initialize telemetry; continuing without it", "error", err)
		return lo, func(context.Context) error { return nil }
	}
	if telemetry.Enabled() {
		lo.Info("telemetry export enabled")
	}

	return slog.New(handler), shutdown
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
	if err := telemetry.ObservePool("core", pgStore.Pool()); err != nil {
		lo.Warn("could not register db pool metrics", "error", err)
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
		MinFeeCapWei:  ko.Int64("chain.min_fee_cap_wei"),
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

func loadWorker(pgStore store.Store, federationStore store.FederationStore, chainProvider *ethutils.Provider, gasOracle gas.GasOracle, relayerInst *relayer.Relayer, accountIndexAddress common.Address, pubInst *pub.Pub) *worker.WorkerContainer {
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
		ChainID:             ko.MustInt64("chain.id"),
		SafeExecutor:        loadSafeExecutor(pgStore, chainProvider),
		MessagingPublisher:  pubInst,
	})
	if err := workerContainer.Bootstrap(); err != nil {
		lo.Error("could not initialize worker container", "error", err)
		os.Exit(1)
	}

	return workerContainer
}

// loadPub connects to NATS and ensures the streams clc-core publishes to. It
// runs before the worker, whose messaging outbox job publishes through it.
func loadPub() (*pub.Pub, jetstream.JetStream) {
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

	return pub.NewPub(pub.PubOpts{
		PersistDuration: persistDuration,
		JS:              js,
		NatsConn:        natsConn,
	}), js
}

func loadSub(pgStore store.Store, chainProvider *ethutils.Provider, workers *worker.WorkerContainer, pubInst *pub.Pub, js jetstream.JetStream, routerInst *router.Service) *sub.Sub {
	var routerSink sub.RouterSink
	if routerInst != nil {
		routerSink = routerInst
	}
	subInst, err := sub.NewSub(sub.SubObts{
		Router:      routerSink,
		ChainID:     ko.MustInt64("chain.id"),
		Store:       pgStore,
		JS:          js,
		ConsumerID:  ko.MustString("jetstream.id"),
		SkipBacklog: ko.Bool("jetstream.skip_backlog"),
		Pub:         pubInst,
		Logg:        lo,
		SafeReader:  safe.NewReader(chainProvider),
		RegisterSafe: func(ctx context.Context, tx pgx.Tx, address string) error {
			return worker.EnqueueSafeRegistration(ctx, workers.Client(), tx, address)
		},
		RefreshSafe: func(ctx context.Context, tx pgx.Tx, address string, minBlock uint64, register bool) error {
			return worker.EnqueueSafeSignersRefresh(ctx, workers.Client(), tx, address, minBlock, register)
		},
	})
	if err != nil {
		lo.Error("could not initialize jetstream subscriber", "error", err)
		os.Exit(1)
	}

	return subInst
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

	// Pre-038 credential rows are unusable until this confirms them on chain.
	// A failure here only delays those holders' passkey sign-in to the next
	// boot, so it is logged rather than fatal.
	legacyCtx, cancelLegacy := context.WithTimeout(context.Background(), 60*time.Second)
	if err := r.VerifyLegacyWebAuthnCredentials(legacyCtx); err != nil {
		lo.Error("legacy webauthn credential check failed", "error", err)
	}
	cancelLegacy()

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

func loadAPI(pgStore store.Store, federationStore store.FederationStore, chainProvider *ethutils.Provider, workerContainer *worker.WorkerContainer, relayerInst *relayer.Relayer, pretiumClient *pretium.PretiumClient, routerInst *router.Service) *api.API {
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
		SafeMultiSend:   ko.Strings("safe.multisend_call_only"),
		SafeSTSURL:      ko.String("safe.sts_base_url"),
		SafeSTSKey:      ko.String("safe.sts_api_key"),
		SafeAutoExecute: safeAutoExecute(),
		Router:          routerInst,
		RouteDeadlineHorizon: time.Duration(
			ko.Int("router.relay_deadline_horizon_secs")) * time.Second,
	})
}

type (
	// federationHoldings lists the vouchers the indexer believes a wallet holds.
	// The router verifies each on chain before using it.
	federationHoldings struct{ fed store.FederationStore }
)

func (h federationHoldings) HeldTokens(ctx context.Context, wallet common.Address) ([]common.Address, error) {
	rows, err := h.fed.ListUserTokenHoldings(ctx, wallet.Hex(), 500)
	if err != nil {
		return nil, err
	}
	out := make([]common.Address, 0, len(rows))
	for _, r := range rows {
		if common.IsHexAddress(r.TokenAddress) {
			out = append(out, common.HexToAddress(r.TokenAddress))
		}
	}
	return out, nil
}

func seconds(key string) time.Duration {
	return time.Duration(ko.Int(key)) * time.Second
}

// loadRouter builds the credit router and its first graph from the database
// alone, so discovery works with last-known state whether or not the RPC is
// up. The chain loops start with the rest of the service.
func loadRouter(pgStore store.Store, federationStore store.FederationStore, chainProvider *ethutils.Provider, relayerInst *relayer.Relayer) *router.Service {
	if !ko.Bool("router.enabled") {
		lo.Warn("credit router disabled: router.enabled is false")
		return nil
	}
	maxHops := ko.Int("router.max_hops")
	if maxHops < 0 || maxHops > 5 {
		lo.Error("router.max_hops must be between 1 and 5 (a relayed batch holds 16 calls, three per hop)", "max_hops", maxHops)
		os.Exit(1)
	}
	var plain []common.Address
	for _, address := range ko.Strings("router.plain_erc20") {
		if !common.IsHexAddress(address) {
			lo.Error("router.plain_erc20 holds an invalid address", "address", address)
			os.Exit(1)
		}
		plain = append(plain, common.HexToAddress(address))
	}
	var holdings router.HoldingsSource
	if federationStore != nil {
		holdings = federationHoldings{fed: federationStore}
	}
	svc := router.New(router.Opts{
		Config: router.Config{
			ChainID:              ko.MustInt64("chain.id"),
			MaxHops:              maxHops,
			MaxRoutesPerPage:     ko.Int("router.max_routes_per_page"),
			ReloadInterval:       seconds("router.reload_interval_secs"),
			StructureInterval:    seconds("router.structure_reconcile_interval_secs"),
			ParamsInterval:       seconds("router.params_refresh_interval_secs"),
			BalanceInterval:      seconds("router.balance_sweep_interval_secs"),
			PairFeeInterval:      seconds("router.pair_fee_sweep_interval_secs"),
			DirtyDebounce:        time.Duration(ko.Int("router.dirty_debounce_ms")) * time.Millisecond,
			StaleAfterBlocks:     uint64(ko.Int64("router.stale_after_blocks")),
			QuoteDeadline:        seconds("router.quote_deadline_secs"),
			MaxSearchEvaluations: ko.Int("router.max_search_evaluations"),
			PlainERC20:           plain,
			TieMarginPPM:         uint64(ko.Int64("router.tie_margin_ppm")),
			HoldingsCacheTTL:     seconds("router.holdings_cache_secs"),
			ChainBatchSize:       ko.Int("router.chain_batch_size"),
		},
		Store:    pgStore,
		Chain:    router.NewW3Reader(chainProvider.Client, ko.Int("router.chain_batch_size")),
		Holdings: holdings,
		Relay:    relayerInst,
		Logg:     lo,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	err := svc.Load(ctx)
	cancel()
	if err != nil {
		lo.Error("could not load the router graph", "error", err)
		os.Exit(1)
	}
	if snap := svc.Snapshot(); snap != nil {
		lo.Info("router graph loaded", "pools", len(snap.Pools), "vouchers", len(snap.Tokens),
			"edges", len(snap.Edges), "legs", snap.LegCount())
	}
	return svc
}

// safeAutoExecute needs the Transaction Service and pinned MultiSend as well as
// the switch: the API queues executions only when its /safe proxy is mounted,
// and the worker must then run the executor that drains them.
func safeAutoExecute() bool {
	return ko.Bool("safe.auto_execute") && ko.String("safe.sts_base_url") != "" &&
		len(ko.Strings("safe.multisend_call_only")) > 0
}

func loadSafeExecutor(pgStore store.Store, chainProvider *ethutils.Provider) *safe.Executor {
	if !safeAutoExecute() {
		return nil
	}
	chainID := ko.MustInt64("chain.id")
	multiSend := make([]common.Address, 0, len(ko.Strings("safe.multisend_call_only")))
	for _, address := range ko.Strings("safe.multisend_call_only") {
		multiSend = append(multiSend, common.HexToAddress(address))
	}
	policy := safe.NewPolicy(worker.NewSafeResolver(pgStore, chainID), multiSend, safe.DefaultAllowedSignatures)
	sts := safe.NewSTS(ko.String("safe.sts_base_url"), ko.String("safe.sts_api_key"))
	return safe.NewExecutor(chainID, policy, sts, safe.NewReader(chainProvider))
}
