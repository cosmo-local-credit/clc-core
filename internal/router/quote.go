// Package router finds and prices multi-hop voucher exchanges across
// SwapPools.
//
// It has two tiers. The graph tier (graph.go, engine.go) answers discovery
// questions from an in-memory snapshot with no RPC on the request path; its
// answers are estimates as of a block. The quote tier (service.go) reads the
// pools at a pinned block and reproduces the contract arithmetic exactly; only
// it is authoritative.
//
// The package does no HTTP and no SQL. Persistence goes through StateStore,
// chain access through ChainReader.
package router

import (
	"errors"
	"fmt"

	"github.com/holiman/uint256"
)

const (
	// PPM is the contracts' fee and price-index denominator.
	PPM = 1_000_000
	// floorFeePPM is SwapPool.DEFAULT_FEE_PPM: the protocol fee is charged on
	// at least 1 % of the gross, whatever the pool's own fee.
	floorFeePPM = 10_000

	// QuoterUnsupported covers oracle and decimal quoters, and anything not
	// yet verified. Such pools are excluded from routing, not approximated.
	QuoterUnsupported QuoterKind = 0
	// QuoterRelative is RelativeQuoter: value * index(in) / index(out),
	// scaled for decimals.
	QuoterRelative QuoterKind = 1
	// QuoterNone is a pool with no quoter: SwapPool.getQuote returns the
	// input unchanged.
	QuoterNone QuoterKind = 2

	ReasonSameToken         Reason = "same_token"
	ReasonNotAllowedIn      Reason = "not_allowed_in"
	ReasonNotAllowedOut     Reason = "not_allowed_out"
	ReasonDepositCap        Reason = "deposit_cap"
	ReasonLiquidity         Reason = "liquidity"
	ReasonFeeDomain         Reason = "fee_domain"
	ReasonZeroInput         Reason = "zero_input"
	ReasonZeroOutput        Reason = "zero_output"
	ReasonOverflow          Reason = "overflow"
	ReasonUnsupportedQuoter Reason = "unsupported_quoter"
	ReasonUnsupportedToken  Reason = "unsupported_token"
	ReasonWalletBalance     Reason = "wallet_balance"
	ReasonRetired           Reason = "retired"
	ReasonSearchBudget      Reason = "search_budget"
)

var (
	ppm      = uint256.NewInt(PPM)
	ppmSq    = uint256.NewInt(PPM * PPM)
	floorFee = uint256.NewInt(floorFeePPM)
	maxU256  = new(uint256.Int).SetAllOne()
)

type (
	// QuoterKind is how a pool turns an input amount into a gross output.
	QuoterKind uint8

	// Reason says why a hop or a route cannot carry an amount. The values are part
	// of the API.
	Reason string

	// QuoteError is a typed refusal. Hop is zero-based; -1 means the route as a
	// whole.
	QuoteError struct {
		Hop    int
		Reason Reason
	}

	// LegParams is everything one SwapPool.withdraw(out, in, value, ...) depends
	// on. Amounts are raw token units.
	LegParams struct {
		Quoter        QuoterKind
		FeePPM        uint64 // the pair fee FeePolicy.getFee(in, out) returns
		ProtocolPPM   uint64 // 0 when the pool charges no protocol fee
		HasRegistry   bool
		HasLimiter    bool
		FeesDecoupled bool

		InAllowed  bool
		InDecimals uint8
		InIndex    uint256.Int // RelativeQuoter.priceIndex; 0 means parity
		InLimit    uint256.Int
		InBalance  uint256.Int

		OutAllowed  bool
		OutDecimals uint8
		OutIndex    uint256.Int
		OutBalance  uint256.Int
		OutFeesHeld uint256.Int
	}

	// HopQuote is one leg's settlement, itemized the way SwapSettlement reports
	// it.
	HopQuote struct {
		AmountIn    uint256.Int
		Gross       uint256.Int
		PoolFee     uint256.Int
		ProtocolFee uint256.Int
		Net         uint256.Int
	}

	// RouteQuote is a chained quote: hop i spends Inputs[i], and every hop but the
	// last delivers at least the next hop's input to the wallet.
	RouteQuote struct {
		Hops []HopQuote
		// Inputs[i] is what hop i spends. For exact input Inputs[i+1] equals
		// Hops[i].Net; for exact output it is the next hop's minimum input and
		// Hops[i].Net may exceed it.
		Inputs []uint256.Int
	}
)

func (k QuoterKind) String() string {
	switch k {
	case QuoterRelative:
		return "relative"
	case QuoterNone:
		return "none"
	default:
		return "unsupported"
	}
}

func (e *QuoteError) Error() string {
	if e.Hop < 0 {
		return fmt.Sprintf("route: %s", e.Reason)
	}
	return fmt.Sprintf("hop %d: %s", e.Hop, e.Reason)
}

func refuse(hop int, reason Reason) error { return &QuoteError{Hop: hop, Reason: reason} }

func effectiveIndex(index *uint256.Int) *uint256.Int {
	if index.IsZero() {
		return ppm
	}
	return index
}

func pow10(n uint8) (*uint256.Int, bool) {
	z := uint256.NewInt(1)
	ten := uint256.NewInt(10)
	for range n {
		if _, overflow := z.MulOverflow(z, ten); overflow {
			return nil, false
		}
	}
	return z, true
}

// fullMulDiv is solady's FixedPointMathLib.fullMulDiv: floor(x*y/d) with a
// 512-bit intermediate, reverting when the result does not fit.
func fullMulDiv(x, y, d *uint256.Int) (*uint256.Int, bool) {
	if d.IsZero() {
		return nil, false
	}
	z, overflow := new(uint256.Int).MulDivOverflow(x, y, d)
	return z, !overflow
}

// fullMulDivUp is fullMulDiv rounded up.
func fullMulDivUp(x, y, d *uint256.Int) (*uint256.Int, bool) {
	z, ok := fullMulDiv(x, y, d)
	if !ok {
		return nil, false
	}
	if !new(uint256.Int).MulMod(x, y, d).IsZero() {
		if _, overflow := z.AddOverflow(z, uint256.NewInt(1)); overflow {
			return nil, false
		}
	}
	return z, true
}

// Gross is SwapPool.getQuote(out, in, value): what the quoter values the
// input at, before any fee. It is monotone non-decreasing in value.
func (p *LegParams) Gross(value *uint256.Int) (uint256.Int, error) {
	switch p.Quoter {
	case QuoterNone:
		return *value, nil
	case QuoterRelative:
	default:
		return uint256.Int{}, refuse(0, ReasonUnsupportedQuoter)
	}

	in := effectiveIndex(&p.InIndex)
	out := effectiveIndex(&p.OutIndex)

	if p.InDecimals == p.OutDecimals {
		// (inputValue * inExchangeRate) / outExchangeRate, checked.
		product, overflow := new(uint256.Int).MulOverflow(value, in)
		if overflow {
			return uint256.Int{}, refuse(0, ReasonOverflow)
		}
		return *product.Div(product, out), nil
	}

	var gap uint8
	if p.InDecimals > p.OutDecimals {
		gap = p.InDecimals - p.OutDecimals
	} else {
		gap = p.OutDecimals - p.InDecimals
	}
	d, ok := pow10(gap)
	if !ok {
		return uint256.Int{}, refuse(0, ReasonOverflow)
	}

	if p.InDecimals > p.OutDecimals {
		// fullMulDiv(value, in, out) / d
		q, ok := fullMulDiv(value, in, out)
		if !ok {
			return uint256.Int{}, refuse(0, ReasonOverflow)
		}
		return *q.Div(q, d), nil
	}

	// _fullMulDivScaled(value, in, d, out):
	// fullMulDiv(value, in, out) * d + fullMulDiv(mulmod(value, in, out), d, out)
	q, ok := fullMulDiv(value, in, out)
	if !ok {
		return uint256.Int{}, refuse(0, ReasonOverflow)
	}
	remainder := new(uint256.Int).MulMod(value, in, out)
	tail, ok := fullMulDiv(remainder, d, out)
	if !ok {
		return uint256.Int{}, refuse(0, ReasonOverflow)
	}
	scaled, overflow := new(uint256.Int).MulOverflow(q, d)
	if overflow {
		return uint256.Int{}, refuse(0, ReasonOverflow)
	}
	if _, overflow := scaled.AddOverflow(scaled, tail); overflow {
		return uint256.Int{}, refuse(0, ReasonOverflow)
	}
	return *scaled, nil
}

// FeeDomainOK is SwapPool._mustFeeDomainPpm.
func FeeDomainOK(feePPM, protocolPPM uint64) bool {
	if feePPM < floorFeePPM {
		return true
	}
	lhs := new(uint256.Int).Mul(uint256.NewInt(feePPM), uint256.NewInt(PPM+protocolPPM))
	return lhs.Lt(ppmSq)
}

// Settle applies the pool fee and the floored protocol fee to a gross amount,
// as SwapPool._swap and getAmountOut do. It refuses exactly where
// _netAfterFees reverts.
func Settle(gross *uint256.Int, feePPM, protocolPPM uint64) (poolFee, protocolFee, net uint256.Int, err error) {
	// getFee: (_value * feePpm) / PPM, checked.
	product, overflow := new(uint256.Int).MulOverflow(gross, uint256.NewInt(feePPM))
	if overflow {
		return poolFee, protocolFee, net, refuse(0, ReasonOverflow)
	}
	poolFee = *product.Div(product, ppm)

	if protocolPPM > 0 {
		// assumedFee = (quotedValue * DEFAULT_FEE_PPM) / PPM, checked.
		assumed, overflow := new(uint256.Int).MulOverflow(gross, floorFee)
		if overflow {
			return poolFee, protocolFee, net, refuse(0, ReasonOverflow)
		}
		assumed.Div(assumed, ppm)
		base := assumed
		if poolFee.Cmp(assumed) >= 0 {
			base = new(uint256.Int).Set(&poolFee)
		}
		fee, overflow := new(uint256.Int).MulOverflow(base, uint256.NewInt(protocolPPM))
		if overflow {
			return poolFee, protocolFee, net, refuse(0, ReasonOverflow)
		}
		protocolFee = *fee.Div(fee, ppm)
	}

	total, overflow := new(uint256.Int).AddOverflow(&poolFee, &protocolFee)
	if overflow || total.Gt(gross) {
		return poolFee, protocolFee, net, refuse(0, ReasonFeeDomain)
	}
	net = *new(uint256.Int).Sub(gross, total)
	if net.IsZero() {
		return poolFee, protocolFee, net, refuse(0, ReasonZeroOutput)
	}
	return poolFee, protocolFee, net, nil
}

// Headroom is how much more of the input the pool may take before its
// limiter refuses; the maximum uint256 when it has no limiter.
func (p *LegParams) Headroom() uint256.Int {
	if !p.HasLimiter {
		return *maxU256
	}
	if p.InLimit.Gt(&p.InBalance) {
		return *new(uint256.Int).Sub(&p.InLimit, &p.InBalance)
	}
	return uint256.Int{}
}

// Available is the output the pool can pay out: its balance, less held fees
// when fees are decoupled from liquidity.
func (p *LegParams) Available() uint256.Int {
	if !p.FeesDecoupled {
		return p.OutBalance
	}
	if p.OutBalance.Gt(&p.OutFeesHeld) {
		return *new(uint256.Int).Sub(&p.OutBalance, &p.OutFeesHeld)
	}
	return uint256.Int{}
}

// Arithmetic is SwapPool.getAmountOut: fee domain, quote and fees, with no
// membership or capacity checks. The authoritative quote compares it against
// the chain.
func (p *LegParams) Arithmetic(amountIn *uint256.Int) (HopQuote, error) {
	if !FeeDomainOK(p.FeePPM, p.ProtocolPPM) {
		return HopQuote{}, refuse(0, ReasonFeeDomain)
	}
	gross, err := p.Gross(amountIn)
	if err != nil {
		return HopQuote{}, err
	}
	poolFee, protocolFee, net, err := Settle(&gross, p.FeePPM, p.ProtocolPPM)
	if err != nil {
		return HopQuote{}, err
	}
	return HopQuote{AmountIn: *amountIn, Gross: gross, PoolFee: poolFee, ProtocolFee: protocolFee, Net: net}, nil
}

// Swap reproduces SwapPool._swap for one leg, in the contract's order of
// checks, for a trader whose transfers move exactly the stated amount.
func (p *LegParams) Swap(amountIn *uint256.Int) (HopQuote, error) {
	if p.HasRegistry && !p.OutAllowed {
		return HopQuote{}, refuse(0, ReasonNotAllowedOut)
	}
	if !FeeDomainOK(p.FeePPM, p.ProtocolPPM) {
		return HopQuote{}, refuse(0, ReasonFeeDomain)
	}
	if p.HasRegistry && !p.InAllowed {
		return HopQuote{}, refuse(0, ReasonNotAllowedIn)
	}
	if p.HasLimiter {
		total, overflow := new(uint256.Int).AddOverflow(&p.InBalance, amountIn)
		if overflow || total.Gt(&p.InLimit) {
			return HopQuote{}, refuse(0, ReasonDepositCap)
		}
	}
	if amountIn.IsZero() {
		return HopQuote{}, refuse(0, ReasonZeroInput)
	}
	gross, err := p.Gross(amountIn)
	if err != nil {
		return HopQuote{}, err
	}
	available := p.Available()
	if available.Lt(&gross) {
		return HopQuote{}, refuse(0, ReasonLiquidity)
	}
	poolFee, protocolFee, net, err := Settle(&gross, p.FeePPM, p.ProtocolPPM)
	if err != nil {
		return HopQuote{}, err
	}
	return HopQuote{AmountIn: *amountIn, Gross: gross, PoolFee: poolFee, ProtocolFee: protocolFee, Net: net}, nil
}

// SettleDenominator is K = D² − f·D − max(f, 1 %)·p, the denominator
// SwapPool._reverseNetToQuoted uses. For every gross g:
//
//	g·K/D² <= net(g) < g·K/D² + 3
//
// The lower bound holds because each floor only reduces a fee; the upper
// because two floors each lose less than one unit and the floored base loses at
// most p/D <= 1 more. K is positive whenever the fee domain holds.
func SettleDenominator(feePPM, protocolPPM uint64) uint256.Int {
	base := max(feePPM, floorFeePPM)
	if protocolPPM == 0 {
		base = 0
	}
	k := new(uint256.Int).Set(ppmSq)
	k.Sub(k, new(uint256.Int).Mul(uint256.NewInt(feePPM), ppm))
	k.Sub(k, new(uint256.Int).Mul(uint256.NewInt(base), uint256.NewInt(protocolPPM)))
	return *k
}

// AmountIn is the first hop's input.
func (q RouteQuote) AmountIn() uint256.Int { return q.Inputs[0] }

// AmountOut is what the last hop delivers.
func (q RouteQuote) AmountOut() uint256.Int { return q.Hops[len(q.Hops)-1].Net }

// BridgeSurplus is, per intermediate hop, what that hop delivered beyond what
// the next hop spends. It stays in the wallet.
func (q RouteQuote) BridgeSurplus() []uint256.Int {
	out := make([]uint256.Int, 0, len(q.Hops)-1)
	for i := 0; i+1 < len(q.Hops); i++ {
		out = append(out, *new(uint256.Int).Sub(&q.Hops[i].Net, &q.Inputs[i+1]))
	}
	return out
}

// FeeFree is the output the route would deliver with every fee removed:
// gross_n(...gross_1(amountIn)).
func FeeFree(legs []LegParams, amountIn *uint256.Int) (uint256.Int, error) {
	value := *amountIn
	for i := range legs {
		gross, err := legs[i].Gross(&value)
		if err != nil {
			return uint256.Int{}, err
		}
		value = gross
	}
	return value, nil
}

// EffectiveFeePPM is (feeFree − net) * 1e6 / feeFree, rounded down; zero when
// feeFree is zero.
func EffectiveFeePPM(feeFree, net *uint256.Int) uint256.Int {
	if feeFree.IsZero() || !net.Lt(feeFree) {
		return uint256.Int{}
	}
	diff := new(uint256.Int).Sub(feeFree, net)
	z, ok := fullMulDiv(diff, ppm, feeFree)
	if !ok {
		return uint256.Int{}
	}
	return *z
}

// SwapRoute quotes exact input through every leg, each hop spending what the
// previous one delivered. wallet bounds the first input; nil skips the check.
func SwapRoute(legs []LegParams, amountIn *uint256.Int, wallet *uint256.Int) (RouteQuote, error) {
	inputs := make([]uint256.Int, len(legs))
	inputs[0] = *amountIn
	for i := 1; i < len(legs); i++ {
		inputs[i] = uint256.Int{} // filled as the chain advances
	}
	return chain(legs, inputs, wallet, true)
}

// SwapChained evaluates a batch whose hop inputs are fixed in advance, as the
// exact-output batch is: hop i spends inputs[i] and must deliver at least
// inputs[i+1].
func SwapChained(legs []LegParams, inputs []uint256.Int, wallet *uint256.Int) (RouteQuote, error) {
	return chain(legs, inputs, wallet, false)
}

func chain(legs []LegParams, inputs []uint256.Int, wallet *uint256.Int, follow bool) (RouteQuote, error) {
	if len(legs) == 0 || len(inputs) != len(legs) {
		return RouteQuote{}, refuse(-1, ReasonZeroInput)
	}
	if wallet != nil && inputs[0].Gt(wallet) {
		return RouteQuote{}, refuse(0, ReasonWalletBalance)
	}
	hops := make([]HopQuote, len(legs))
	for i := range legs {
		hop, err := legs[i].Swap(&inputs[i])
		if err != nil {
			return RouteQuote{}, withHop(err, i)
		}
		hops[i] = hop
		if i+1 < len(legs) {
			if follow {
				inputs[i+1] = hop.Net
			} else if hop.Net.Lt(&inputs[i+1]) {
				// The intermediate bounded withdraw's minAmountOut is the next
				// hop's input.
				return RouteQuote{}, refuse(i, ReasonZeroOutput)
			}
		}
	}
	return RouteQuote{Hops: hops, Inputs: inputs}, nil
}

func withHop(err error, hop int) error {
	var qe *QuoteError
	if errors.As(err, &qe) {
		return &QuoteError{Hop: hop, Reason: qe.Reason}
	}
	return err
}
