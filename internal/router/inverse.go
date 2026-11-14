package router

import (
	"errors"
	"slices"

	"github.com/holiman/uint256"
)

const (
	// DefaultSearchBudget is how many full forward evaluations one maximum or
	// exact-output search may spend.
	DefaultSearchBudget = 256

	LimitWallet     = "wallet"
	LimitDepositCap = "deposit_cap"
	LimitLiquidity  = "liquidity"
	LimitRounding   = "rounding"
)

var (
	one = uint256.NewInt(1)
)

// grossOrMax evaluates the quoter, treating an overflow as larger than any
// value: the search predicates below only ever compare it from below.
func (p *LegParams) grossOrMax(value *uint256.Int) uint256.Int {
	gross, err := p.Gross(value)
	if err != nil {
		return *maxU256
	}
	return gross
}

// maxInputFor is the largest x in [0, upTo] with gross(x) <= g. gross is
// monotone, so this is a binary search. The upper end is found by doubling
// from 1, so the search costs about twice log2 of the answer rather than of
// upTo, which is often the whole 256-bit range.
func (p *LegParams) maxInputFor(g, upTo *uint256.Int) uint256.Int {
	lo, hi := uint256.Int{}, *upTo
	if gross := p.grossOrMax(&hi); !gross.Gt(g) {
		return hi
	}
	for probe := uint256.NewInt(1); probe.Lt(&hi); probe.Lsh(probe, 1) {
		if gross := p.grossOrMax(probe); gross.Gt(g) {
			hi = *probe
			break
		}
		lo = *probe
		if probe.Gt(new(uint256.Int).Rsh(maxU256, 1)) {
			break
		}
	}
	// Invariant: gross(lo) <= g (gross(0) = 0), gross(hi) > g.
	for {
		span := new(uint256.Int).Sub(&hi, &lo)
		if !span.Gt(one) {
			return lo
		}
		mid := new(uint256.Int).Rsh(span, 1)
		mid.Add(mid, &lo)
		if gross := p.grossOrMax(mid); gross.Gt(g) {
			hi = *mid
		} else {
			lo = *mid
		}
	}
}

// minInputFor is the smallest x with gross(x) >= g, or false when no input
// reaches it.
func (p *LegParams) minInputFor(g *uint256.Int) (uint256.Int, bool) {
	if g.IsZero() {
		return uint256.Int{}, true
	}
	// Find an upper bound by doubling, then bisect.
	hi := uint256.NewInt(1)
	for {
		if gross := p.grossOrMax(hi); !gross.Lt(g) {
			break
		}
		if hi.Gt(new(uint256.Int).Rsh(maxU256, 1)) {
			return uint256.Int{}, false
		}
		hi.Lsh(hi, 1)
	}
	lo := new(uint256.Int).Rsh(hi, 1) // gross(lo) < g unless lo == 0 and g == 0
	if lo.IsZero() {
		if gross := p.grossOrMax(lo); !gross.Lt(g) {
			return *lo, true
		}
	}
	// Invariant: gross(lo) < g, gross(hi) >= g.
	for {
		span := new(uint256.Int).Sub(hi, lo)
		if !span.Gt(one) {
			return *hi, true
		}
		mid := new(uint256.Int).Rsh(span, 1)
		mid.Add(mid, lo)
		if gross := p.grossOrMax(mid); gross.Lt(g) {
			lo = mid
		} else {
			hi = mid
		}
	}
}

// MinInputForNet is the smallest input whose arithmetic delivers at least
// target, ignoring capacity. Net is not monotone in gross, so it walks the
// producible gross values upward from the proven floor:
//
//   - no gross at or below (T − 3)·D²/K can deliver T (upper bound of
//     SettleDenominator);
//   - every gross from ceil(T·D²/K) up does (lower bound).
//
// The window between them holds at most 3·D²/K + 1 producible values; budget
// caps how many are tried.
func (p *LegParams) MinInputForNet(target *uint256.Int, budget int) (uint256.Int, error) {
	if target.IsZero() {
		return uint256.Int{}, refuse(0, ReasonZeroOutput)
	}
	if !FeeDomainOK(p.FeePPM, p.ProtocolPPM) {
		return uint256.Int{}, refuse(0, ReasonFeeDomain)
	}
	if p.Quoter != QuoterRelative && p.Quoter != QuoterNone {
		return uint256.Int{}, refuse(0, ReasonUnsupportedQuoter)
	}
	k := SettleDenominator(p.FeePPM, p.ProtocolPPM)

	g := uint256.NewInt(1)
	if target.Gt(uint256.NewInt(3)) {
		t3 := new(uint256.Int).Sub(target, uint256.NewInt(3))
		floor, ok := fullMulDiv(t3, ppmSq, &k)
		if !ok {
			return uint256.Int{}, refuse(0, ReasonOverflow)
		}
		g = floor.Add(floor, one)
	}

	for range budget {
		x, ok := p.minInputFor(g)
		if !ok {
			return uint256.Int{}, refuse(0, ReasonOverflow)
		}
		gross, err := p.Gross(&x)
		if err != nil {
			return uint256.Int{}, err
		}
		_, _, net, err := Settle(&gross, p.FeePPM, p.ProtocolPPM)
		if err == nil && !net.Lt(target) {
			return x, nil
		}
		var qe *QuoteError
		if err != nil && errors.As(err, &qe) && qe.Reason != ReasonZeroOutput {
			return uint256.Int{}, err
		}
		if gross.Eq(maxU256) {
			return uint256.Int{}, refuse(0, ReasonOverflow)
		}
		g = new(uint256.Int).Add(&gross, one)
	}
	return uint256.Int{}, refuse(0, ReasonSearchBudget)
}

// QuoteExactOutput finds the smallest first-hop input whose chained batch delivers
// at least target. It works backwards one hop at a time: the smallest last-hop
// input that delivers target, then the smallest previous-hop input that
// delivers that, and so on. Each step is minimal, so the whole is: any smaller
// first input delivers less than the second hop needs, and so on down the
// route.
//
// The returned quote is the forward evaluation of the resulting batch, with
// capacity and wallet checks. Because every input is minimal and capacity only
// tightens as inputs grow, a capacity refusal proves no input can deliver the
// target through this route.
func QuoteExactOutput(legs []LegParams, target *uint256.Int, wallet *uint256.Int, budget int) (RouteQuote, error) {
	if len(legs) == 0 {
		return RouteQuote{}, refuse(-1, ReasonZeroOutput)
	}
	inputs := make([]uint256.Int, len(legs))
	need := *target
	for i, leg := range slices.Backward(legs) {
		x, err := leg.MinInputForNet(&need, budget)
		if err != nil {
			return RouteQuote{}, withHop(err, i)
		}
		inputs[i] = x
		need = x
	}
	return SwapChained(legs, inputs, wallet)
}

type (
	// legLimit is one leg's own capacity limit: the largest input it accepts
	// and which constraint sets it.
	legLimit struct {
		max   uint256.Int
		limit string
	}

	// MaxResult describes the most a wallet can spend through a route.
	MaxResult struct {
		// Known is false when the maximum could not be established. The route
		// stays listed and can still be quoted for a specific amount.
		Known bool
		// Exact means every larger input has been ruled out.
		Exact     bool
		AmountIn  uint256.Int
		AmountOut uint256.Int
		// LimitedBy names the binding constraint: wallet, deposit_cap or
		// liquidity, on hop LimitedByHop.
		LimitedBy    string
		LimitedByHop int
		// Unavailable is set when a constraint proves the route cannot carry any
		// amount.
		Unavailable Reason
	}
)

// legCeiling is the largest input one leg accepts on its own capacity: its
// deposit headroom and the largest input whose gross fits the liquidity.
func (p *LegParams) legCeiling() (uint256.Int, string) {
	headroom := p.Headroom()
	available := p.Available()
	byLiquidity := p.maxInputFor(&available, maxU256)
	if headroom.Lt(&byLiquidity) {
		return headroom, LimitDepositCap
	}
	return byLiquidity, LimitLiquidity
}

// MaxInput is the largest input for which the forward quote succeeds.
//
// For one hop every constraint is monotone and a binary search is exact. For
// more hops it is not: each later hop consumes the previous hop's net, and net
// is not monotone in gross. Everything after hop one depends only on hop one's
// gross, so the search walks hop one's producible gross plateaus down from a
// proven ceiling (see maxChain), and Exact means every larger input was ruled
// out.
func MaxInput(legs []LegParams, wallet *uint256.Int, budget int) MaxResult {
	return maxInput(legs, nil, wallet, budget)
}

// maxInput is MaxInput with each leg's own capacity limit precomputed when
// limits is not nil. A route search shares them across every route that uses
// the same leg, since finding a leg's liquidity bound is a binary search over
// the whole input range.
func maxInput(legs []LegParams, limits []legLimit, wallet *uint256.Int, budget int) MaxResult {
	ceilingOf := func(k int) (uint256.Int, string) {
		if limits != nil {
			return limits[k].max, limits[k].limit
		}
		return legs[k].legCeiling()
	}
	for i := range legs {
		if !FeeDomainOK(legs[i].FeePPM, legs[i].ProtocolPPM) {
			return MaxResult{Unavailable: ReasonFeeDomain, LimitedByHop: i}
		}
		if legs[i].Quoter != QuoterRelative && legs[i].Quoter != QuoterNone {
			return MaxResult{Unavailable: ReasonUnsupportedQuoter, LimitedByHop: i}
		}
	}
	if len(legs) == 0 {
		return MaxResult{}
	}

	// U: the largest input hop one accepts on its own constraints.
	u, limitedBy := ceilingOf(0)
	if wallet != nil && wallet.Lt(&u) {
		u, limitedBy = *wallet, LimitWallet
	}
	if u.IsZero() {
		reason := ReasonDepositCap
		switch limitedBy {
		case LimitWallet:
			reason = ReasonWalletBalance
		case LimitLiquidity:
			reason = ReasonLiquidity
		}
		return MaxResult{Unavailable: reason, LimitedBy: limitedBy}
	}

	if len(legs) == 1 {
		quote, err := SwapRoute(legs, &u, wallet)
		if err != nil {
			// Every constraint below U is monotone, so the only failure left
			// at U is the output rounding to nothing, which no smaller input
			// fixes.
			return MaxResult{Unavailable: reasonOf(err)}
		}
		return MaxResult{Known: true, Exact: true, AmountIn: u, AmountOut: quote.AmountOut(), LimitedBy: limitedBy}
	}
	return maxChain(legs, ceilingOf, u, limitedBy, wallet, budget)
}

// capacityReason is the refusal a leg's own capacity limit produces.
func capacityReason(limit string) Reason {
	if limit == LimitLiquidity {
		return ReasonLiquidity
	}
	return ReasonDepositCap
}

// maxChain finds the maximum for routes of two or more hops.
//
// A backward pass bounds what each later hop k can take, using the settlement
// bounds g·K/D² <= net(g) < g·K/D² + 3:
//
//   - ceil[k] is a proven ceiling: an input to hop k above it is refused at
//     hop k or later. Hop k refuses inputs above its own capacity limit, and
//     any gross above floor(ceil[k+1]·D²/K) nets more than ceil[k+1].
//   - safe[k] is a proven floor: every input to hop k at or below it is
//     accepted on capacity by hop k and every later hop, since any gross at
//     or below floor((safe[k+1] − 3)·D²/K) nets less than safe[k+1].
//
// Hop one's gross then has a ceiling (its net must not exceed ceil[1]) and a
// floor (a net at or below safe[1] always passes). The search starts at the
// top plateau under the ceiling and, on a capacity refusal by any later hop,
// steps to the top of the next lower plateau: every input in between has the
// same gross, so the same outcome at every later hop.
func maxChain(legs []LegParams, ceilingOf func(int) (uint256.Int, string), u uint256.Int, limitedBy string, wallet *uint256.Int, budget int) MaxResult {
	n := len(legs)
	ceil := make([]uint256.Int, n)
	safe := make([]uint256.Int, n)
	ownLimit := make([]string, n)
	bindLimit := make([]string, n)
	bindHop := make([]int, n)
	three := uint256.NewInt(3)

	for k := n - 1; k >= 1; k-- {
		own, limit := ceilingOf(k)
		ownLimit[k] = limit
		if own.IsZero() {
			return MaxResult{Unavailable: capacityReason(limit), LimitedBy: limit, LimitedByHop: k}
		}
		if k == n-1 {
			ceil[k], safe[k], bindLimit[k], bindHop[k] = own, own, limit, k
			continue
		}
		den := SettleDenominator(legs[k].FeePPM, legs[k].ProtocolPPM)
		gHi, ok := fullMulDiv(&ceil[k+1], ppmSq, &den)
		if !ok {
			gHi = new(uint256.Int).Set(maxU256)
		}
		byNext := legs[k].maxInputFor(gHi, &own)
		if byNext.Lt(&own) {
			ceil[k], bindLimit[k], bindHop[k] = byNext, bindLimit[k+1], bindHop[k+1]
		} else {
			ceil[k], bindLimit[k], bindHop[k] = own, limit, k
		}
		gSafe := new(uint256.Int)
		if safe[k+1].Gt(three) {
			s3 := new(uint256.Int).Sub(&safe[k+1], three)
			if z, ok := fullMulDiv(s3, ppmSq, &den); ok {
				gSafe = z
			} else {
				gSafe.Set(maxU256)
			}
		}
		safe[k] = legs[k].maxInputFor(gSafe, &own)
	}

	first := &legs[0]
	k1 := SettleDenominator(first.FeePPM, first.ProtocolPPM)
	gHi, ok := fullMulDiv(&ceil[1], ppmSq, &k1)
	if !ok {
		gHi = new(uint256.Int).Set(maxU256)
	}
	gLo := new(uint256.Int)
	if safe[1].Gt(three) {
		s3 := new(uint256.Int).Sub(&safe[1], three)
		if z, ok := fullMulDiv(s3, ppmSq, &k1); ok {
			gLo = z
		} else {
			gLo.Set(maxU256)
		}
	}

	x := first.maxInputFor(gHi, &u)
	hop := 0
	if x.Lt(&u) {
		limitedBy, hop = bindLimit[1], bindHop[1]
	}
	lastRefusal, lastLimit, lastHop := capacityReason(bindLimit[1]), bindLimit[1], bindHop[1]

	for range budget {
		if x.IsZero() {
			// No positive input is left: those above the ceiling are refused
			// downstream, and every plateau below it was refused on capacity.
			return MaxResult{Unavailable: lastRefusal, LimitedBy: lastLimit, LimitedByHop: lastHop}
		}
		quote, err := SwapRoute(legs, &x, wallet)
		if err == nil {
			return MaxResult{
				Known: true, Exact: true, AmountIn: x, AmountOut: quote.AmountOut(),
				LimitedBy: limitedBy, LimitedByHop: hop,
			}
		}
		var qe *QuoteError
		if !errors.As(err, &qe) {
			return MaxResult{}
		}
		if qe.Hop == 0 {
			// x <= U, so hop one can only refuse on rounding: no smaller input
			// helps.
			return MaxResult{Unavailable: qe.Reason}
		}
		if qe.Reason != ReasonDepositCap && qe.Reason != ReasonLiquidity {
			// A later hop refused something other than capacity: its output
			// rounds to nothing at dust-sized amounts. Smaller inputs only
			// shrink what reaches it, so the maximum is unknown rather than
			// disproved.
			return MaxResult{}
		}
		lastRefusal, lastHop = qe.Reason, qe.Hop
		lastLimit = LimitDepositCap
		if qe.Reason == ReasonLiquidity {
			lastLimit = LimitLiquidity
		}
		g, gerr := first.Gross(&x)
		if gerr != nil || g.IsZero() {
			return MaxResult{}
		}
		if !g.Gt(gLo) {
			// At or below the proven floor every later hop accepts on
			// capacity; reaching here means the bound was violated.
			return MaxResult{}
		}
		limitedBy, hop = ownLimit[qe.Hop], qe.Hop
		below := new(uint256.Int).Sub(&g, one)
		x = first.maxInputFor(below, &x)
	}

	// Budget spent: offer the floor candidate, which every later hop must
	// accept on capacity, without claiming it is the maximum.
	floorX := first.maxInputFor(gLo, &u)
	if floorX.IsZero() {
		return MaxResult{}
	}
	quote, err := SwapRoute(legs, &floorX, wallet)
	if err != nil {
		return MaxResult{}
	}
	return MaxResult{
		Known: true, Exact: false, AmountIn: floorX, AmountOut: quote.AmountOut(),
		LimitedBy: bindLimit[1], LimitedByHop: bindHop[1],
	}
}

func reasonOf(err error) Reason {
	var qe *QuoteError
	if errors.As(err, &qe) {
		return qe.Reason
	}
	return ReasonOverflow
}
