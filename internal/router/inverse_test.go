package router

import (
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// feasible is the definition the engine must match: the forward quote
// succeeds.
func feasible(legs []LegParams, x uint64, wallet *uint256.Int) bool {
	amount := u(x)
	_, err := SwapRoute(legs, &amount, wallet)
	return err == nil
}

func bruteMax(legs []LegParams, upTo uint64, wallet *uint256.Int) (uint64, bool) {
	for x := upTo; x > 0; x-- {
		if feasible(legs, x, wallet) {
			return x, true
		}
	}
	return 0, false
}

// bruteMinChained is the smallest first input of a chained exact-output
// batch: hop two spends its own minimum, which hop one must deliver.
func bruteMinChained(legs []LegParams, target uint64, upTo uint64) (uint64, bool) {
	need := target
	for _, leg := range slices.Backward(legs) {
		found := false
		for x := uint64(1); x <= upTo; x++ {
			amount := u(x)
			hop, err := leg.Arithmetic(&amount)
			if err == nil && hop.Net.Uint64() >= need {
				need, found = x, true
				break
			}
		}
		if !found {
			return 0, false
		}
	}
	return need, true
}

func capped(leg LegParams, headroom, liquidity uint64) LegParams {
	leg.HasLimiter = true
	leg.InBalance = u(1_000)
	leg.InLimit = u(1_000 + headroom)
	leg.OutBalance = u(liquidity)
	return leg
}

func TestMaxInputSawtoothCase(t *testing.T) {
	// Net is not monotone in gross. Hop one: 1 % fee, 10 % protocol cut, so grosses 998
	// and 1000 net 989 while 999 nets 990. Hop two takes at most 989. A
	// search that settles on 998 and scans down never finds 1000.
	first := capped(relative(6, 6, 0, 0, 10_000, 100_000), 100_000, 100_000)
	second := capped(relative(6, 6, 0, 0, 0, 0), 989, 100_000)
	legs := []LegParams{first, second}
	wallet := u(1_000)

	want, ok := bruteMax(legs, 1_000, &wallet)
	require.True(t, ok)
	require.Equal(t, uint64(1_000), want, "the brute-force maximum")
	assert.False(t, feasible(legs, 999, &wallet), "the plateau below is infeasible")

	got := MaxInput(legs, &wallet, DefaultSearchBudget)
	require.True(t, got.Known)
	assert.True(t, got.Exact)
	assert.Equal(t, want, got.AmountIn.Uint64())
}

func TestMaxInputMatchesBruteForce(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	fees := []uint64{0, 3_000, 10_000, 30_000, 100_000, 333_333}
	protos := []uint64{0, 20_000, 100_000, 500_000, PPM}
	type shape struct{ din, dout, bridge uint8 }
	shapes := []shape{{6, 6, 6}, {6, 6, 2}, {2, 6, 6}, {6, 2, 6}, {6, 6, 0}, {0, 6, 2}}

	exact, unknown, unavailable := 0, 0, 0
	for i := range 4_000 {
		s := shapes[rng.IntN(len(shapes))]
		first := relative(s.din, s.bridge, 1+rng.Uint64N(3*PPM), 1+rng.Uint64N(3*PPM),
			fees[rng.IntN(len(fees))], protos[rng.IntN(len(protos))])
		second := relative(s.bridge, s.dout, 1+rng.Uint64N(3*PPM), 1+rng.Uint64N(3*PPM),
			fees[rng.IntN(len(fees))], protos[rng.IntN(len(protos))])
		if !FeeDomainOK(first.FeePPM, first.ProtocolPPM) || !FeeDomainOK(second.FeePPM, second.ProtocolPPM) {
			continue
		}
		first = capped(first, rng.Uint64N(3_000), rng.Uint64N(3_000))
		second = capped(second, rng.Uint64N(1_500), rng.Uint64N(1_500))
		legs := []LegParams{first, second}
		if rng.IntN(3) == 0 {
			legs = legs[:1]
		}
		wallet := u(rng.Uint64N(3_000))

		want, ok := bruteMax(legs, 3_000, &wallet)
		got := MaxInput(legs, &wallet, DefaultSearchBudget)
		switch {
		case got.Unavailable != "":
			unavailable++
			require.False(t, ok, "case %d: route declared unavailable but %d is feasible (%s)", i, want, got.Unavailable)
		case !got.Known:
			unknown++
		default:
			require.True(t, ok, "case %d: engine found %s, brute force found nothing", i, got.AmountIn.Dec())
			require.True(t, feasible(legs, got.AmountIn.Uint64(), &wallet), "case %d: returned maximum is infeasible", i)
			if got.Exact {
				exact++
				require.Equal(t, want, got.AmountIn.Uint64(), "case %d: exact maximum", i)
			} else {
				require.LessOrEqual(t, got.AmountIn.Uint64(), want)
			}
			out := got.AmountIn
			quote, err := SwapRoute(legs, &out, &wallet)
			require.NoError(t, err)
			assert.Equal(t, quote.AmountOut(), got.AmountOut)
		}
	}
	t.Logf("exact %d, unknown %d, unavailable %d", exact, unknown, unavailable)
	assert.Greater(t, exact, 1_000)
}

func TestMaxInputBudgetExhaustionStaysFeasible(t *testing.T) {
	// Punitive first-hop fees leave many plateaus between the proven floor
	// and ceiling. With a budget of one, any case whose top candidate hop two
	// refuses exhausts it; the result must still be feasible and must never
	// mark the route unavailable.
	wallet := u(600)
	exhausted := 0
	for _, fee := range []uint64{10_000, 100_000, 500_000, 900_000} {
		for _, proto := range []uint64{20_000, 100_000, 999_999} {
			if !FeeDomainOK(fee, proto) {
				continue
			}
			for bridge := uint64(1); bridge <= 30; bridge++ {
				first := capped(relative(6, 6, 0, 0, fee, proto), 1_000_000, 1_000_000)
				second := capped(relative(6, 6, 0, 0, 0, 0), bridge, 1_000_000)
				legs := []LegParams{first, second}
				want, ok := bruteMax(legs, 600, &wallet)

				got := MaxInput(legs, &wallet, 1)
				require.Empty(t, got.Unavailable, "fee %d proto %d bridge %d", fee, proto, bridge)
				if got.Known {
					require.True(t, ok)
					require.True(t, feasible(legs, got.AmountIn.Uint64(), &wallet))
					if got.Exact {
						require.Equal(t, want, got.AmountIn.Uint64())
					} else {
						exhausted++
						require.LessOrEqual(t, got.AmountIn.Uint64(), want)
					}
				}

				full := MaxInput(legs, &wallet, DefaultSearchBudget)
				require.True(t, ok)
				require.True(t, full.Known && full.Exact, "fee %d proto %d bridge %d", fee, proto, bridge)
				require.Equal(t, want, full.AmountIn.Uint64(), "fee %d proto %d bridge %d", fee, proto, bridge)
			}
		}
	}
	t.Logf("exhausted %d", exhausted)
	assert.Positive(t, exhausted, "some case must actually exhaust the budget")
}

func TestMaxInputProvesUnavailable(t *testing.T) {
	wallet := u(1_000)
	full := capped(relative(6, 6, 0, 0, 0, 0), 0, 1_000)
	got := MaxInput([]LegParams{full}, &wallet, DefaultSearchBudget)
	assert.Equal(t, ReasonDepositCap, got.Unavailable)

	dry := capped(relative(6, 6, 0, 0, 0, 0), 1_000, 1_000)
	empty := capped(relative(6, 6, 0, 0, 0, 0), 1_000, 0)
	got = MaxInput([]LegParams{dry, empty}, &wallet, DefaultSearchBudget)
	assert.Equal(t, ReasonLiquidity, got.Unavailable)
	assert.Equal(t, 1, got.LimitedByHop)

	zero := u(0)
	got = MaxInput([]LegParams{dry}, &zero, DefaultSearchBudget)
	assert.Equal(t, ReasonWalletBalance, got.Unavailable)
}

func TestMaxInputDecimalScales(t *testing.T) {
	// 6 -> 18 jumps by 1e12 per unit; 18 -> 6 stays flat across 1e12 inputs.
	up := relative(6, 18, 0, 0, 0, 20_000)
	up.HasLimiter, up.InLimit = true, u(5_000)
	up.OutBalance = ud("3000000000000000") // 3,000 units of 18-decimal
	down := relative(18, 6, 0, 0, 0, 20_000)
	down.HasLimiter, down.InLimit = true, ud("2500000000000000")
	down.OutBalance = u(1_000_000)
	wallet := u(5_000)
	legs := []LegParams{up, down}

	got := MaxInput(legs, &wallet, DefaultSearchBudget)
	require.True(t, got.Known)
	require.True(t, got.Exact)
	x := got.AmountIn.Uint64()
	assert.True(t, feasible(legs, x, &wallet))
	assert.False(t, feasible(legs, x+1, &wallet), "one more unit must be refused")
	// Every larger input up to the wallet is infeasible.
	for y := x + 1; y <= 5_000; y++ {
		require.False(t, feasible(legs, y, &wallet), "input %d", y)
	}
}

func TestExactOutputMatchesBruteForce(t *testing.T) {
	rng := rand.New(rand.NewPCG(13, 17))
	fees := []uint64{0, 10_000, 30_000, 250_000}
	protos := []uint64{0, 20_000, 100_000, PPM}
	checked := 0
	for i := range 2_000 {
		first := relative(6, 6, 1+rng.Uint64N(2*PPM), 1+rng.Uint64N(2*PPM), fees[rng.IntN(4)], protos[rng.IntN(4)])
		second := relative(6, 6, 1+rng.Uint64N(2*PPM), 1+rng.Uint64N(2*PPM), fees[rng.IntN(4)], protos[rng.IntN(4)])
		if !FeeDomainOK(first.FeePPM, first.ProtocolPPM) || !FeeDomainOK(second.FeePPM, second.ProtocolPPM) {
			continue
		}
		first.OutBalance, second.OutBalance = u(1<<40), u(1<<40)
		legs := []LegParams{first, second}
		if rng.IntN(3) == 0 {
			legs = legs[:1]
		}
		target := 1 + rng.Uint64N(800)
		const upTo = 20_000
		want, ok := bruteMinChained(legs, target, upTo)
		tgt := u(target)
		quote, err := QuoteExactOutput(legs, &tgt, nil, DefaultSearchBudget)
		if !ok {
			// Brute force only looked so far: the engine may only succeed
			// beyond it.
			if err == nil {
				beyond := false
				for _, in := range quote.Inputs {
					beyond = beyond || in.Gt(uint256.NewInt(upTo))
				}
				require.True(t, beyond, "case %d: engine found a route inside the brute-force window", i)
			}
			continue
		}
		require.NoError(t, err, "case %d target %d", i, target)
		checked++
		in := quote.AmountIn()
		require.Equal(t, want, in.Uint64(), "case %d: smallest input", i)
		out := quote.AmountOut()
		require.False(t, out.Lt(&tgt), "case %d: never delivers less than requested", i)
		for h, surplus := range quote.BridgeSurplus() {
			assert.False(t, surplus.Gt(&quote.Hops[h].Net))
		}

		// The contract's getAmountIn, chained, is an upper bound.
		bound := tgt
		for _, leg := range slices.Backward(legs) {
			bound = contractAmountIn(leg, bound)
		}
		require.False(t, in.Gt(&bound), "case %d: engine input %s exceeds getAmountIn %s", i, in.Dec(), bound.Dec())
	}
	assert.Greater(t, checked, 500)
}

// contractAmountIn transcribes SwapPool.getAmountIn for a same-decimals
// relative quoter: reverse the fees with ceiling division, reverse the quoter
// with ceiling division, add one.
func contractAmountIn(p LegParams, out uint256.Int) uint256.Int {
	quoted := out
	if p.FeePPM != 0 || p.ProtocolPPM != 0 {
		var denominator uint256.Int
		if p.FeePPM >= floorFeePPM {
			denominator = *new(uint256.Int).Sub(ppmSq, new(uint256.Int).Mul(uint256.NewInt(p.FeePPM), uint256.NewInt(PPM+p.ProtocolPPM)))
		} else {
			denominator = *new(uint256.Int).Sub(ppmSq, new(uint256.Int).Mul(uint256.NewInt(p.FeePPM), ppm))
			denominator.Sub(&denominator, new(uint256.Int).Mul(floorFee, uint256.NewInt(p.ProtocolPPM)))
		}
		num := new(uint256.Int).Mul(&out, ppmSq)
		num.Add(num, &denominator)
		num.Sub(num, uint256.NewInt(1))
		quoted = *num.Div(num, &denominator)
	}
	in, out2 := effectiveIndex(&p.InIndex), effectiveIndex(&p.OutIndex)
	amount, _ := fullMulDivUp(&quoted, out2, in)
	return *amount.Add(amount, uint256.NewInt(1))
}

func TestExactOutputCapacityRefusalIsFinal(t *testing.T) {
	leg := capped(relative(6, 6, 0, 0, 0, 0), 50, 1_000)
	target := u(60)
	_, err := QuoteExactOutput([]LegParams{leg}, &target, nil, DefaultSearchBudget)
	var qe *QuoteError
	require.ErrorAs(t, err, &qe)
	assert.Equal(t, ReasonDepositCap, qe.Reason)
}
