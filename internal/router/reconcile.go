package router

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

// Loop names, used in health and metrics.
const (
	loopReload    = "reload"
	loopStructure = "structure"
	loopParams    = "params"
	loopBalances  = "balances"
	loopPairFees  = "pair_fees"
	loopDirty     = "dirty"
)

type (
	// LoopHealth is one loop's last outcome.
	LoopHealth struct {
		Runs        int       `json:"runs"`
		LastRun     time.Time `json:"lastRun"`
		LastOK      time.Time `json:"lastOk"`
		LastError   string    `json:"lastError,omitempty"`
		Duration    string    `json:"duration"`
		Corrections int       `json:"corrections"`
		Block       uint64    `json:"block"`
		Failing     bool      `json:"failing"`
	}

	health struct {
		mu    sync.Mutex
		loops map[string]*LoopHealth
		head  uint64
	}

	edgeRef struct{ pool, token int32 }

	// dirtyQueue collects pool balances that events say have changed.
	dirtyQueue struct {
		mu     sync.Mutex
		set    map[[2]common.Address]struct{}
		signal chan struct{}
	}

	// Stats is what /admin/router/stats reports.
	Stats struct {
		GraphVersion uint64                `json:"graphVersion"`
		AsOfBlock    uint64                `json:"asOfBlock"`
		HeadBlock    uint64                `json:"headBlock"`
		Stale        bool                  `json:"stale"`
		Initialized  bool                  `json:"initialized"`
		BuiltAt      time.Time             `json:"builtAt"`
		Pools        int                   `json:"pools"`
		ActivePools  int                   `json:"activePools"`
		Verified     int                   `json:"verifiedPools"`
		Vouchers     int                   `json:"vouchers"`
		Edges        int                   `json:"edges"`
		Legs         int                   `json:"legs"`
		PairFees     int                   `json:"pairFees"`
		DirtyQueue   int                   `json:"dirtyQueue"`
		Loops        map[string]LoopHealth `json:"loops"`
	}
)

func newHealth() *health { return &health{loops: map[string]*LoopHealth{}} }

func (h *health) entry(name string) *LoopHealth {
	e, ok := h.loops[name]
	if !ok {
		e = &LoopHealth{}
		h.loops[name] = e
	}
	return e
}

func (h *health) ok(name string, d time.Duration, corrections int, block uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.entry(name)
	now := time.Now()
	e.Runs++
	e.LastRun, e.LastOK = now, now
	e.LastError, e.Failing = "", false
	e.Duration = d.String()
	e.Corrections = corrections
	if block > 0 {
		e.Block = block
	}
}

func (h *health) fail(name string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.entry(name)
	e.Runs++
	e.LastRun = time.Now()
	e.LastError, e.Failing = err.Error(), true
}

func (h *health) observeHead(block uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if block > h.head {
		h.head = block
	}
}

func (h *health) headBlock() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.head
}

// initialized: one structure reconciliation, one parameter refresh and one
// balance sweep have completed.
func (h *health) initialized() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, name := range []string{loopStructure, loopParams, loopBalances} {
		if e, ok := h.loops[name]; !ok || e.LastOK.IsZero() {
			return false
		}
	}
	return true
}

func (h *health) anyFailing() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for name, e := range h.loops {
		if name != loopPairFees && e.Failing {
			return true
		}
	}
	return false
}

func (h *health) snapshot() map[string]LoopHealth {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]LoopHealth, len(h.loops))
	for k, v := range h.loops {
		out[k] = *v
	}
	return out
}

// loop runs fn immediately and then every interval. The first runs are the
// boot backfill.
func (s *Service) loop(ctx context.Context, name string, interval time.Duration, fn func(context.Context) (int, uint64, error)) {
	run := func() {
		start := time.Now()
		corrections, block, err := fn(ctx)
		d := time.Since(start)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.logg.Warn("router: reconciliation failed", "loop", name, "error", err)
			s.health.fail(name, err)
			s.metrics.loop(ctx, name, d, 0, false)
			return
		}
		s.health.ok(name, d, corrections, block)
		s.metrics.loop(ctx, name, d, corrections, true)
		if corrections > 0 {
			s.logg.Info("router: reconciliation corrected state the events missed",
				"loop", name, "corrections", corrections, "block", block)
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func (s *Service) head(ctx context.Context) (uint64, error) {
	head, err := s.chain.Head(ctx)
	if err != nil {
		return 0, fmt.Errorf("head: %w", err)
	}
	s.health.observeHead(head.Number)
	return head.Number, nil
}

func hexAddr(a string) common.Address { return common.HexToAddress(a) }

// RunStructure runs one structure reconciliation now. Exposed for tests and
// operators.
func (s *Service) RunStructure(ctx context.Context) (int, error) {
	n, _, err := s.reconcileStructure(ctx)
	return n, err
}

// RunPairFeeSweep reads every eligible leg's exact pair fee now. Exposed for
// tests and operators.
func (s *Service) RunPairFeeSweep(ctx context.Context) (int, error) {
	n, _, err := s.sweepPairFees(ctx)
	return n, err
}

// reconcileStructure enumerates every active pool's registry and reads
// membership, limit and price index for each member and each known edge. It
// finds members the database never heard of, and corrects any value an event
// failed to deliver.
func (s *Service) reconcileStructure(ctx context.Context) (int, uint64, error) {
	rows, err := s.store.LoadRouterState(ctx, s.cfg.ChainID)
	if err != nil {
		return 0, 0, err
	}
	block, err := s.head(ctx)
	if err != nil {
		return 0, 0, err
	}

	tokensByAddr := map[common.Address]*TokenRow{}
	for i := range rows.Tokens {
		tokensByAddr[hexAddr(rows.Tokens[i].Address)] = &rows.Tokens[i]
	}
	tokensByID := map[uint64]*TokenRow{}
	for i := range rows.Tokens {
		tokensByID[rows.Tokens[i].ID] = &rows.Tokens[i]
	}
	edges := map[EdgeKey]*EdgeRow{}
	byPool := map[uint64][]common.Address{}
	for i := range rows.Edges {
		e := &rows.Edges[i]
		edges[EdgeKey{e.PoolID, e.VoucherID}] = e
		if t, ok := tokensByID[e.VoucherID]; ok {
			byPool[e.PoolID] = append(byPool[e.PoolID], hexAddr(t.Address))
		}
	}

	var pools []PoolRow
	probes := make([]StructureProbe, 0, len(rows.Pools))
	for _, p := range rows.Pools {
		if !p.Active {
			continue
		}
		pools = append(pools, p)
		probes = append(probes, StructureProbe{
			Pool:     hexAddr(p.Address),
			Registry: hexAddr(p.RegistryAddress),
			Limiter:  hexAddr(p.LimiterAddress),
			Quoter:   hexAddr(p.QuoterAddress),
			Relative: parseQuoterKind(p.QuoterType, p.QuoterAddress) == QuoterRelative,
			Known:    byPool[p.ID],
		})
	}
	readings, err := s.chain.PoolStructure(ctx, block, probes)
	if err != nil {
		return 0, 0, err
	}

	pos := ReconcilePosition(block)
	var (
		writes      []StructureWrite
		corrections int
		failures    []string
		unknown     int
	)
	for i, reading := range readings {
		pool := pools[i]
		if reading.Err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", pool.Address, reading.Err))
			continue
		}
		for _, m := range reading.Members {
			token, ok := tokensByAddr[m.Token]
			if !ok {
				if m.Have {
					unknown++ // a member with no catalog voucher cannot be routed
				}
				continue
			}
			edge := edges[EdgeKey{pool.ID, token.ID}]
			w := StructureWrite{
				Registry: pool.RegistryAddress, Pool: pool.Address, Quoter: pool.QuoterAddress,
				Token: m.Token, Pos: pos,
			}
			changed := false
			if present(pool.RegistryAddress) && (edge == nil && m.Have || edge != nil && edge.Allowed != m.Have) {
				have := m.Have
				w.Allowed, changed = &have, true
			}
			if m.Limit != nil && (edge != nil || m.Have) && !sameNumeric(edgeLimit(edge), m.Limit) {
				w.Limit, changed = m.Limit, true
			}
			if m.PriceIndex != nil && (edge != nil || m.Have) && !sameNumeric(edgePrice(edge), m.PriceIndex) {
				w.PriceIndex, changed = m.PriceIndex, true
			}
			if changed {
				writes = append(writes, w)
				corrections++
			}
		}
	}
	s.metrics.unknownMembers(ctx, unknown)
	if len(writes) > 0 {
		if err := s.store.WriteStructure(ctx, s.cfg.ChainID, writes); err != nil {
			return 0, 0, err
		}
		s.requestReload()
	}
	if len(failures) > 0 {
		return corrections, block, fmt.Errorf("%d pools unread: %s", len(failures), strings.Join(failures[:min(3, len(failures))], "; "))
	}
	return corrections, block, nil
}

func edgeLimit(e *EdgeRow) *string {
	if e == nil {
		return nil
	}
	return e.Limit
}

func edgePrice(e *EdgeRow) *string {
	if e == nil {
		return nil
	}
	return e.PriceIndex
}

func sameNumeric(stored *string, v *uint256.Int) bool {
	if stored == nil {
		return false
	}
	parsed, err := uint256.FromDecimal(*stored)
	return err == nil && parsed.Eq(v)
}

// refreshParams reads every pool's pointers, fees and seal state, and checks
// each relative quoter against the chain with one differential probe.
func (s *Service) refreshParams(ctx context.Context) (int, uint64, error) {
	rows, err := s.store.LoadRouterState(ctx, s.cfg.ChainID)
	if err != nil {
		return 0, 0, err
	}
	var pools []PoolRow
	for _, p := range rows.Pools {
		if p.Active {
			pools = append(pools, p)
		}
	}
	return s.refreshPools(ctx, rows, pools)
}

func (s *Service) refreshPools(ctx context.Context, rows StateRows, pools []PoolRow) (int, uint64, error) {
	if len(pools) == 0 {
		return 0, 0, nil
	}
	block, err := s.head(ctx)
	if err != nil {
		return 0, 0, err
	}
	addrs := make([]common.Address, len(pools))
	for i, p := range pools {
		addrs[i] = hexAddr(p.Address)
	}
	params, err := s.chain.PoolParams(ctx, block, addrs)
	if err != nil {
		return 0, 0, err
	}

	tokensByID := map[uint64]*TokenRow{}
	for i := range rows.Tokens {
		tokensByID[rows.Tokens[i].ID] = &rows.Tokens[i]
	}
	members := map[uint64][]*TokenRow{}
	for _, e := range rows.Edges {
		if t, ok := tokensByID[e.VoucherID]; ok && e.Allowed && t.Active && t.Decimals != nil {
			members[e.PoolID] = append(members[e.PoolID], t)
		}
	}

	// One differential probe per pool: a pair of members, priced locally
	// from indexes read at the same block, and by the pool's getQuote.
	type probe struct {
		index    int
		in, out  *TokenRow
		value    uint256.Int
		relative bool
	}
	var (
		probes     []probe
		structures []StructureProbe
	)
	for i, p := range pools {
		if params[i].Err != nil {
			continue
		}
		kind := parseQuoterKind(p.QuoterType, params[i].Quoter.Hex())
		if params[i].Quoter == (common.Address{}) {
			kind = QuoterNone
		}
		if kind == QuoterUnsupported {
			continue
		}
		ms := members[p.ID]
		if len(ms) < 2 {
			continue
		}
		in, out := ms[0], ms[1]
		unit, ok := pow10(uint8(*in.Decimals))
		if !ok {
			continue
		}
		probes = append(probes, probe{index: i, in: in, out: out, value: *unit, relative: kind == QuoterRelative})
		structures = append(structures, StructureProbe{
			Pool: addrs[i], Quoter: params[i].Quoter, Relative: kind == QuoterRelative,
			Known: []common.Address{hexAddr(in.Address), hexAddr(out.Address)}, SkipEntries: true,
		})
	}
	verified := make([]bool, len(pools))
	if len(probes) > 0 {
		structure, err := s.chain.PoolStructure(ctx, block, structures)
		if err != nil {
			return 0, 0, err
		}
		amounts := make([]AmountProbe, len(probes))
		for k, pr := range probes {
			amounts[k] = AmountProbe{Pool: addrs[pr.index], In: hexAddr(pr.in.Address), Out: hexAddr(pr.out.Address), Amount: pr.value}
		}
		quotes, err := s.chain.Quotes(ctx, block, amounts)
		if err != nil {
			return 0, 0, err
		}
		for k, pr := range probes {
			if quotes[k].Err != nil || structure[k].Err != nil {
				continue
			}
			leg := LegParams{
				Quoter: QuoterNone, InDecimals: uint8(*pr.in.Decimals), OutDecimals: uint8(*pr.out.Decimals),
			}
			if pr.relative {
				inM, ok1 := member(structure[k], hexAddr(pr.in.Address))
				outM, ok2 := member(structure[k], hexAddr(pr.out.Address))
				if !ok1 || !ok2 || inM.PriceIndex == nil || outM.PriceIndex == nil {
					continue
				}
				leg.Quoter, leg.InIndex, leg.OutIndex = QuoterRelative, *inM.PriceIndex, *outM.PriceIndex
			}
			local, err := leg.Gross(&pr.value)
			verified[pr.index] = err == nil && local.Eq(&quotes[k].Value)
		}
	}

	writes := make([]PoolParamsWrite, 0, len(pools))
	corrections := 0
	var failures []string
	for i, p := range pools {
		if params[i].Err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", p.Address, params[i].Err))
			continue
		}
		if pointersDiffer(p, params[i]) {
			corrections++
			s.fees.ForgetPool(addrs[i])
		}
		if p.ChainState == nil || p.ChainState.QuoterVerified != verified[i] ||
			p.ChainState.DefaultFeePPM != params[i].DefaultFeePPM ||
			p.ChainState.ProtocolFeePPM != params[i].ProtocolFeePPM {
			corrections++
		}
		writes = append(writes, PoolParamsWrite{PoolID: p.ID, Params: params[i], QuoterVerified: verified[i], Block: block})
	}
	if len(writes) > 0 {
		if err := s.store.WritePoolParams(ctx, writes); err != nil {
			return 0, 0, err
		}
		s.requestReload()
	}
	if len(failures) > 0 {
		return corrections, block, fmt.Errorf("%d pools unread: %s", len(failures), strings.Join(failures[:min(3, len(failures))], "; "))
	}
	return corrections, block, nil
}

func pointersDiffer(row PoolRow, p PoolParams) bool {
	return hexAddr(row.RegistryAddress) != p.Registry || hexAddr(row.LimiterAddress) != p.Limiter ||
		hexAddr(row.QuoterAddress) != p.Quoter || hexAddr(row.FeePolicyAddress) != p.FeePolicy ||
		hexAddr(row.ControllerAddress) != p.Controller || row.FeesDecoupled != p.FeesDecoupled
}

// refreshLoop re-reads single pools on request, after a seal or upgrade.
func (s *Service) refreshLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case addr := <-s.refresh:
			pending := map[common.Address]bool{addr: true}
			// Debounce bursts: an upgrade often touches several contracts.
			timer := time.NewTimer(s.cfg.DirtyDebounce)
		collect:
			for {
				select {
				case more := <-s.refresh:
					pending[more] = true
				case <-timer.C:
					break collect
				case <-ctx.Done():
					timer.Stop()
					return
				}
			}
			rows, err := s.store.LoadRouterState(ctx, s.cfg.ChainID)
			if err != nil {
				s.health.fail(loopParams, err)
				continue
			}
			var pools []PoolRow
			for _, p := range rows.Pools {
				if !p.Active {
					continue
				}
				for _, a := range []string{p.Address, p.RegistryAddress, p.LimiterAddress, p.QuoterAddress, p.FeePolicyAddress, p.ControllerAddress} {
					if present(a) && pending[hexAddr(a)] {
						pools = append(pools, p)
						break
					}
				}
			}
			if _, _, err := s.refreshPools(ctx, rows, pools); err != nil {
				s.logg.Warn("router: pool refresh failed", "error", err)
			}
		}
	}
}

// sweepBalances reads balanceOf and fees for every edge of every active pool.
func (s *Service) sweepBalances(ctx context.Context) (int, uint64, error) {
	snap, err := s.current()
	if err != nil {
		return 0, 0, err
	}
	var keys []edgeRef
	for i := range snap.Edges {
		e := &snap.Edges[i]
		if !snap.Pools[e.Pool].Active {
			continue
		}
		keys = append(keys, edgeRef{pool: e.Pool, token: e.Token})
	}
	return s.readBalances(ctx, snap, keys)
}

func (s *Service) readBalances(ctx context.Context, snap *Snapshot, refs []edgeRef) (int, uint64, error) {
	if len(refs) == 0 {
		return 0, 0, nil
	}
	block, err := s.head(ctx)
	if err != nil {
		return 0, 0, err
	}
	probes := make([]BalanceProbe, len(refs))
	for i, r := range refs {
		probes[i] = BalanceProbe{Pool: snap.Pools[r.pool].Addr, Token: snap.Tokens[r.token].Address}
	}
	readings, err := s.chain.Balances(ctx, block, probes)
	if err != nil {
		return 0, 0, err
	}
	var (
		writes      []LiquidityWrite
		corrections int
		failed      int
	)
	for i, r := range readings {
		if r.Err != nil {
			failed++
			continue
		}
		ref := refs[i]
		key := EdgeKey{snap.Pools[ref.pool].DBID, snap.Tokens[ref.token].DBID}
		if edge, ok := snap.Edge(ref.pool, ref.token); ok && edge.HasBalance &&
			(!edge.Balance.Eq(&r.Balance) || !edge.FeesHeld.Eq(&r.FeesHeld)) {
			corrections++
		}
		writes = append(writes, LiquidityWrite{Key: key, Balance: r.Balance, FeesHeld: r.FeesHeld, Block: block})
	}
	applied, err := s.store.WriteLiquidity(ctx, writes)
	if err != nil {
		return 0, 0, err
	}
	appliedSet := make(map[EdgeKey]bool, len(applied))
	for _, k := range applied {
		appliedSet[k] = true
	}
	deltas := make([]Delta, 0, len(applied))
	for _, w := range writes {
		if appliedSet[w.Key] {
			deltas = append(deltas, EdgeLiquidityDelta(w))
		}
	}
	s.Apply(deltas...)
	if failed > 0 {
		return corrections, block, fmt.Errorf("%d balances unread", failed)
	}
	return corrections, block, nil
}

// sweepPairFees reads the exact fee of every eligible leg, spread over half
// the interval so the RPC sees a trickle rather than a burst.
func (s *Service) sweepPairFees(ctx context.Context) (int, uint64, error) {
	snap, err := s.current()
	if err != nil {
		return 0, 0, err
	}
	var probes []PairProbe
	for token := range snap.legs {
		for _, leg := range snap.legs[token] {
			probes = append(probes, PairProbe{
				Pool: snap.Pools[leg.Pool].Addr, In: snap.Tokens[leg.In].Address, Out: snap.Tokens[leg.Out].Address,
			})
		}
	}
	if len(probes) == 0 {
		return 0, 0, nil
	}
	chunk := s.cfg.ChainBatchSize
	chunks := (len(probes) + chunk - 1) / chunk
	pause := s.cfg.PairFeeInterval / time.Duration(2*chunks)
	changed, failed := 0, 0
	var lastBlock uint64
	for start := 0; start < len(probes); start += chunk {
		block, err := s.head(ctx)
		if err != nil {
			return changed, lastBlock, err
		}
		lastBlock = block
		end := min(start+chunk, len(probes))
		readings, err := s.chain.PairFees(ctx, block, probes[start:end])
		if err != nil {
			return changed, lastBlock, err
		}
		for _, r := range readings {
			if r.Err != nil {
				failed++
				continue
			}
			pool, _ := snap.PoolByAddress(r.Pool)
			if previous, ok := s.fees.Get(r.Pool, r.In, r.Out); !ok && r.FeePPM != snap.Pools[pool].DefaultFeePPM || ok && previous != r.FeePPM {
				changed++
			}
			s.fees.Put(r.Pool, r.In, r.Out, r.FeePPM, block)
		}
		if end < len(probes) {
			select {
			case <-ctx.Done():
				return changed, lastBlock, ctx.Err()
			case <-time.After(pause):
			}
		}
	}
	select {
	case s.deltas <- republishDelta{}:
	default:
		s.requestReload()
	}
	if failed > 0 {
		return changed, lastBlock, fmt.Errorf("%d pair fees unread", failed)
	}
	return changed, lastBlock, nil
}

func newDirtyQueue() *dirtyQueue {
	return &dirtyQueue{set: map[[2]common.Address]struct{}{}, signal: make(chan struct{}, 1)}
}

func (q *dirtyQueue) add(pool, token common.Address) {
	q.mu.Lock()
	q.set[[2]common.Address{pool, token}] = struct{}{}
	q.mu.Unlock()
	select {
	case q.signal <- struct{}{}:
	default:
	}
}

func (q *dirtyQueue) drain() [][2]common.Address {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([][2]common.Address, 0, len(q.set))
	for k := range q.set {
		out = append(out, k)
	}
	clear(q.set)
	return out
}

func (q *dirtyQueue) depth() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.set)
}

func (s *Service) markDirty(pool, token common.Address) { s.dirty.add(pool, token) }

// dirtyLoop debounces event-triggered balance reads and batches them.
func (s *Service) dirtyLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.dirty.signal:
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.cfg.DirtyDebounce):
		}
		pending := s.dirty.drain()
		snap := s.Snapshot()
		if snap == nil || len(pending) == 0 {
			continue
		}
		var refs []edgeRef
		for _, p := range pending {
			pool, okP := snap.PoolByAddress(p[0])
			token, okT := snap.TokenByAddress(p[1])
			if !okP || !okT {
				continue
			}
			if _, ok := snap.Edge(pool, token); ok {
				refs = append(refs, edgeRef{pool: pool, token: token})
			}
		}
		start := time.Now()
		_, block, err := s.readBalances(ctx, snap, refs)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				s.health.fail(loopDirty, err)
				// Put them back; the next signal or the sweep retries.
				for _, p := range pending {
					s.dirty.add(p[0], p[1])
				}
			}
			continue
		}
		s.health.ok(loopDirty, time.Since(start), len(refs), block)
	}
}

// Stats reports counts and freshness.
func (s *Service) Stats() (Stats, error) {
	snap, err := s.current()
	if err != nil {
		return Stats{}, err
	}
	f := s.freshness(snap)
	st := Stats{
		GraphVersion: snap.Version, AsOfBlock: snap.Block, HeadBlock: f.HeadBlock, Stale: f.Stale,
		Initialized: s.health.initialized(), BuiltAt: snap.BuiltAt,
		Pools: len(snap.Pools), Vouchers: len(snap.Tokens), Edges: len(snap.Edges), Legs: snap.legCount,
		PairFees: s.fees.Len(), DirtyQueue: s.dirty.depth(), Loops: s.health.snapshot(),
	}
	for _, p := range snap.Pools {
		if p.Active {
			st.ActivePools++
		}
		if p.Verified {
			st.Verified++
		}
	}
	return st, nil
}
