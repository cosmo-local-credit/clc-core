package router

import (
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

type (
	// PoolRow is one catalog pool as the store fills it. The row types here
	// mirror pools, vouchers, pool_tokens, pool_token_liquidity and
	// pool_chain_state; amounts travel as decimal text so a NUMERIC never
	// passes through a float.
	PoolRow struct {
		ID                uint64
		Address           string
		Name              string
		Active            bool
		QuoterType        string
		RegistryAddress   string
		LimiterAddress    string
		QuoterAddress     string
		FeePolicyAddress  string
		ControllerAddress string
		FeesDecoupled     bool
		// ChainState is nil until the parameter refresh has read the pool.
		ChainState *PoolChainState
	}

	PoolChainState struct {
		DefaultFeePPM        uint64
		ProtocolFeePPM       uint64
		ProtocolFeeRecipient string
		SealState            uint8
		QuoterVerified       bool
		Block                uint64
	}

	TokenRow struct {
		ID          uint64
		Address     string
		Symbol      string
		Name        string
		Decimals    *int
		Active      bool
		VoucherType string
	}

	EdgeRow struct {
		PoolID     uint64
		VoucherID  uint64
		Allowed    bool
		Limit      *string
		PriceIndex *string
		AllowedPos Position
		LimitPos   Position
		PricePos   Position
		// Liquidity is nil until a balance read has landed.
		Balance      *string
		FeesHeld     *string
		BalanceBlock uint64
	}

	StateRows struct {
		Pools  []PoolRow
		Tokens []TokenRow
		Edges  []EdgeRow
	}

	// EdgeKey identifies a pool_tokens row by database ids.
	EdgeKey struct {
		PoolID    uint64
		VoucherID uint64
	}

	// Token is a voucher in the graph. Graph records use dense indexes,
	// assigned at build time, as ids.
	Token struct {
		DBID     uint64
		Address  common.Address
		Symbol   string
		Name     string
		Decimals uint8
		// HasDecimals is false when the catalog does not know them; such a
		// voucher cannot be quoted.
		HasDecimals bool
		Active      bool
		// Plain means transfers move exactly the stated amount and balances do
		// not change on their own (GiftableToken, or an allowlisted ERC-20).
		Plain bool
	}

	Pool struct {
		DBID          uint64
		Address       string
		Addr          common.Address
		Name          string
		Active        bool
		Quoter        QuoterKind
		QuoterAddress common.Address
		Verified      bool
		ParamsKnown   bool
		HasRegistry   bool
		HasLimiter    bool
		FeesDecoupled bool
		DefaultFeePPM uint64
		ProtocolPPM   uint64
		SealState     uint8
		ParamsBlock   uint64
	}

	Edge struct {
		Pool, Token  int32
		Allowed      bool
		Limit        uint256.Int
		Balance      uint256.Int
		FeesHeld     uint256.Int
		PriceIndex   uint256.Int
		PriceKnown   bool
		HasBalance   bool
		BalanceBlock uint64
	}

	// Leg is one eligible directed swap: pay In into Pool, receive Out.
	Leg struct {
		Pool    int32
		In, Out int32
		InEdge  int32
		OutEdge int32
	}

	// Snapshot is an immutable view of the graph. Readers load it once per request
	// and never lock.
	Snapshot struct {
		Version uint64
		// Block is the oldest block any balance in the snapshot was read at; zero
		// while some edge has never been read.
		Block   uint64
		BuiltAt time.Time

		Tokens []Token
		Pools  []Pool
		Edges  []Edge

		byPool   [][]int32
		byToken  [][]int32
		legs     [][]Leg
		legCount int

		tokenByAddr map[common.Address]int32
		poolByAddr  map[common.Address]int32
		edgeIndex   map[[2]int32]int32

		fees *FeeBook
	}

	// FeeSource says where a leg's fee came from.
	FeeSource string

	// FeeBook holds exact pair fees read from the chain, with the block they were
	// read at. FeePolicy's per-pair overrides sit in a mapping no call can
	// enumerate, so until a pair has been read discovery uses the pool's default.
	FeeBook struct {
		mu   sync.RWMutex
		fees map[feeKey]feeEntry
	}

	feeKey struct {
		pool, in, out common.Address
	}
	feeEntry struct {
		ppm   uint64
		block uint64
	}

	// BuildConfig carries what eligibility needs beyond the rows.
	BuildConfig struct {
		// PlainERC20 lists external ERC-20 vouchers confirmed to have plain
		// transfer behaviour.
		PlainERC20 map[common.Address]struct{}
	}

	// State is the writer's mutable copy of the rows. Only the writer goroutine
	// touches it.
	State struct {
		Pools  map[uint64]*PoolRow
		Tokens map[uint64]*TokenRow
		Edges  map[EdgeKey]*EdgeState
	}

	// EdgeState is an edge with parsed amounts and the position of each field's
	// last write.
	EdgeState struct {
		Allowed      bool
		AllowedPos   Position
		Limit        uint256.Int
		HasLimit     bool
		LimitPos     Position
		PriceIndex   uint256.Int
		PriceKnown   bool
		PricePos     Position
		Balance      uint256.Int
		FeesHeld     uint256.Int
		HasBalance   bool
		BalanceBlock uint64
	}
)

// TokenByAddress resolves a voucher.
func (s *Snapshot) TokenByAddress(address common.Address) (int32, bool) {
	id, ok := s.tokenByAddr[address]
	return id, ok
}

// PoolByAddress resolves a pool.
func (s *Snapshot) PoolByAddress(address common.Address) (int32, bool) {
	id, ok := s.poolByAddr[address]
	return id, ok
}

// Edge returns the pool_tokens row joining pool and token.
func (s *Snapshot) Edge(pool, token int32) (*Edge, bool) {
	id, ok := s.edgeIndex[[2]int32{pool, token}]
	if !ok {
		return nil, false
	}
	return &s.Edges[id], true
}

// PoolEdges lists a pool's edges.
func (s *Snapshot) PoolEdges(pool int32) []int32 { return s.byPool[pool] }

// TokenEdges lists the pools holding a token.
func (s *Snapshot) TokenEdges(token int32) []int32 { return s.byToken[token] }

// Legs lists the eligible legs paying in token.
func (s *Snapshot) Legs(token int32) []Leg { return s.legs[token] }

// LegCount is the number of eligible directed legs.
func (s *Snapshot) LegCount() int { return s.legCount }

// Fees is the pair-fee book shared by every snapshot.
func (s *Snapshot) Fees() *FeeBook { return s.fees }

const (
	FeeSourcePair    FeeSource = "pair"
	FeeSourceDefault FeeSource = "default"
)

// LegParams assembles the arithmetic inputs for one leg from the snapshot.
func (s *Snapshot) LegParams(leg Leg) (LegParams, FeeSource) {
	pool := &s.Pools[leg.Pool]
	in, out := &s.Edges[leg.InEdge], &s.Edges[leg.OutEdge]
	fee, source := pool.DefaultFeePPM, FeeSourceDefault
	if ppmValue, ok := s.fees.Get(pool.Addr, s.Tokens[leg.In].Address, s.Tokens[leg.Out].Address); ok {
		fee, source = ppmValue, FeeSourcePair
	}
	return LegParams{
		Quoter:        pool.Quoter,
		FeePPM:        fee,
		ProtocolPPM:   pool.ProtocolPPM,
		HasRegistry:   pool.HasRegistry,
		HasLimiter:    pool.HasLimiter,
		FeesDecoupled: pool.FeesDecoupled,
		InAllowed:     in.Allowed,
		InDecimals:    s.Tokens[leg.In].Decimals,
		InIndex:       in.PriceIndex,
		InLimit:       in.Limit,
		InBalance:     in.Balance,
		OutAllowed:    out.Allowed,
		OutDecimals:   s.Tokens[leg.Out].Decimals,
		OutIndex:      out.PriceIndex,
		OutBalance:    out.Balance,
		OutFeesHeld:   out.FeesHeld,
	}, source
}

// LegFor finds the leg (pool, in, out) whether or not it is eligible.
func (s *Snapshot) LegFor(pool, in, out int32) (Leg, bool) {
	inEdge, ok := s.edgeIndex[[2]int32{pool, in}]
	if !ok {
		return Leg{}, false
	}
	outEdge, ok := s.edgeIndex[[2]int32{pool, out}]
	if !ok {
		return Leg{}, false
	}
	return Leg{Pool: pool, In: in, Out: out, InEdge: inEdge, OutEdge: outEdge}, true
}

// PoolRoutable reports whether a pool can take part in a route at all.
func (s *Snapshot) PoolRoutable(pool int32) (bool, Reason) {
	p := &s.Pools[pool]
	switch {
	case !p.Active:
		return false, ReasonRetired
	case p.Quoter != QuoterRelative && p.Quoter != QuoterNone, !p.Verified, !p.ParamsKnown:
		return false, ReasonUnsupportedQuoter
	}
	return true, ""
}

// TokenRoutable reports whether a voucher can take part in a route at all.
func (s *Snapshot) TokenRoutable(token int32) (bool, Reason) {
	t := &s.Tokens[token]
	switch {
	case !t.Active:
		return false, ReasonRetired
	case !t.Plain, !t.HasDecimals:
		return false, ReasonUnsupportedToken
	}
	return true, ""
}

// eligible decides whether a directed leg is listed for discovery:
//
//   - the pool and both vouchers are active and routable, and the pool's
//     quoter is relative (or absent) and verified;
//   - in != out, and both are allowed;
//   - the pool has deposit headroom for in;
//   - the pool holds some out beyond any decoupled fees;
//   - the fee it would charge is inside the contract's fee domain.
func (s *Snapshot) eligible(leg Leg) bool {
	if leg.In == leg.Out {
		return false
	}
	if ok, _ := s.PoolRoutable(leg.Pool); !ok {
		return false
	}
	if ok, _ := s.TokenRoutable(leg.In); !ok {
		return false
	}
	if ok, _ := s.TokenRoutable(leg.Out); !ok {
		return false
	}
	pool := &s.Pools[leg.Pool]
	in, out := &s.Edges[leg.InEdge], &s.Edges[leg.OutEdge]
	if !in.Allowed || !out.Allowed || !in.HasBalance || !out.HasBalance {
		return false
	}
	if pool.Quoter == QuoterRelative && (!in.PriceKnown || !out.PriceKnown) {
		return false
	}
	params, _ := s.LegParams(leg)
	headroom := params.Headroom()
	available := params.Available()
	if headroom.IsZero() || available.IsZero() {
		return false
	}
	return FeeDomainOK(params.FeePPM, params.ProtocolPPM)
}

// NewFeeBook returns an empty book.
func NewFeeBook() *FeeBook { return &FeeBook{fees: map[feeKey]feeEntry{}} }

// Get returns the recorded fee for a pair.
func (b *FeeBook) Get(pool, in, out common.Address) (uint64, bool) {
	if b == nil {
		return 0, false
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	entry, ok := b.fees[feeKey{pool, in, out}]
	return entry.ppm, ok
}

// Put records a fee read at block, keeping the newer of two reads.
func (b *FeeBook) Put(pool, in, out common.Address, ppm, block uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := feeKey{pool, in, out}
	if current, ok := b.fees[key]; ok && current.block > block {
		return
	}
	b.fees[key] = feeEntry{ppm: ppm, block: block}
}

// ForgetPool drops every fee recorded for a pool, after its fee policy moved.
func (b *FeeBook) ForgetPool(pool common.Address) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for key := range b.fees {
		if key.pool == pool {
			delete(b.fees, key)
		}
	}
}

// Len is the number of recorded pairs.
func (b *FeeBook) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.fees)
}

// NewState builds writer state from store rows.
func NewState(rows StateRows) *State {
	st := &State{
		Pools:  make(map[uint64]*PoolRow, len(rows.Pools)),
		Tokens: make(map[uint64]*TokenRow, len(rows.Tokens)),
		Edges:  make(map[EdgeKey]*EdgeState, len(rows.Edges)),
	}
	for i := range rows.Pools {
		row := rows.Pools[i]
		st.Pools[row.ID] = &row
	}
	for i := range rows.Tokens {
		row := rows.Tokens[i]
		st.Tokens[row.ID] = &row
	}
	for _, row := range rows.Edges {
		edge := &EdgeState{
			Allowed:      row.Allowed,
			AllowedPos:   row.AllowedPos,
			LimitPos:     row.LimitPos,
			PricePos:     row.PricePos,
			BalanceBlock: row.BalanceBlock,
		}
		if row.Limit != nil {
			if v, err := uint256.FromDecimal(*row.Limit); err == nil {
				edge.Limit, edge.HasLimit = *v, true
			}
		}
		if row.PriceIndex != nil {
			if v, err := uint256.FromDecimal(*row.PriceIndex); err == nil {
				edge.PriceIndex, edge.PriceKnown = *v, true
			}
		}
		if row.Balance != nil {
			balance, errB := uint256.FromDecimal(*row.Balance)
			fees := uint256.NewInt(0)
			var errF error
			if row.FeesHeld != nil {
				fees, errF = uint256.FromDecimal(*row.FeesHeld)
			}
			if errB == nil && errF == nil {
				edge.Balance, edge.FeesHeld, edge.HasBalance = *balance, *fees, true
			}
		}
		st.Edges[EdgeKey{row.PoolID, row.VoucherID}] = edge
	}
	return st
}

func parseQuoterKind(quoterType, quoterAddress string) QuoterKind {
	if quoterAddress == "" || common.HexToAddress(quoterAddress) == (common.Address{}) {
		return QuoterNone
	}
	if strings.EqualFold(quoterType, "relative") {
		return QuoterRelative
	}
	return QuoterUnsupported
}

func present(address string) bool {
	return address != "" && common.HexToAddress(address) != (common.Address{})
}

// Build derives an immutable snapshot from the state.
func (st *State) Build(cfg BuildConfig, version uint64, fees *FeeBook) *Snapshot {
	snap := &Snapshot{
		Version:     version,
		BuiltAt:     time.Now(),
		tokenByAddr: make(map[common.Address]int32, len(st.Tokens)),
		poolByAddr:  make(map[common.Address]int32, len(st.Pools)),
		edgeIndex:   make(map[[2]int32]int32, len(st.Edges)),
		fees:        fees,
	}

	// Deterministic ids: order by database id.
	poolIDs := make([]uint64, 0, len(st.Pools))
	for id := range st.Pools {
		poolIDs = append(poolIDs, id)
	}
	slices.Sort(poolIDs)
	poolIndex := make(map[uint64]int32, len(poolIDs))
	for _, id := range poolIDs {
		row := st.Pools[id]
		addr := common.HexToAddress(row.Address)
		pool := Pool{
			DBID:          row.ID,
			Address:       addr.Hex(),
			Addr:          addr,
			Name:          row.Name,
			Active:        row.Active,
			Quoter:        parseQuoterKind(row.QuoterType, row.QuoterAddress),
			HasRegistry:   present(row.RegistryAddress),
			HasLimiter:    present(row.LimiterAddress),
			FeesDecoupled: row.FeesDecoupled,
		}
		if present(row.QuoterAddress) {
			pool.QuoterAddress = common.HexToAddress(row.QuoterAddress)
		}
		if cs := row.ChainState; cs != nil {
			pool.ParamsKnown = true
			pool.Verified = cs.QuoterVerified
			pool.DefaultFeePPM = cs.DefaultFeePPM
			pool.ProtocolPPM = cs.ProtocolFeePPM
			pool.SealState = cs.SealState
			pool.ParamsBlock = cs.Block
		}
		poolIndex[id] = int32(len(snap.Pools))
		snap.poolByAddr[addr] = int32(len(snap.Pools))
		snap.Pools = append(snap.Pools, pool)
	}

	tokenIDs := make([]uint64, 0, len(st.Tokens))
	for id := range st.Tokens {
		tokenIDs = append(tokenIDs, id)
	}
	slices.Sort(tokenIDs)
	tokenIndex := make(map[uint64]int32, len(tokenIDs))
	for _, id := range tokenIDs {
		row := st.Tokens[id]
		addr := common.HexToAddress(row.Address)
		token := Token{DBID: row.ID, Address: addr, Symbol: row.Symbol, Name: row.Name, Active: row.Active}
		if row.Decimals != nil && *row.Decimals >= 0 && *row.Decimals <= 77 {
			token.Decimals, token.HasDecimals = uint8(*row.Decimals), true
		}
		switch strings.ToUpper(row.VoucherType) {
		case "GIFTABLE":
			token.Plain = true
		default:
			_, token.Plain = cfg.PlainERC20[addr]
		}
		tokenIndex[id] = int32(len(snap.Tokens))
		snap.tokenByAddr[addr] = int32(len(snap.Tokens))
		snap.Tokens = append(snap.Tokens, token)
	}

	keys := make([]EdgeKey, 0, len(st.Edges))
	for key := range st.Edges {
		if _, ok := poolIndex[key.PoolID]; !ok {
			continue
		}
		if _, ok := tokenIndex[key.VoucherID]; !ok {
			continue
		}
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b EdgeKey) int {
		if a.PoolID != b.PoolID {
			return cmpUint(a.PoolID, b.PoolID)
		}
		return cmpUint(a.VoucherID, b.VoucherID)
	})

	snap.byPool = make([][]int32, len(snap.Pools))
	snap.byToken = make([][]int32, len(snap.Tokens))
	var oldest uint64
	allRead := true
	for _, key := range keys {
		es := st.Edges[key]
		edge := Edge{
			Pool:         poolIndex[key.PoolID],
			Token:        tokenIndex[key.VoucherID],
			Allowed:      es.Allowed,
			Limit:        es.Limit,
			Balance:      es.Balance,
			FeesHeld:     es.FeesHeld,
			PriceIndex:   es.PriceIndex,
			PriceKnown:   es.PriceKnown,
			HasBalance:   es.HasBalance,
			BalanceBlock: es.BalanceBlock,
		}
		id := int32(len(snap.Edges))
		snap.Edges = append(snap.Edges, edge)
		snap.byPool[edge.Pool] = append(snap.byPool[edge.Pool], id)
		snap.byToken[edge.Token] = append(snap.byToken[edge.Token], id)
		snap.edgeIndex[[2]int32{edge.Pool, edge.Token}] = id
		if edge.Allowed && snap.Pools[edge.Pool].Active {
			if !edge.HasBalance {
				allRead = false
			} else if oldest == 0 || edge.BalanceBlock < oldest {
				oldest = edge.BalanceBlock
			}
		}
	}
	if allRead {
		snap.Block = oldest
	}

	snap.legs = make([][]Leg, len(snap.Tokens))
	for pool := range snap.Pools {
		edges := snap.byPool[pool]
		for _, inEdge := range edges {
			for _, outEdge := range edges {
				if inEdge == outEdge {
					continue
				}
				leg := Leg{
					Pool:    int32(pool),
					In:      snap.Edges[inEdge].Token,
					Out:     snap.Edges[outEdge].Token,
					InEdge:  inEdge,
					OutEdge: outEdge,
				}
				if snap.eligible(leg) {
					snap.legs[leg.In] = append(snap.legs[leg.In], leg)
					snap.legCount++
				}
			}
		}
	}
	return snap
}

func cmpUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
