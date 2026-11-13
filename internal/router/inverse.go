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
// monotone, so this is a binary search.
func (p *LegParams) maxInputFor(g, upTo *uint256.Int) uint256.Int {
	lo, hi := uint256.Int{}, *upTo
	if gross := p.grossOrMax(&hi); !gross.Gt(g) {
		return hi
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
// two hops it is not: hop two consumes hop one's net, and net is not monotone
// in gross. The search then walks hop one's producible gross plateaus down
// from a proven ceiling (see the inline proof), so Exact means every larger
// input was ruled out. Routes longer than two hops get Known: false.
func MaxInput(legs []LegParams, wallet *uint256.Int, budget int) MaxResult {
	for i := range legs {
		if !FeeDomainOK(legs[i].FeePPM, legs[i].ProtocolPPM) {
			return MaxResult{Unavailable: ReasonFeeDomain, LimitedByHop: i}
		}
		if legs[i].Quoter != QuoterRelative && legs[i].Quoter != QuoterNone {
			return MaxResult{Unavailable: ReasonUnsupportedQuoter, LimitedByHop: i}
		}
	}

	// U: the largest input hop one accepts on its own constraints.
	u, limitedBy := legs[0].legCeiling()
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

	switch len(legs) {
	case 1:
		quote, err := SwapRoute(legs, &u, wallet)
		if err != nil {
			// Every constraint below U is monotone, so the only failure left
			// at U is the output rounding to nothing, which no smaller input
			// fixes.
			return MaxResult{Unavailable: reasonOf(err)}
		}
		return MaxResult{Known: true, Exact: true, AmountIn: u, AmountOut: quote.AmountOut(), LimitedBy: limitedBy}
	case 2:
		return maxTwoHops(legs, u, limitedBy, wallet, budget)
	default:
		return MaxResult{}
	}
}

func maxTwoHops(legs []LegParams, u uint256.Int, limitedBy string, wallet *uint256.Int, budget int) MaxResult {
	first, second := &legs[0], &legs[1]

	// Bmax: the largest intermediate amount hop two accepts. Monotone.
	bmax, bridgeLimit := second.legCeiling()
	if bmax.IsZero() {
		reason := ReasonDepositCap
		if bridgeLimit == LimitLiquidity {
			reason = ReasonLiquidity
		}
		return MaxResult{Unavailable: reason, LimitedBy: bridgeLimit, LimitedByHop: 1}
	}

	k1 := SettleDenominator(first.FeePPM, first.ProtocolPPM)
	// Proven ceiling: any hop-one gross above floor(Bmax·D²/K1) nets more
	// than Bmax, by the lower bound of SettleDenominator.
	gHi, ok := fullMulDiv(&bmax, ppmSq, &k1)
	if !ok {
		gHi = new(uint256.Int).Set(maxU256)
	}
	// Proven floor: any gross at or below floor((Bmax − 3)·D²/K1) nets no
	// more than Bmax, by the upper bound.
	gLo := new(uint256.Int)
	if bmax.Gt(uint256.NewInt(3)) {
		b3 := new(uint256.Int).Sub(&bmax, uint256.NewInt(3))
		if z, ok := fullMulDiv(b3, ppmSq, &k1); ok {
			gLo = z
		} else {
			gLo.Set(maxU256)
		}
	}

	// Candidates are the gross values hop one can actually produce. Start at
	// the top plateau under the ceiling, and on a refusal by hop two step to
	// the top of the next lower plateau: every input in between has the same
	// gross, so the same net, so the same outcome at hop two.
	x := first.maxInputFor(gHi, &u)
	hop := 0
	if x.Lt(&u) {
		limitedBy, hop = bridgeLimit, 1
	}
	lastRefusal := ReasonDepositCap
	if bridgeLimit == LimitLiquidity {
		lastRefusal = ReasonLiquidity
	}

	for range budget {
		if x.IsZero() {
			// No positive input is left: those above the ceiling net more
			// than hop two accepts, and every plateau below it was refused by
			// hop two's capacity.
			return MaxResult{Unavailable: lastRefusal, LimitedBy: bridgeLimit, LimitedByHop: 1}
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
			// Hop two refused something other than capacity: its output
			// rounds to nothing at dust-sized amounts. Smaller inputs only
			// shrink what reaches it, so the maximum is unknown rather than
			// disproved.
			return MaxResult{}
		}
		lastRefusal = qe.Reason
		g, gerr := first.Gross(&x)
		if gerr != nil || g.IsZero() {
			return MaxResult{}
		}
		if !g.Gt(gLo) {
			// Below the proven floor hop one nets at most Bmax, which hop two
			// accepts on capacity; reaching here means the bound was violated.
			return MaxResult{}
		}
		limitedBy, hop = bridgeLimit, 1
		below := new(uint256.Int).Sub(&g, one)
		x = first.maxInputFor(below, &x)
	}

	// Budget spent: offer the floor candidate, which hop two must accept on
	// capacity, without claiming it is the maximum.
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
		LimitedBy: bridgeLimit, LimitedByHop: 1,
	}
}

func reasonOf(err error) Reason {
	var qe *QuoteError
	if errors.As(err, &qe) {
		return qe.Reason
	}
	return ReasonOverflow
}
