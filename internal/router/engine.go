package router

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"slices"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
)

type (
	// Holding is a voucher balance a wallet can spend from.
	Holding struct {
		Token   int32
		Balance uint256.Int
	}

	// ReachedVoucher is a voucher some eligible route delivers.
	ReachedVoucher struct {
		Token   int32
		MinHops int
	}

	// ReachedPool is a pool some eligible route ends in.
	ReachedPool struct {
		Pool    int32
		MinHops int
	}

	// ReachResult is the estimated reachable set for a wallet.
	ReachResult struct {
		Depth    int
		Inputs   []Holding
		Vouchers []ReachedVoucher
		Pools    []ReachedPool
		Digest   common.Hash
	}

	// Route is a sequence of legs, each paying in what the previous one delivers.
	Route []Leg

	// Mode is how a requested amount is read.
	Mode string

	// RouteEval is a route assessed on snapshot numbers.
	RouteEval struct {
		ID         string
		Route      Route
		Params     []LegParams
		FeeSources []FeeSource

		// RateIn and RateOut price one whole input voucher through the route,
		// arithmetic only. RateKnown is false when that amount rounds to nothing.
		RateIn, RateOut uint256.Int
		RateKnown       bool
		EffectiveFeePPM uint256.Int

		Max MaxResult

		// Set when an amount was requested.
		HasAmount     bool
		CarriesAmount bool
		// AmountIn and AmountOut are the estimate for the requested amount when
		// the route carries it.
		AmountIn, AmountOut uint256.Int
		Refusal             *QuoteError
	}

	// RankConfig carries the ranking policy knobs.
	RankConfig struct {
		// TieMarginPPM treats two outputs (or inputs) within this relative
		// margin as tied, so an extra hop is not chosen for a negligible gain.
		// Zero means exact comparison.
		TieMarginPPM uint64
	}

	// RoutesResult is the routes to one voucher, grouped by input voucher.
	RoutesResult struct {
		Target int32
		Groups []RouteGroup
		// Total is the number of listed routes across all groups.
		Total int
		// OrderDigest commits to the full ordered listing, so a cursor taken from
		// one page can detect that the listing moved under it.
		OrderDigest common.Hash
	}

	// RouteGroup is one input voucher's ranked routes.
	RouteGroup struct {
		Input  Holding
		Routes []RouteEval
	}

	// FallbackPool is a pool that holds the target, listed so a screen with no
	// route still says where the voucher lives.
	FallbackPool struct {
		Pool      int32
		Liquidity uint256.Int
		Accepts   []int32
	}
)

// spendable keeps holdings with a positive balance in a routable voucher,
// ordered by token id.
func spendable(snap *Snapshot, holdings []Holding) []Holding {
	out := make([]Holding, 0, len(holdings))
	seen := make(map[int32]struct{}, len(holdings))
	for _, h := range holdings {
		if h.Balance.IsZero() {
			continue
		}
		if _, dup := seen[h.Token]; dup {
			continue
		}
		if ok, _ := snap.TokenRoutable(h.Token); !ok {
			continue
		}
		seen[h.Token] = struct{}{}
		out = append(out, h)
	}
	slices.SortFunc(out, func(a, b Holding) int { return int(a.Token - b.Token) })
	return out
}

// Reach walks eligible legs from the wallet's vouchers to depth 1 or 2. It
// records, per reached voucher and per pool, the fewest hops of any route; a
// pool counts when a route ends in it. Pools and vouchers along a route are
// distinct, as the batch requires.
func Reach(snap *Snapshot, holdings []Holding, depth int) ReachResult {
	inputs := spendable(snap, holdings)
	vouchers := map[int32]int{}
	pools := map[int32]int{}
	record := func(token, pool int32, hops int) {
		if current, ok := vouchers[token]; !ok || hops < current {
			vouchers[token] = hops
		}
		if current, ok := pools[pool]; !ok || hops < current {
			pools[pool] = hops
		}
	}

	type witness struct{ from, pool int32 }
	bridges := map[int32][]witness{}
	for _, h := range inputs {
		for _, leg := range snap.legs[h.Token] {
			record(leg.Out, leg.Pool, 1)
			if depth >= 2 {
				bridges[leg.Out] = append(bridges[leg.Out], witness{from: h.Token, pool: leg.Pool})
			}
		}
	}
	if depth >= 2 {
		for bridge, witnesses := range bridges {
			for _, leg := range snap.legs[bridge] {
				if hops, ok := vouchers[leg.Out]; ok && hops == 1 {
					if p, ok := pools[leg.Pool]; ok && p == 1 {
						continue // nothing a longer route could improve
					}
				}
				for _, w := range witnesses {
					if w.pool != leg.Pool && w.from != leg.Out {
						record(leg.Out, leg.Pool, 2)
						break
					}
				}
			}
		}
	}

	result := ReachResult{Depth: depth, Inputs: inputs}
	for token, hops := range vouchers {
		result.Vouchers = append(result.Vouchers, ReachedVoucher{Token: token, MinHops: hops})
	}
	for pool, hops := range pools {
		result.Pools = append(result.Pools, ReachedPool{Pool: pool, MinHops: hops})
	}
	slices.SortFunc(result.Vouchers, func(a, b ReachedVoucher) int {
		return bytes.Compare(snap.Tokens[a.Token].Address[:], snap.Tokens[b.Token].Address[:])
	})
	slices.SortFunc(result.Pools, func(a, b ReachedPool) int {
		return bytes.Compare(snap.Pools[a.Pool].Addr[:], snap.Pools[b.Pool].Addr[:])
	})
	result.Digest = reachDigest(snap, result)
	return result
}

func reachDigest(snap *Snapshot, r ReachResult) common.Hash {
	var buf bytes.Buffer
	buf.WriteByte(byte(r.Depth))
	for _, v := range r.Vouchers {
		buf.Write(snap.Tokens[v.Token].Address[:])
		buf.WriteByte(byte(v.MinHops))
	}
	buf.WriteByte(0xff)
	for _, p := range r.Pools {
		buf.Write(snap.Pools[p.Pool].Addr[:])
		buf.WriteByte(byte(p.MinHops))
	}
	return crypto.Keccak256Hash(buf.Bytes())
}

// Input is the voucher the route spends.
func (r Route) Input() int32 { return r[0].In }

// Output is the voucher the route delivers.
func (r Route) Output() int32 { return r[len(r)-1].Out }

// ID is a stable identifier derived from the route's pools and vouchers.
func (r Route) ID(snap *Snapshot) string {
	var buf bytes.Buffer
	for _, leg := range r {
		buf.Write(snap.Pools[leg.Pool].Addr[:])
		buf.Write(snap.Tokens[leg.In].Address[:])
		buf.Write(snap.Tokens[leg.Out].Address[:])
	}
	sum := crypto.Keccak256(buf.Bytes())
	return "r_" + hex.EncodeToString(sum[:10])
}

// EnumerateRoutes lists every eligible route of at most maxHops legs from one
// of the inputs to target, with distinct pools and distinct vouchers.
func EnumerateRoutes(snap *Snapshot, inputs []int32, target int32, maxHops int) []Route {
	var routes []Route
	path := make([]Leg, 0, maxHops)
	usedPools := map[int32]bool{}
	usedTokens := map[int32]bool{}

	var walk func(token int32)
	walk = func(token int32) {
		if len(path) == maxHops {
			return
		}
		for _, leg := range snap.legs[token] {
			if usedPools[leg.Pool] || usedTokens[leg.Out] {
				continue
			}
			path = append(path, leg)
			if leg.Out == target {
				routes = append(routes, slices.Clone(Route(path)))
			} else {
				usedPools[leg.Pool], usedTokens[leg.Out] = true, true
				walk(leg.Out)
				delete(usedPools, leg.Pool)
				delete(usedTokens, leg.Out)
			}
			path = path[:len(path)-1]
		}
	}

	for _, input := range inputs {
		if input == target {
			continue
		}
		usedTokens[input] = true
		walk(input)
		delete(usedTokens, input)
	}
	return routes
}

const (
	ExactInput  Mode = "exactInput"
	ExactOutput Mode = "exactOutput"
)

// arithmeticRoute chains Arithmetic through every leg, ignoring capacity.
func arithmeticRoute(params []LegParams, amountIn *uint256.Int) (uint256.Int, error) {
	value := *amountIn
	for i := range params {
		hop, err := params[i].Arithmetic(&value)
		if err != nil {
			return uint256.Int{}, withHop(err, i)
		}
		value = hop.Net
	}
	return value, nil
}

// Evaluate assesses a route for a wallet holding balance of its input. With a
// positive amount, it also checks whether the route carries it in mode.
func Evaluate(snap *Snapshot, route Route, balance *uint256.Int, mode Mode, amount *uint256.Int, budget int) RouteEval {
	eval := RouteEval{ID: route.ID(snap), Route: route}
	for _, leg := range route {
		params, source := snap.LegParams(leg)
		eval.Params = append(eval.Params, params)
		eval.FeeSources = append(eval.FeeSources, source)
	}

	unit, ok := pow10(snap.Tokens[route.Input()].Decimals)
	if ok {
		eval.RateIn = *unit
		if out, err := arithmeticRoute(eval.Params, unit); err == nil {
			eval.RateOut, eval.RateKnown = out, true
			if feeFree, err := FeeFree(eval.Params, unit); err == nil {
				eval.EffectiveFeePPM = EffectiveFeePPM(&feeFree, &out)
			}
		}
	}

	eval.Max = MaxInput(eval.Params, balance, budget)

	if amount != nil && !amount.IsZero() {
		eval.HasAmount = true
		var (
			quote RouteQuote
			err   error
		)
		if mode == ExactOutput {
			quote, err = QuoteExactOutput(eval.Params, amount, balance, budget)
		} else {
			quote, err = SwapRoute(eval.Params, amount, balance)
		}
		if err == nil {
			eval.CarriesAmount = true
			eval.AmountIn, eval.AmountOut = quote.AmountIn(), quote.AmountOut()
		} else if qe := (*QuoteError)(nil); errors.As(err, &qe) {
			eval.Refusal = qe
		}
	}
	return eval
}

// Listed reports whether discovery shows the route: it is dropped only when a
// constraint proves it cannot carry anything.
func (e RouteEval) Listed() bool { return e.Max.Unavailable == "" }

// within reports whether a and b differ by at most margin ppm of the larger.
func within(a, b *uint256.Int, margin uint64) bool {
	if margin == 0 {
		return a.Eq(b)
	}
	hi, lo := a, b
	if lo.Gt(hi) {
		hi, lo = lo, hi
	}
	diff := new(uint256.Int).Sub(hi, lo)
	lhs, overflowL := new(uint256.Int).MulOverflow(diff, ppm)
	rhs, overflowR := new(uint256.Int).MulOverflow(hi, uint256.NewInt(margin))
	if overflowL || overflowR {
		return false
	}
	return !lhs.Gt(rhs)
}

// Rank orders one input voucher's routes:
//
//   - With an amount, routes that carry it come first: by highest delivered
//     output for exact input, by lowest required input for exact output.
//   - Routes that cannot carry it follow, by rate.
//   - Without an amount, by rate: delivered output per whole input voucher.
//   - Ties go to fewer hops, then pool, bridge and input addresses, so the
//     order is deterministic.
func Rank(snap *Snapshot, evals []RouteEval, mode Mode, cfg RankConfig) {
	slices.SortStableFunc(evals, func(a, b RouteEval) int {
		if a.HasAmount || b.HasAmount {
			if a.CarriesAmount != b.CarriesAmount {
				if a.CarriesAmount {
					return -1
				}
				return 1
			}
			if a.CarriesAmount {
				if mode == ExactOutput {
					// lower input first
					if !within(&a.AmountIn, &b.AmountIn, cfg.TieMarginPPM) {
						return a.AmountIn.Cmp(&b.AmountIn)
					}
				} else if !within(&a.AmountOut, &b.AmountOut, cfg.TieMarginPPM) {
					return b.AmountOut.Cmp(&a.AmountOut)
				}
				return tieBreak(snap, a.Route, b.Route)
			}
		}
		if a.RateKnown != b.RateKnown {
			if a.RateKnown {
				return -1
			}
			return 1
		}
		if a.RateKnown && !within(&a.RateOut, &b.RateOut, cfg.TieMarginPPM) {
			// Same input amount across one voucher's routes, so comparing
			// outputs is the integer cross-multiplication of the rates.
			left := new(uint256.Int).Mul(&a.RateOut, &b.RateIn)
			right := new(uint256.Int).Mul(&b.RateOut, &a.RateIn)
			return right.Cmp(left)
		}
		return tieBreak(snap, a.Route, b.Route)
	})
}

func tieBreak(snap *Snapshot, a, b Route) int {
	if len(a) != len(b) {
		return len(a) - len(b)
	}
	for i := range a {
		if c := bytes.Compare(snap.Pools[a[i].Pool].Addr[:], snap.Pools[b[i].Pool].Addr[:]); c != 0 {
			return c
		}
	}
	for i := range a {
		if c := bytes.Compare(snap.Tokens[a[i].Out].Address[:], snap.Tokens[b[i].Out].Address[:]); c != 0 {
			return c
		}
	}
	return bytes.Compare(snap.Tokens[a[0].In].Address[:], snap.Tokens[b[0].In].Address[:])
}

// FindRoutes enumerates, evaluates and ranks every route from the wallet's
// holdings to target.
func FindRoutes(snap *Snapshot, holdings []Holding, target int32, maxHops int, from *int32, mode Mode, amount *uint256.Int, budget int, rank RankConfig) RoutesResult {
	inputs := spendable(snap, holdings)
	result := RoutesResult{Target: target}
	for _, h := range inputs {
		if from != nil && h.Token != *from {
			continue
		}
		if h.Token == target {
			continue
		}
		routes := EnumerateRoutes(snap, []int32{h.Token}, target, maxHops)
		if len(routes) == 0 {
			continue
		}
		group := RouteGroup{Input: h}
		for _, route := range routes {
			eval := Evaluate(snap, route, &h.Balance, mode, amount, budget)
			if eval.Listed() {
				group.Routes = append(group.Routes, eval)
			}
		}
		if len(group.Routes) == 0 {
			continue
		}
		Rank(snap, group.Routes, mode, rank)
		result.Groups = append(result.Groups, group)
		result.Total += len(group.Routes)
	}
	slices.SortFunc(result.Groups, func(a, b RouteGroup) int {
		return bytes.Compare(snap.Tokens[a.Input.Token].Address[:], snap.Tokens[b.Input.Token].Address[:])
	})

	var buf bytes.Buffer
	for _, g := range result.Groups {
		buf.Write(snap.Tokens[g.Input.Token].Address[:])
		for _, r := range g.Routes {
			buf.WriteString(r.ID)
			buf.WriteByte(boolByte(r.CarriesAmount))
		}
	}
	result.OrderDigest = crypto.Keccak256Hash(buf.Bytes())
	return result
}

func boolByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}

// Page cuts the flattened listing at [offset, offset+limit) and regroups it.
func (r RoutesResult) Page(offset, limit int) []RouteGroup {
	var page []RouteGroup
	index := 0
	for _, g := range r.Groups {
		var routes []RouteEval
		for _, route := range g.Routes {
			if index >= offset && index < offset+limit {
				routes = append(routes, route)
			}
			index++
		}
		if len(routes) > 0 {
			page = append(page, RouteGroup{Input: g.Input, Routes: routes})
		}
	}
	return page
}

// FallbackPools lists active pools holding a positive, payable balance of
// target, with the vouchers each accepts.
func FallbackPools(snap *Snapshot, target int32) []FallbackPool {
	var out []FallbackPool
	for _, edgeID := range snap.byToken[target] {
		edge := &snap.Edges[edgeID]
		pool := &snap.Pools[edge.Pool]
		if !pool.Active || !edge.Allowed || !edge.HasBalance {
			continue
		}
		liquidity := edge.Balance
		if pool.FeesDecoupled {
			if edge.Balance.Gt(&edge.FeesHeld) {
				liquidity = *new(uint256.Int).Sub(&edge.Balance, &edge.FeesHeld)
			} else {
				liquidity = uint256.Int{}
			}
		}
		if liquidity.IsZero() {
			continue
		}
		fp := FallbackPool{Pool: edge.Pool, Liquidity: liquidity}
		for _, other := range snap.byPool[edge.Pool] {
			in := &snap.Edges[other]
			if in.Token == target || !in.Allowed || !snap.Tokens[in.Token].Active {
				continue
			}
			if pool.HasLimiter && !in.Limit.Gt(&in.Balance) {
				continue
			}
			fp.Accepts = append(fp.Accepts, in.Token)
		}
		out = append(out, fp)
	}
	slices.SortFunc(out, func(a, b FallbackPool) int { return b.Liquidity.Cmp(&a.Liquidity) })
	return out
}

// pageCursor is what a routes cursor encodes.
func encodeOffset(offset int, digest common.Hash) []byte {
	buf := make([]byte, 8, 40)
	binary.BigEndian.PutUint64(buf, uint64(offset))
	return append(buf, digest[:]...)
}

func decodeOffset(raw []byte) (int, common.Hash, bool) {
	if len(raw) != 40 {
		return 0, common.Hash{}, false
	}
	offset := binary.BigEndian.Uint64(raw[:8])
	if offset > 1<<31 {
		return 0, common.Hash{}, false
	}
	return int(offset), common.BytesToHash(raw[8:]), true
}
