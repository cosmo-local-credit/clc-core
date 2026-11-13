package router

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	gocalibur "github.com/grassrootseconomics/go-calibur"
	"github.com/holiman/uint256"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
)

type (
	// Config is the [router] section.
	Config struct {
		ChainID              int64
		MaxHops              int
		MaxRoutesPerPage     int
		ReloadInterval       time.Duration
		StructureInterval    time.Duration
		ParamsInterval       time.Duration
		BalanceInterval      time.Duration
		PairFeeInterval      time.Duration
		DirtyDebounce        time.Duration
		StaleAfterBlocks     uint64
		QuoteDeadline        time.Duration
		MaxSearchEvaluations int
		PlainERC20           []common.Address
		TieMarginPPM         uint64
		HoldingsCacheTTL     time.Duration
		ChainBatchSize       int
	}

	// StateStore is the persistence the router needs. Every write is ordered: it
	// lands only when its position is newer than the row's.
	StateStore interface {
		LoadRouterState(ctx context.Context, chainID int64) (StateRows, error)
		WriteStructure(ctx context.Context, chainID int64, writes []StructureWrite) error
		WriteLiquidity(ctx context.Context, rows []LiquidityWrite) ([]EdgeKey, error)
		WritePoolParams(ctx context.Context, rows []PoolParamsWrite) error
		SetQuoterVerified(ctx context.Context, poolID uint64, verified bool, block uint64) error
	}

	// StructureWrite is one chain-read field of one pool_tokens row, written
	// through the same ordered statements as the events. Membership is keyed
	// by registry, limits by pool, price indexes by quoter, as their events
	// are.
	StructureWrite struct {
		Registry   string
		Pool       string
		Quoter     string
		Token      common.Address
		Allowed    *bool
		Limit      *uint256.Int
		PriceIndex *uint256.Int
		Pos        Position
	}

	// LiquidityWrite is a chain-read balance.
	LiquidityWrite struct {
		Key      EdgeKey
		Balance  uint256.Int
		FeesHeld uint256.Int
		Block    uint64
	}

	// PoolParamsWrite is a parameter refresh for one pool.
	PoolParamsWrite struct {
		PoolID         uint64
		Params         PoolParams
		QuoterVerified bool
		Block          uint64
	}

	// HoldingsSource lists the vouchers an indexer believes a wallet holds. The
	// router verifies every one on chain before using it.
	HoldingsSource interface {
		HeldTokens(ctx context.Context, wallet common.Address) ([]common.Address, error)
	}

	// Relay is what the quote needs from the relayer.
	Relay interface {
		SignerAddress() common.Address
		CaliburAddress() common.Address
		RelayGasBudget(nCalls int, delegated, webAuthn bool) uint64
	}

	// RequestError is a malformed request (400).
	RequestError struct{ Msg string }

	// RouteError is route_unavailable (409).
	RouteError struct {
		Reason Reason
		Hop    int
	}

	// MismatchError is quote_mismatch (409): the chain disagreed with the local
	// arithmetic.
	MismatchError struct {
		Hop   int
		Local string
		Chain string
	}

	// SimulationError is simulation_failed (409).
	SimulationError struct{ Revert DecodedRevert }

	// CursorError is a cursor whose listing has moved (409).
	CursorError struct{}

	// Service owns the graph and the chain loops.
	Service struct {
		cfg      Config
		buildCfg BuildConfig
		store    StateStore
		chain    ChainReader
		holders  HoldingsSource
		relay    Relay
		logg     *slog.Logger
		chainID  *big.Int

		snap    atomic.Pointer[Snapshot]
		version atomic.Uint64
		fees    *FeeBook
		stateMu sync.Mutex
		state   *State // guarded by stateMu

		deltas    chan Delta
		reloadNow chan struct{}
		refresh   chan common.Address
		dirty     *dirtyQueue

		health   *health
		holdings *holdingsCache
		metrics  *metrics
		poolRead singleflight.Group

		cancel context.CancelFunc
		wg     sync.WaitGroup
	}

	// Opts wires a Service.
	Opts struct {
		Config   Config
		Store    StateStore
		Chain    ChainReader
		Holdings HoldingsSource
		Relay    Relay
		Logg     *slog.Logger
	}

	// republishDelta rebuilds the snapshot without touching state, after the fee
	// book changed.
	republishDelta struct{}

	// Freshness says how current a response is.
	Freshness struct {
		AsOfBlock    uint64
		GraphVersion uint64
		HeadBlock    uint64
		Stale        bool
	}

	// ReachView is the estimated reachable set for a wallet.
	ReachView struct {
		Freshness

		Snap   *Snapshot
		Result ReachResult
	}

	// RoutesRequest is GET /users/me/credit/routes.
	RoutesRequest struct {
		Target common.Address
		Depth  int
		From   *common.Address
		Mode   Mode
		Amount *uint256.Int
		Cursor string
	}

	// RoutesView is one page of routes to a voucher.
	RoutesView struct {
		Freshness

		Snap       *Snapshot
		Target     int32
		Groups     []RouteGroup
		Complete   bool
		NextCursor string
		Fallback   []FallbackPool
	}

	// HopRef names one hop of a requested route.
	HopRef struct {
		Pool     common.Address
		TokenIn  common.Address
		TokenOut common.Address
	}

	// QuoteRequest is POST /users/me/credit/quote.
	QuoteRequest struct {
		Hops      []HopRef
		Mode      Mode
		Amount    uint256.Int
		Recipient *common.Address
		KeyHash   common.Hash
	}

	// QuoteView is an authoritative quote and the batch to sign.
	QuoteView struct {
		Block        Head
		ExpiresAt    uint64
		Mode         Mode
		Hops         []HopRef
		Legs         []LegParams
		Quote        RouteQuote
		MinAmountOut uint256.Int
		// Requested is the exact-output quantity; zero for exact input.
		Requested       uint256.Int
		FeeFree         uint256.Int
		EffectiveFeePPM uint256.Int
		Wallet          common.Address
		Recipient       common.Address
		Batch           SignedBatch
		Digest          common.Hash
		Simulation      SimResult
		Delegated       bool
		EOANonce        uint64
	}

	// poolState is every pool-side read for one route at one block. It is the
	// only part of a quote shared between callers.
	poolState struct {
		params    []PoolParams
		structure []PoolStructure
		balances  []BalanceReading
		fees      []PairFeeReading
	}

	// holdingsCache keeps verified balances briefly per wallet.
	holdingsCache struct {
		ttl     time.Duration
		mu      sync.Mutex
		entries map[common.Address]holdingsEntry
	}

	holdingsEntry struct {
		at       time.Time
		balances map[common.Address]uint256.Int
	}
)

func (c *Config) withDefaults() {
	if c.MaxHops <= 0 {
		c.MaxHops = 2
	}
	if c.MaxRoutesPerPage <= 0 {
		c.MaxRoutesPerPage = 50
	}
	if c.ReloadInterval <= 0 {
		c.ReloadInterval = 30 * time.Second
	}
	if c.StructureInterval <= 0 {
		c.StructureInterval = 10 * time.Minute
	}
	if c.ParamsInterval <= 0 {
		c.ParamsInterval = 2 * time.Minute
	}
	if c.BalanceInterval <= 0 {
		c.BalanceInterval = 5 * time.Minute
	}
	if c.PairFeeInterval <= 0 {
		c.PairFeeInterval = 30 * time.Minute
	}
	if c.DirtyDebounce <= 0 {
		c.DirtyDebounce = 250 * time.Millisecond
	}
	if c.StaleAfterBlocks == 0 {
		c.StaleAfterBlocks = 120
	}
	if c.QuoteDeadline <= 0 {
		c.QuoteDeadline = 300 * time.Second
	}
	if c.MaxSearchEvaluations <= 0 {
		c.MaxSearchEvaluations = DefaultSearchBudget
	}
	if c.HoldingsCacheTTL <= 0 {
		c.HoldingsCacheTTL = 15 * time.Second
	}
	if c.ChainBatchSize <= 0 {
		c.ChainBatchSize = 150
	}
}

// Errors the API maps to statuses.
var (
	// ErrUnavailable: the graph or the chain cannot be used; retry (503).
	ErrUnavailable = errors.New("router unavailable")
	// ErrUnknownVoucher: the target or an input is not in the catalog (404).
	ErrUnknownVoucher = errors.New("unknown voucher")
)

func (e *RequestError) Error() string { return e.Msg }

func (e *RouteError) Error() string {
	return fmt.Sprintf("route unavailable: %s (hop %d)", e.Reason, e.Hop)
}

func (e *MismatchError) Error() string {
	return fmt.Sprintf("quote mismatch at hop %d: local %s, chain %s", e.Hop, e.Local, e.Chain)
}

func (e *SimulationError) Error() string { return "simulation failed: " + e.Revert.Reason }

func (CursorError) Error() string { return "the route listing changed; restart from the first page" }

// New builds a service; Load must run before it answers.
func New(o Opts) *Service {
	cfg := o.Config
	cfg.withDefaults()
	plain := make(map[common.Address]struct{}, len(cfg.PlainERC20))
	for _, a := range cfg.PlainERC20 {
		plain[a] = struct{}{}
	}
	logg := o.Logg
	if logg == nil {
		logg = slog.New(slog.DiscardHandler)
	}
	s := &Service{
		cfg:       cfg,
		buildCfg:  BuildConfig{PlainERC20: plain},
		store:     o.Store,
		chain:     o.Chain,
		holders:   o.Holdings,
		relay:     o.Relay,
		logg:      logg,
		chainID:   big.NewInt(cfg.ChainID),
		fees:      NewFeeBook(),
		deltas:    make(chan Delta, 4096),
		reloadNow: make(chan struct{}, 1),
		refresh:   make(chan common.Address, 256),
		dirty:     newDirtyQueue(),
		health:    newHealth(),
		holdings:  newHoldingsCache(cfg.HoldingsCacheTTL),
	}
	s.metrics = newMetrics(s)
	return s
}

// Config returns the effective configuration.
func (s *Service) Config() Config { return s.cfg }

// Snapshot returns the current snapshot, or nil before Load.
func (s *Service) Snapshot() *Snapshot { return s.snap.Load() }

// Load builds the first snapshot from the database alone, so discovery works
// with last-known state whether or not the RPC is up.
func (s *Service) Load(ctx context.Context) error {
	if err := s.reload(ctx); err != nil {
		return fmt.Errorf("router: load state: %w", err)
	}
	return nil
}

// publish builds and stores a snapshot; the caller holds stateMu.
func (s *Service) publish() {
	version := s.version.Add(1)
	snap := s.state.Build(s.buildCfg, version, s.fees)
	s.snap.Store(snap)
}

func (s *Service) reload(ctx context.Context) error {
	rows, err := s.store.LoadRouterState(ctx, s.cfg.ChainID)
	if err != nil {
		return err
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.state = NewState(rows)
	s.publish()
	return nil
}

// ReconcileOnce runs the structure reconciliation, parameter refresh and
// balance sweep now, in that order, then rebuilds the graph from the
// database. It is the boot backfill, run on demand.
func (s *Service) ReconcileOnce(ctx context.Context) error {
	if s.chain == nil {
		return ErrNoChain
	}
	steps := []struct {
		name string
		fn   func(context.Context) (int, uint64, error)
	}{
		{loopStructure, s.reconcileStructure},
		{loopParams, s.refreshParams},
		{loopBalances, s.sweepBalances},
	}
	for _, step := range steps {
		start := time.Now()
		corrections, block, err := step.fn(ctx)
		if err != nil {
			s.health.fail(step.name, err)
			return fmt.Errorf("%s: %w", step.name, err)
		}
		s.health.ok(step.name, time.Since(start), corrections, block)
		// Each step reads what the previous one wrote.
		if err := s.reload(ctx); err != nil {
			return err
		}
	}
	return nil
}

// Run starts the writer and the reconciliation loops. It returns at once.
func (s *Service) Run(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.wg.Go(func() { s.writer(ctx) })
	if s.chain != nil {
		s.wg.Go(func() { s.loop(ctx, loopStructure, s.cfg.StructureInterval, s.reconcileStructure) })
		s.wg.Go(func() { s.loop(ctx, loopParams, s.cfg.ParamsInterval, s.refreshParams) })
		s.wg.Go(func() { s.loop(ctx, loopBalances, s.cfg.BalanceInterval, s.sweepBalances) })
		s.wg.Go(func() { s.loop(ctx, loopPairFees, s.cfg.PairFeeInterval, s.sweepPairFees) })
		s.wg.Go(func() { s.dirtyLoop(ctx) })
		s.wg.Go(func() { s.refreshLoop(ctx) })
	}
}

// Stop ends every goroutine Run started.
func (s *Service) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
}

// Apply hands committed changes to the graph. It never blocks: when the
// queue is full the graph reloads from the database instead, which converges
// on the same state.
func (s *Service) Apply(deltas ...Delta) {
	if s == nil {
		return
	}
	for _, d := range deltas {
		switch d := d.(type) {
		case DirtyDelta:
			s.markDirty(d.Pool, d.Token)
		case TransferDelta:
			snap := s.Snapshot()
			if snap == nil {
				continue
			}
			for _, party := range d.Parties {
				if _, ok := snap.PoolByAddress(party); ok {
					s.markDirty(party, d.Token)
				}
			}
		case RefreshPoolDelta:
			select {
			case s.refresh <- d.Address:
			default:
				s.requestReload()
			}
		default:
			select {
			case s.deltas <- d:
			default:
				s.requestReload()
			}
		}
	}
}

func (s *Service) requestReload() {
	select {
	case s.reloadNow <- struct{}{}:
	default:
	}
}

// writer is the only goroutine that touches s.state.
func (s *Service) writer(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.ReloadInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.doReload(ctx)
		case <-s.reloadNow:
			s.doReload(ctx)
		case d := <-s.deltas:
			s.stateMu.Lock()
			changed, reload := s.applyOne(d)
			// Coalesce whatever else is queued into one publish.
			for drained := false; !drained; {
				select {
				case next := <-s.deltas:
					c, r := s.applyOne(next)
					changed, reload = changed || c, reload || r
				default:
					drained = true
				}
			}
			if changed && !reload {
				s.publish()
			}
			s.stateMu.Unlock()
			if reload {
				s.doReload(ctx)
			}
		}
	}
}

func (s *Service) applyOne(d Delta) (changed, reload bool) {
	if s.state == nil {
		return false, true
	}
	if _, ok := d.(republishDelta); ok {
		return true, false
	}
	return s.state.Apply(d)
}

func (republishDelta) isDelta() {}

func (s *Service) doReload(ctx context.Context) {
	if err := s.reload(ctx); err != nil {
		s.logg.Warn("router: reload from database failed", "error", err)
		s.health.fail(loopReload, err)
		return
	}
	s.health.ok(loopReload, 0, 0, 0)
}

func (s *Service) freshness(snap *Snapshot) Freshness {
	head := s.health.headBlock()
	f := Freshness{AsOfBlock: snap.Block, GraphVersion: snap.Version, HeadBlock: head}
	f.Stale = !s.health.initialized() || s.health.anyFailing() || snap.Block == 0 ||
		(head > snap.Block && head-snap.Block > s.cfg.StaleAfterBlocks)
	return f
}

func (s *Service) current() (*Snapshot, error) {
	snap := s.Snapshot()
	if snap == nil {
		return nil, ErrUnavailable
	}
	return snap, nil
}

// holdingsFor returns the wallet's verified balances of routable vouchers.
// A failed or partial read is an error, never an empty wallet.
func (s *Service) holdingsFor(ctx context.Context, wallet common.Address, snap *Snapshot) ([]Holding, error) {
	if cached, ok := s.holdings.get(wallet); ok {
		return resolveHoldings(snap, cached), nil
	}
	if s.chain == nil {
		return nil, ErrUnavailable
	}
	var candidates []common.Address
	if s.holders != nil {
		tokens, err := s.holders.HeldTokens(ctx, wallet)
		if err != nil {
			return nil, fmt.Errorf("%w: holdings: %w", ErrUnavailable, err)
		}
		for _, t := range tokens {
			if id, ok := snap.TokenByAddress(t); ok && len(snap.legs[id]) > 0 {
				candidates = append(candidates, t)
			}
		}
	} else {
		for id := range snap.Tokens {
			if len(snap.legs[id]) > 0 {
				candidates = append(candidates, snap.Tokens[id].Address)
			}
		}
	}
	balances := map[common.Address]uint256.Int{}
	if len(candidates) > 0 {
		readings, err := s.chain.HolderBalances(ctx, 0, wallet, candidates)
		if err != nil {
			return nil, fmt.Errorf("%w: balances: %w", ErrUnavailable, err)
		}
		for i, r := range readings {
			if r.Err != nil {
				return nil, fmt.Errorf("%w: balanceOf %s: %w", ErrUnavailable, candidates[i].Hex(), r.Err)
			}
			if !r.Value.IsZero() {
				balances[candidates[i]] = r.Value
			}
		}
	}
	s.holdings.put(wallet, balances)
	return resolveHoldings(snap, balances), nil
}

func resolveHoldings(snap *Snapshot, balances map[common.Address]uint256.Int) []Holding {
	out := make([]Holding, 0, len(balances))
	for addr, bal := range balances {
		if id, ok := snap.TokenByAddress(addr); ok {
			out = append(out, Holding{Token: id, Balance: bal})
		}
	}
	slices.SortFunc(out, func(a, b Holding) int { return int(a.Token - b.Token) })
	return out
}

// Reach answers GET /users/me/credit.
func (s *Service) Reach(ctx context.Context, wallet common.Address, depth int) (ReachView, error) {
	if depth < 1 || depth > s.cfg.MaxHops {
		return ReachView{}, &RequestError{Msg: fmt.Sprintf("depth must be between 1 and %d", s.cfg.MaxHops)}
	}
	snap, err := s.current()
	if err != nil {
		return ReachView{}, err
	}
	holdings, err := s.holdingsFor(ctx, wallet, snap)
	if err != nil {
		return ReachView{}, err
	}
	start := time.Now()
	result := Reach(snap, holdings, depth)
	s.metrics.enumeration(ctx, "reach", time.Since(start))
	return ReachView{Freshness: s.freshness(snap), Snap: snap, Result: result}, nil
}

// ReachablePoolIDs is the database ids of the pools a wallet can reach at
// depth, for the discovery filters.
func (s *Service) ReachablePoolIDs(ctx context.Context, wallet common.Address, depth int) ([]uint64, common.Hash, error) {
	view, err := s.Reach(ctx, wallet, depth)
	if err != nil {
		return nil, common.Hash{}, err
	}
	ids := make([]uint64, 0, len(view.Result.Pools))
	for _, p := range view.Result.Pools {
		ids = append(ids, view.Snap.Pools[p.Pool].DBID)
	}
	slices.Sort(ids)
	return ids, view.Result.Digest, nil
}

// Routes answers GET /users/me/credit/routes.
func (s *Service) Routes(ctx context.Context, wallet common.Address, req RoutesRequest) (RoutesView, error) {
	depth := req.Depth
	if depth == 0 {
		depth = s.cfg.MaxHops
	}
	if depth < 1 || depth > s.cfg.MaxHops {
		return RoutesView{}, &RequestError{Msg: fmt.Sprintf("depth must be between 1 and %d", s.cfg.MaxHops)}
	}
	if req.Mode == "" {
		req.Mode = ExactInput
	}
	if req.Mode != ExactInput && req.Mode != ExactOutput {
		return RoutesView{}, &RequestError{Msg: "mode must be exactInput or exactOutput"}
	}
	snap, err := s.current()
	if err != nil {
		return RoutesView{}, err
	}
	target, ok := snap.TokenByAddress(req.Target)
	if !ok {
		return RoutesView{}, ErrUnknownVoucher
	}
	var from *int32
	if req.From != nil {
		id, ok := snap.TokenByAddress(*req.From)
		if !ok {
			return RoutesView{}, ErrUnknownVoucher
		}
		from = &id
	}
	offset := 0
	var wantDigest common.Hash
	if req.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(req.Cursor)
		if err != nil {
			return RoutesView{}, &RequestError{Msg: "invalid cursor"}
		}
		var ok bool
		offset, wantDigest, ok = decodeOffset(raw)
		if !ok {
			return RoutesView{}, &RequestError{Msg: "invalid cursor"}
		}
	}

	holdings, err := s.holdingsFor(ctx, wallet, snap)
	if err != nil {
		return RoutesView{}, err
	}
	start := time.Now()
	result := FindRoutes(snap, holdings, target, depth, from, req.Mode, req.Amount, s.cfg.MaxSearchEvaluations,
		RankConfig{TieMarginPPM: s.cfg.TieMarginPPM})
	s.metrics.enumeration(ctx, "routes", time.Since(start))
	if req.Cursor != "" && result.OrderDigest != wantDigest {
		return RoutesView{}, CursorError{}
	}

	limit := s.cfg.MaxRoutesPerPage
	view := RoutesView{
		Freshness: s.freshness(snap),
		Snap:      snap,
		Target:    target,
		Groups:    result.Page(offset, limit),
		Complete:  offset+limit >= result.Total,
		Fallback:  FallbackPools(snap, target),
	}
	if !view.Complete {
		view.NextCursor = base64.RawURLEncoding.EncodeToString(encodeOffset(offset+limit, result.OrderDigest))
	}
	return view, nil
}

// OutputSurplus is what the route delivers beyond an exact-output request.
func (q QuoteView) OutputSurplus() uint256.Int {
	out := q.Quote.AmountOut()
	if q.Mode != ExactOutput || out.Lt(&q.Requested) {
		return uint256.Int{}
	}
	return *new(uint256.Int).Sub(&out, &q.Requested)
}

func (s *Service) validateHops(snap *Snapshot, hops []HopRef) ([]Leg, error) {
	if len(hops) == 0 || len(hops) > s.cfg.MaxHops {
		return nil, &RequestError{Msg: fmt.Sprintf("route must have between 1 and %d hops", s.cfg.MaxHops)}
	}
	pools := map[common.Address]bool{}
	tokens := map[common.Address]bool{hops[0].TokenIn: true}
	legs := make([]Leg, len(hops))
	for i, h := range hops {
		if i > 0 && hops[i-1].TokenOut != h.TokenIn {
			return nil, &RequestError{Msg: fmt.Sprintf("hop %d must spend what hop %d delivers", i, i-1)}
		}
		if h.TokenIn == h.TokenOut {
			return nil, &RequestError{Msg: fmt.Sprintf("hop %d swaps a voucher for itself", i)}
		}
		if pools[h.Pool] {
			return nil, &RequestError{Msg: "a route may use each pool once"}
		}
		if tokens[h.TokenOut] {
			return nil, &RequestError{Msg: "a route may visit each voucher once"}
		}
		pools[h.Pool], tokens[h.TokenOut] = true, true

		pool, ok := snap.PoolByAddress(h.Pool)
		if !ok {
			return nil, &RouteError{Reason: ReasonRetired, Hop: i}
		}
		in, okIn := snap.TokenByAddress(h.TokenIn)
		out, okOut := snap.TokenByAddress(h.TokenOut)
		if !okIn || !okOut {
			return nil, &RouteError{Reason: ReasonUnsupportedToken, Hop: i}
		}
		if ok, reason := snap.PoolRoutable(pool); !ok {
			return nil, &RouteError{Reason: reason, Hop: i}
		}
		for _, t := range []int32{in, out} {
			if ok, reason := snap.TokenRoutable(t); !ok {
				return nil, &RouteError{Reason: reason, Hop: i}
			}
		}
		leg, ok := snap.LegFor(pool, in, out)
		if !ok {
			return nil, &RouteError{Reason: ReasonNotAllowedIn, Hop: i}
		}
		legs[i] = leg
	}
	return legs, nil
}

func (s *Service) readPoolState(ctx context.Context, block uint64, snap *Snapshot, legs []Leg) (*poolState, error) {
	var key strings.Builder
	fmt.Fprintf(&key, "%d", block)
	for _, l := range legs {
		key.WriteString(snap.Pools[l.Pool].Address)
		key.WriteString(snap.Tokens[l.In].Address.Hex())
		key.WriteString(snap.Tokens[l.Out].Address.Hex())
	}
	v, err, _ := s.poolRead.Do(key.String(), func() (any, error) {
		pools := make([]common.Address, len(legs))
		balances := make([]BalanceProbe, 0, 2*len(legs))
		pairs := make([]PairProbe, len(legs))
		for i, l := range legs {
			pool := snap.Pools[l.Pool].Addr
			in, out := snap.Tokens[l.In].Address, snap.Tokens[l.Out].Address
			pools[i] = pool
			balances = append(balances, BalanceProbe{Pool: pool, Token: in}, BalanceProbe{Pool: pool, Token: out})
			pairs[i] = PairProbe{Pool: pool, In: in, Out: out}
		}
		st := &poolState{}
		g, gctx := errgroup.WithContext(ctx)
		g.Go(func() (err error) { st.params, err = s.chain.PoolParams(gctx, block, pools); return })
		g.Go(func() (err error) { st.balances, err = s.chain.Balances(gctx, block, balances); return })
		g.Go(func() (err error) { st.fees, err = s.chain.PairFees(gctx, block, pairs); return })
		if err := g.Wait(); err != nil {
			return nil, err
		}
		probes := make([]StructureProbe, len(legs))
		for i, l := range legs {
			p := st.params[i]
			if p.Err != nil {
				return nil, fmt.Errorf("pool %s: %w", p.Pool.Hex(), p.Err)
			}
			probes[i] = StructureProbe{
				Pool: p.Pool, Registry: p.Registry, Limiter: p.Limiter, Quoter: p.Quoter,
				Relative:    snap.Pools[l.Pool].Quoter == QuoterRelative,
				Known:       []common.Address{snap.Tokens[l.In].Address, snap.Tokens[l.Out].Address},
				SkipEntries: true,
			}
		}
		structure, err := s.chain.PoolStructure(ctx, block, probes)
		if err != nil {
			return nil, err
		}
		st.structure = structure
		return st, nil
	})
	if err != nil {
		return nil, err
	}
	st, ok := v.(*poolState)
	if !ok {
		return nil, fmt.Errorf("%w: pool state", ErrUnavailable)
	}
	return st, nil
}

func member(ps PoolStructure, token common.Address) (MemberReading, bool) {
	for _, m := range ps.Members {
		if m.Token == token {
			return m, true
		}
	}
	return MemberReading{}, false
}

// pinnedLegs turns pinned reads into arithmetic inputs.
func (s *Service) pinnedLegs(snap *Snapshot, legs []Leg, st *poolState) ([]LegParams, error) {
	out := make([]LegParams, len(legs))
	for i, l := range legs {
		pool := &snap.Pools[l.Pool]
		p := st.params[i]
		if p.Quoter != pool.QuoterAddress {
			// The pool was repointed since its quoter was verified.
			s.Apply(RefreshPoolDelta{Address: pool.Addr})
			return nil, &RouteError{Reason: ReasonUnsupportedQuoter, Hop: i}
		}
		structure := st.structure[i]
		if structure.Err != nil {
			return nil, fmt.Errorf("%w: structure: %w", ErrUnavailable, structure.Err)
		}
		inTok, outTok := snap.Tokens[l.In], snap.Tokens[l.Out]
		inM, ok1 := member(structure, inTok.Address)
		outM, ok2 := member(structure, outTok.Address)
		inB, outB := st.balances[2*i], st.balances[2*i+1]
		fee := st.fees[i]
		if !ok1 || !ok2 || inB.Err != nil || outB.Err != nil || fee.Err != nil {
			return nil, fmt.Errorf("%w: pinned read failed at hop %d", ErrUnavailable, i)
		}
		params := LegParams{
			Quoter:        pool.Quoter,
			FeePPM:        fee.FeePPM,
			ProtocolPPM:   p.ProtocolFeePPM,
			HasRegistry:   p.Registry != (common.Address{}),
			HasLimiter:    p.Limiter != (common.Address{}),
			FeesDecoupled: p.FeesDecoupled,
			InAllowed:     inM.Have,
			InDecimals:    inTok.Decimals,
			InBalance:     inB.Balance,
			OutAllowed:    outM.Have,
			OutDecimals:   outTok.Decimals,
			OutBalance:    outB.Balance,
			OutFeesHeld:   outB.FeesHeld,
		}
		if inM.Limit != nil {
			params.InLimit = *inM.Limit
		}
		if pool.Quoter == QuoterRelative {
			if inM.PriceIndex == nil || outM.PriceIndex == nil {
				return nil, fmt.Errorf("%w: price index unread at hop %d", ErrUnavailable, i)
			}
			params.InIndex, params.OutIndex = *inM.PriceIndex, *outM.PriceIndex
		}
		out[i] = params
	}
	return out, nil
}

// Quote answers POST /users/me/credit/quote. It reads both pools and the
// wallet at one pinned block, reproduces the contract arithmetic, checks it
// against getAmountOut, builds the batch, simulates it, and returns exactly
// what the wallet signs. Nothing is stored.
func (s *Service) Quote(ctx context.Context, wallet common.Address, req QuoteRequest) (view QuoteView, err error) {
	start := time.Now()
	defer func() { s.metrics.quote(ctx, time.Since(start), err) }()

	if s.chain == nil || s.relay == nil {
		return QuoteView{}, ErrUnavailable
	}
	if req.Mode != ExactInput && req.Mode != ExactOutput {
		return QuoteView{}, &RequestError{Msg: "mode must be exactInput or exactOutput"}
	}
	if req.Amount.IsZero() {
		return QuoteView{}, &RequestError{Msg: "amount must be positive"}
	}
	if req.Recipient != nil && *req.Recipient == (common.Address{}) {
		return QuoteView{}, &RequestError{Msg: "recipient must not be the zero address"}
	}
	snap, err := s.current()
	if err != nil {
		return QuoteView{}, err
	}
	legs, err := s.validateHops(snap, req.Hops)
	if err != nil {
		return QuoteView{}, err
	}

	head, err := s.chain.Head(ctx)
	if err != nil {
		return QuoteView{}, fmt.Errorf("%w: head: %w", ErrUnavailable, err)
	}
	s.health.observeHead(head.Number)
	block := head.Number

	var (
		st       *poolState
		walletRd WalletReading
		balance  []AmountReading
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { st, err = s.readPoolState(gctx, block, snap, legs); return })
	g.Go(func() (err error) { walletRd, err = s.chain.Wallet(gctx, block, wallet, req.KeyHash); return })
	g.Go(func() (err error) {
		balance, err = s.chain.HolderBalances(gctx, block, wallet, []common.Address{req.Hops[0].TokenIn})
		return
	})
	if err := g.Wait(); err != nil {
		return QuoteView{}, fmt.Errorf("%w: pinned read: %w", ErrUnavailable, err)
	}
	if balance[0].Err != nil {
		return QuoteView{}, fmt.Errorf("%w: wallet balance: %w", ErrUnavailable, balance[0].Err)
	}

	params, err := s.pinnedLegs(snap, legs, st)
	if err != nil {
		return QuoteView{}, err
	}
	for i, l := range legs {
		s.fees.Put(snap.Pools[l.Pool].Addr, snap.Tokens[l.In].Address, snap.Tokens[l.Out].Address, st.fees[i].FeePPM, block)
	}

	var quote RouteQuote
	if req.Mode == ExactOutput {
		quote, err = QuoteExactOutput(params, &req.Amount, &balance[0].Value, s.cfg.MaxSearchEvaluations)
	} else {
		quote, err = SwapRoute(params, &req.Amount, &balance[0].Value)
	}
	if err != nil {
		var qe *QuoteError
		if errors.As(err, &qe) {
			return QuoteView{}, &RouteError{Reason: qe.Reason, Hop: qe.Hop}
		}
		return QuoteView{}, err
	}

	if err := s.crossCheck(ctx, block, snap, legs, req, quote); err != nil {
		return QuoteView{}, err
	}

	calibur := s.relay.CaliburAddress()
	delegated := gocalibur.IsDelegatedTo(walletRd.Code, calibur)
	if len(walletRd.Code) > 0 && !delegated {
		// A Safe or another contract: out of scope.
		return QuoteView{}, &RouteError{Reason: "unsupported_wallet", Hop: -1}
	}

	recipient := wallet
	if req.Recipient != nil {
		recipient = *req.Recipient
	}
	final := quote.AmountOut()
	if req.Mode == ExactOutput {
		final = req.Amount
	}
	deadline := uint64(time.Now().Add(s.cfg.QuoteDeadline).Unix())
	planned := make([]PlannedHop, len(legs))
	for i, l := range legs {
		minOut := final
		to := recipient
		if i+1 < len(legs) {
			minOut, to = quote.Inputs[i+1], wallet
		}
		planned[i] = PlannedHop{
			Pool:      snap.Pools[l.Pool].Addr,
			TokenIn:   snap.Tokens[l.In].Address,
			TokenOut:  snap.Tokens[l.Out].Address,
			AmountIn:  quote.Inputs[i].ToBig(),
			MinOut:    minOut.ToBig(),
			Recipient: to,
		}
	}
	batch := SignedBatch{
		Calls:           BuildRouteCalls(planned, deadline),
		RevertOnFailure: true,
		Nonce:           CaliburNonce(req.KeyHash, walletRd.CaliburSeq),
		KeyHash:         req.KeyHash,
		Executor:        s.relay.SignerAddress(),
		Deadline:        deadline,
	}
	digest := batch.Digest(s.chainID, wallet, calibur)

	calldata, err := batch.ExecuteCalldata()
	if err != nil {
		return QuoteView{}, err
	}
	var override *common.Address
	if !delegated {
		override = &calibur
	}
	sim, err := s.chain.Simulate(ctx, block, wallet, calldata, override)
	if err != nil {
		return QuoteView{}, fmt.Errorf("%w: simulation: %w", ErrUnavailable, err)
	}
	if !sim.OK {
		revert := DecodeRevert(sim.RevertData)
		s.metrics.simulationFailed(ctx, revert.Reason)
		return QuoteView{}, &SimulationError{Revert: revert}
	}
	budget := s.relay.RelayGasBudget(len(batch.Calls), delegated, req.KeyHash != (common.Hash{}))
	if sim.GasEstimate > budget {
		return QuoteView{}, &RouteError{Reason: "gas", Hop: -1}
	}

	feeFree, _ := FeeFree(params, &quote.Inputs[0])
	out := quote.AmountOut()
	view = QuoteView{
		Block:           head,
		ExpiresAt:       deadline,
		Mode:            req.Mode,
		Hops:            req.Hops,
		Legs:            params,
		Quote:           quote,
		MinAmountOut:    final,
		FeeFree:         feeFree,
		EffectiveFeePPM: EffectiveFeePPM(&feeFree, &out),
		Wallet:          wallet,
		Recipient:       recipient,
		Batch:           batch,
		Digest:          digest,
		Simulation:      sim,
		Delegated:       delegated,
		EOANonce:        walletRd.EOANonce,
	}
	if req.Mode == ExactOutput {
		view.Requested = req.Amount
	}
	return view, nil
}

// crossCheck compares the local arithmetic with the chain at the same block:
// getAmountOut must equal each hop's net, and for exact output the engine's
// input must not exceed getAmountIn. A disagreement marks the pool
// unverified, which drops it from discovery.
func (s *Service) crossCheck(ctx context.Context, block uint64, snap *Snapshot, legs []Leg, req QuoteRequest, quote RouteQuote) error {
	probes := make([]AmountProbe, len(legs))
	for i, l := range legs {
		probes[i] = AmountProbe{
			Pool: snap.Pools[l.Pool].Addr, In: snap.Tokens[l.In].Address, Out: snap.Tokens[l.Out].Address,
			Amount: quote.Inputs[i],
		}
	}
	outs, err := s.chain.AmountsOut(ctx, block, probes)
	if err != nil {
		return fmt.Errorf("%w: getAmountOut: %w", ErrUnavailable, err)
	}
	for i, r := range outs {
		if r.Err != nil {
			return fmt.Errorf("%w: getAmountOut hop %d: %w", ErrUnavailable, i, r.Err)
		}
		if !r.Value.Eq(&quote.Hops[i].Net) {
			s.mismatch(ctx, snap, legs[i], block)
			return &MismatchError{Hop: i, Local: quote.Hops[i].Net.Dec(), Chain: r.Value.Dec()}
		}
	}
	if req.Mode != ExactOutput {
		return nil
	}
	for i := range probes {
		if i+1 < len(legs) {
			probes[i].Amount = quote.Inputs[i+1]
		} else {
			probes[i].Amount = req.Amount
		}
	}
	ins, err := s.chain.AmountsIn(ctx, block, probes)
	if err != nil {
		return fmt.Errorf("%w: getAmountIn: %w", ErrUnavailable, err)
	}
	for i, r := range ins {
		if r.Err != nil {
			return fmt.Errorf("%w: getAmountIn hop %d: %w", ErrUnavailable, i, r.Err)
		}
		if quote.Inputs[i].Gt(&r.Value) {
			s.mismatch(ctx, snap, legs[i], block)
			return &MismatchError{Hop: i, Local: quote.Inputs[i].Dec(), Chain: r.Value.Dec()}
		}
	}
	return nil
}

func (s *Service) mismatch(ctx context.Context, snap *Snapshot, leg Leg, block uint64) {
	pool := snap.Pools[leg.Pool]
	s.metrics.mismatch(ctx)
	s.logg.Error("router: quote mismatch, marking pool unverified", "pool", pool.Address)
	if err := s.store.SetQuoterVerified(context.WithoutCancel(ctx), pool.DBID, false, block); err != nil {
		s.logg.Error("router: could not mark pool unverified", "pool", pool.Address, "error", err)
	}
	s.requestReload()
}

func newHoldingsCache(ttl time.Duration) *holdingsCache {
	return &holdingsCache{ttl: ttl, entries: map[common.Address]holdingsEntry{}}
}

func (c *holdingsCache) get(wallet common.Address) (map[common.Address]uint256.Int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[wallet]
	if !ok || time.Since(e.at) > c.ttl {
		return nil, false
	}
	return e.balances, true
}

func (c *holdingsCache) put(wallet common.Address, balances map[common.Address]uint256.Int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) > 10_000 {
		for k, e := range c.entries {
			if time.Since(e.at) > c.ttl {
				delete(c.entries, k)
			}
		}
		if len(c.entries) > 10_000 {
			c.entries = map[common.Address]holdingsEntry{}
		}
	}
	c.entries[wallet] = holdingsEntry{at: time.Now(), balances: balances}
}

// Forget drops a wallet's cached balances, after it spends.
func (s *Service) Forget(wallet common.Address) {
	s.holdings.mu.Lock()
	defer s.holdings.mu.Unlock()
	delete(s.holdings.entries, wallet)
}
