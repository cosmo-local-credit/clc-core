package router

import (
	"errors"
	"math/big"
	"math/rand/v2"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The reference below transcribes RelativeQuoter.valueFor and SwapPool's fee
// path with math/big, checking every intermediate against 2^256 the way
// Solidity's checked arithmetic does. It shares no code with quote.go.

var (
	two256 = new(big.Int).Lsh(big.NewInt(1), 256)
)

func fits(v *big.Int) bool { return v.Sign() >= 0 && v.Cmp(two256) < 0 }

func refGross(value, inIdx, outIdx *big.Int, din, dout uint8) (*big.Int, bool) {
	in, out := new(big.Int).Set(inIdx), new(big.Int).Set(outIdx)
	if in.Sign() == 0 {
		in.SetInt64(PPM)
	}
	if out.Sign() == 0 {
		out.SetInt64(PPM)
	}
	if din == dout {
		p := new(big.Int).Mul(value, in)
		if !fits(p) {
			return nil, false
		}
		return p.Div(p, out), true
	}
	gap := int64(din) - int64(dout)
	if gap < 0 {
		gap = -gap
	}
	d := new(big.Int).Exp(big.NewInt(10), big.NewInt(gap), nil)
	if !fits(d) {
		return nil, false
	}
	q := new(big.Int).Mul(value, in)
	q.Div(q, out)
	if !fits(q) {
		return nil, false
	}
	if din > dout {
		return q.Div(q, d), true
	}
	r := new(big.Int).Mul(value, in)
	r.Mod(r, out)
	tail := new(big.Int).Mul(r, d)
	tail.Div(tail, out)
	if !fits(tail) {
		return nil, false
	}
	scaled := new(big.Int).Mul(q, d)
	if !fits(scaled) {
		return nil, false
	}
	scaled.Add(scaled, tail)
	if !fits(scaled) {
		return nil, false
	}
	return scaled, true
}

func refSettle(gross *big.Int, fee, proto uint64) (poolFee, protocolFee, net *big.Int, ok bool) {
	poolFee = new(big.Int).Mul(gross, new(big.Int).SetUint64(fee))
	if !fits(poolFee) {
		return nil, nil, nil, false
	}
	poolFee.Div(poolFee, big.NewInt(PPM))
	protocolFee = new(big.Int)
	if proto > 0 {
		assumed := new(big.Int).Mul(gross, big.NewInt(floorFeePPM))
		if !fits(assumed) {
			return nil, nil, nil, false
		}
		assumed.Div(assumed, big.NewInt(PPM))
		base := assumed
		if poolFee.Cmp(assumed) >= 0 {
			base = poolFee
		}
		protocolFee.Mul(base, new(big.Int).SetUint64(proto))
		if !fits(protocolFee) {
			return nil, nil, nil, false
		}
		protocolFee.Div(protocolFee, big.NewInt(PPM))
	}
	total := new(big.Int).Add(poolFee, protocolFee)
	if total.Cmp(gross) > 0 {
		return nil, nil, nil, false
	}
	net = new(big.Int).Sub(gross, total)
	return poolFee, protocolFee, net, net.Sign() > 0
}

func u(v uint64) uint256.Int { return *uint256.NewInt(v) }

func ud(s string) uint256.Int { return *uint256.MustFromDecimal(s) }

func relative(din, dout uint8, inIdx, outIdx uint64, fee, proto uint64) LegParams {
	return LegParams{
		Quoter: QuoterRelative, FeePPM: fee, ProtocolPPM: proto,
		InAllowed: true, OutAllowed: true,
		InDecimals: din, OutDecimals: dout,
		InIndex: u(inIdx), OutIndex: u(outIdx),
		OutBalance: ud("1000000000000000000000000000000"),
	}
}

func TestGrossMatchesRelativeQuoterBranches(t *testing.T) {
	for _, tt := range []struct {
		name      string
		din, dout uint8
		in, out   uint64
		value     string
		want      string
	}{
		{"parity 6 to 6", 6, 6, 0, 0, "1000000", "1000000"},
		{"unset index means parity", 6, 6, 0, PPM, "1234567", "1234567"},
		{"10x index", 6, 6, 10 * PPM, PPM, "1000000", "10000000"},
		{"10x out index floors", 6, 6, PPM, 10 * PPM, "1000009", "100000"},
		{"128x index", 6, 6, 128 * PPM, PPM, "3", "384"},
		{"6 to 18 scales up exactly", 6, 18, PPM, PPM, "1", "1000000000000"},
		{"6 to 18 keeps the remainder term", 6, 18, PPM, 3 * PPM, "1", "333333333333"},
		{"18 to 6 floors once", 18, 6, PPM, PPM, "1999999999999", "1"},
		{"18 to 6 with an index", 18, 6, 2 * PPM, 3 * PPM, "3000000000000", "2"},
		{"zero value", 6, 6, PPM, PPM, "0", "0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			leg := relative(tt.din, tt.dout, tt.in, tt.out, 0, 0)
			value := ud(tt.value)
			got, err := leg.Gross(&value)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.Dec())

			ref, ok := refGross(value.ToBig(), new(big.Int).SetUint64(tt.in), new(big.Int).SetUint64(tt.out), tt.din, tt.dout)
			require.True(t, ok)
			assert.Equal(t, ref.String(), got.Dec(), "reference transcription")
		})
	}
}

func TestGrossRefusesWhereSolidityReverts(t *testing.T) {
	leg := relative(6, 6, 10*PPM, PPM, 0, 0)
	huge := *new(uint256.Int).SetAllOne()
	_, err := leg.Gross(&huge)
	var qe *QuoteError
	require.ErrorAs(t, err, &qe)
	assert.Equal(t, ReasonOverflow, qe.Reason)

	wide := relative(0, 78, PPM, PPM, 0, 0)
	one := u(1)
	_, err = wide.Gross(&one)
	require.ErrorAs(t, err, &qe, "10**78 does not fit a uint256")
}

func TestSettleMatchesProductionPool(t *testing.T) {
	// §2.5: getAmountOut for 1,000,000 units through a 0-fee pool with the
	// 2 % protocol fee returns 999,800 — the 1 % floor times 2 %.
	leg := relative(6, 6, 0, 0, 0, 20_000)
	amount := u(1_000_000)
	hop, err := leg.Arithmetic(&amount)
	require.NoError(t, err)
	assert.Equal(t, "1000000", hop.Gross.Dec())
	assert.Equal(t, "0", hop.PoolFee.Dec())
	assert.Equal(t, "200", hop.ProtocolFee.Dec())
	assert.Equal(t, "999800", hop.Net.Dec())
}

func TestSettleFeeSawtooth(t *testing.T) {
	// ROUTER.md §6.5: 1 % pool fee, 10 % protocol cut.
	for gross, want := range map[uint64]uint64{998: 989, 999: 990, 1000: 989, 1001: 990} {
		g := u(gross)
		_, _, net, err := Settle(&g, 10_000, 100_000)
		require.NoError(t, err)
		assert.Equal(t, want, net.Uint64(), "gross %d", gross)
	}
}

func TestSettleFloorAppliesBelowOnePercent(t *testing.T) {
	g := u(1_000_000)
	// 0.1 % pool fee: protocol fee is still charged on 1 %.
	poolFee, protocolFee, net, err := Settle(&g, 1_000, 20_000)
	require.NoError(t, err)
	assert.Equal(t, uint64(1_000), poolFee.Uint64())
	assert.Equal(t, uint64(200), protocolFee.Uint64())
	assert.Equal(t, uint64(998_800), net.Uint64())
	// 5 % pool fee: the base is the pool fee.
	poolFee, protocolFee, _, err = Settle(&g, 50_000, 20_000)
	require.NoError(t, err)
	assert.Equal(t, uint64(50_000), poolFee.Uint64())
	assert.Equal(t, uint64(1_000), protocolFee.Uint64())
}

func TestFeeDomainBoundary(t *testing.T) {
	// _mustFeeDomainPpm: fee >= 1 % and fee*(1e6+proto) >= 1e12 reverts.
	assert.True(t, FeeDomainOK(9_999, PPM), "below the floor the predicate never fires")
	assert.True(t, FeeDomainOK(999_999, 0))
	assert.False(t, FeeDomainOK(PPM, 0))
	assert.True(t, FeeDomainOK(500_000, 999_999))
	assert.False(t, FeeDomainOK(500_000, PPM))

	leg := relative(6, 6, 0, 0, PPM, 0)
	amount := u(1000)
	_, err := leg.Arithmetic(&amount)
	var qe *QuoteError
	require.ErrorAs(t, err, &qe)
	assert.Equal(t, ReasonFeeDomain, qe.Reason)
}

func TestZeroOutputRefused(t *testing.T) {
	leg := relative(18, 6, 0, 0, 0, 20_000)
	dust := u(999_999_999_999) // grosses to 0
	_, err := leg.Arithmetic(&dust)
	var qe *QuoteError
	require.ErrorAs(t, err, &qe)
	assert.Equal(t, ReasonZeroOutput, qe.Reason)
}

func TestSwapChecksInContractOrder(t *testing.T) {
	base := relative(6, 6, 0, 0, 0, 0)
	base.HasRegistry, base.HasLimiter = true, true
	base.InLimit, base.InBalance = u(1000), u(900)
	base.OutBalance = u(500)

	amount := u(100)
	hop, err := base.Swap(&amount)
	require.NoError(t, err)
	assert.Equal(t, uint64(100), hop.Net.Uint64())

	for _, tt := range []struct {
		name   string
		mutate func(*LegParams)
		amount uint64
		want   Reason
	}{
		{"out not allowed beats everything", func(p *LegParams) { p.OutAllowed, p.InAllowed = false, false }, 100, ReasonNotAllowedOut},
		{"fee domain before membership of in", func(p *LegParams) { p.FeePPM, p.InAllowed = PPM, false }, 100, ReasonFeeDomain},
		{"in not allowed", func(p *LegParams) { p.InAllowed = false }, 100, ReasonNotAllowedIn},
		{"deposit cap counts the input", func(p *LegParams) {}, 101, ReasonDepositCap},
		{"zero input", func(p *LegParams) {}, 0, ReasonZeroInput},
		{"liquidity checks the gross", func(p *LegParams) { p.OutBalance = u(99) }, 100, ReasonLiquidity},
		{"decoupled fees are not liquidity", func(p *LegParams) { p.FeesDecoupled, p.OutFeesHeld = true, u(401) }, 100, ReasonLiquidity},
	} {
		t.Run(tt.name, func(t *testing.T) {
			leg := base
			tt.mutate(&leg)
			amount := u(tt.amount)
			_, err := leg.Swap(&amount)
			var qe *QuoteError
			require.ErrorAs(t, err, &qe)
			assert.Equal(t, tt.want, qe.Reason)
		})
	}

	noRegistry := base
	noRegistry.HasRegistry, noRegistry.InAllowed, noRegistry.OutAllowed = false, false, false
	noRegistry.HasLimiter = false
	amount = u(100_000)
	noRegistry.OutBalance = u(200_000)
	_, err = noRegistry.Swap(&amount)
	assert.NoError(t, err, "no registry admits every token, no limiter caps nothing")
}

func TestGrossAndSettleMatchReferenceOnRandomInputs(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	decimals := []uint8{0, 2, 6, 8, 18}
	for range 20_000 {
		din, dout := decimals[rng.IntN(len(decimals))], decimals[rng.IntN(len(decimals))]
		inIdx, outIdx := rng.Uint64N(200*PPM), rng.Uint64N(200*PPM)
		fee := rng.Uint64N(200_000)
		proto := rng.Uint64N(PPM + 1)
		value := new(big.Int).SetUint64(rng.Uint64())
		value.Lsh(value, uint(rng.IntN(140)))

		leg := relative(din, dout, inIdx, outIdx, fee, proto)
		v, _ := uint256.FromBig(value)
		got, err := leg.Gross(v)
		ref, ok := refGross(value, new(big.Int).SetUint64(inIdx), new(big.Int).SetUint64(outIdx), din, dout)
		require.Equal(t, ok, err == nil, "gross overflow agreement for %s", value)
		if !ok {
			continue
		}
		require.Equal(t, ref.String(), got.Dec())

		poolFee, protocolFee, net, err := Settle(&got, fee, proto)
		rp, rpr, rn, rok := refSettle(ref, fee, proto)
		require.Equal(t, rok, err == nil, "settle agreement gross=%s fee=%d proto=%d", ref, fee, proto)
		if rok {
			assert.Equal(t, rp.String(), poolFee.Dec())
			assert.Equal(t, rpr.String(), protocolFee.Dec())
			assert.Equal(t, rn.String(), net.Dec())
		}
	}
}

// §6.3: g·K/D² <= net(g) < g·K/D² + 3 for every gross inside the fee domain.
func TestSettleBoundsProperty(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	d2 := new(big.Int).SetUint64(PPM * PPM)
	for range 50_000 {
		fee := rng.Uint64N(PPM)
		proto := rng.Uint64N(PPM + 1)
		if rng.IntN(4) == 0 {
			proto = 0
		}
		if !FeeDomainOK(fee, proto) {
			continue
		}
		g := rng.Uint64N(1 << uint(1+rng.IntN(60)))
		if g == 0 {
			continue
		}
		gross := u(g)
		_, _, net, err := Settle(&gross, fee, proto)
		k := SettleDenominator(fee, proto)
		lower := new(big.Int).Mul(new(big.Int).SetUint64(g), k.ToBig())
		// net*D² >= g*K   and   net*D² < g*K + 3*D²
		netScaled := new(big.Int).Mul(net.ToBig(), d2)
		if err != nil {
			// Inside the fee domain only a zero gross nets zero.
			t.Fatalf("settle refused gross=%d fee=%d proto=%d: %v", g, fee, proto, err)
		}
		require.True(t, netScaled.Cmp(lower) >= 0, "lower bound g=%d fee=%d proto=%d", g, fee, proto)
		upper := new(big.Int).Add(lower, new(big.Int).Mul(big.NewInt(3), d2))
		require.True(t, netScaled.Cmp(upper) < 0, "upper bound g=%d fee=%d proto=%d", g, fee, proto)
	}
}

func TestEffectiveFeeAcrossTwoHops(t *testing.T) {
	legs := []LegParams{relative(6, 6, 0, 0, 0, 20_000), relative(6, 6, 0, 0, 0, 20_000)}
	amount := u(1_000_000)
	quote, err := SwapRoute(legs, &amount, nil)
	require.NoError(t, err)
	out := quote.AmountOut()
	// Hop two's protocol fee floors: 999,800 grosses a 9,998 base, and 2 %
	// of that is 199.96, so 199.
	assert.Equal(t, "999601", out.Dec(), "about 0.02 % per hop")
	feeFree, err := FeeFree(legs, &amount)
	require.NoError(t, err)
	fee := EffectiveFeePPM(&feeFree, &out)
	assert.Equal(t, uint64(399), fee.Uint64(), "about 0.04 % over two")
}

func TestSwapRouteRefusesBeyondWallet(t *testing.T) {
	legs := []LegParams{relative(6, 6, 0, 0, 0, 0)}
	amount, wallet := u(11), u(10)
	_, err := SwapRoute(legs, &amount, &wallet)
	var qe *QuoteError
	require.True(t, errors.As(err, &qe))
	assert.Equal(t, ReasonWalletBalance, qe.Reason)
}
