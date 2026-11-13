package router

import (
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func addr(prefix byte, i int) string {
	var a common.Address
	a[0] = prefix
	a[16], a[17], a[18], a[19] = byte(i>>24), byte(i>>16), byte(i>>8), byte(i)
	return a.Hex()
}

// syntheticRows builds a graph shaped like production on Gnosis: pools
// with a median of about a dozen members, a few bridge vouchers sitting in
// many pools, parity prices with some at 10x, most edges with headroom and
// liquidity.
func syntheticRows(pools, vouchers int, seed uint64) StateRows {
	r := rand.New(rand.NewPCG(seed, seed+1))
	rows := StateRows{}
	six := 6
	for v := range vouchers {
		rows.Tokens = append(rows.Tokens, TokenRow{
			ID: uint64(v + 1), Address: addr(0xa0, v), Symbol: fmt.Sprintf("V%d", v),
			Decimals: &six, Active: true, VoucherType: "GIFTABLE",
		})
	}
	bridges := max(vouchers/25, 1)
	for p := range pools {
		cs := &PoolChainState{ProtocolFeePPM: 20_000, QuoterVerified: true, Block: 100}
		rows.Pools = append(rows.Pools, PoolRow{
			ID: uint64(p + 1), Address: addr(0xb0, p), Name: fmt.Sprintf("Pool %d", p), Active: true,
			QuoterType: "relative", RegistryAddress: addr(0xc0, p), LimiterAddress: addr(0xc1, p),
			QuoterAddress: addr(0xc2, p), ChainState: cs,
		})
		members := map[int]bool{}
		size := min(6+r.IntN(20), vouchers)
		for len(members) < size {
			if r.IntN(4) == 0 {
				members[r.IntN(bridges)] = true
			} else {
				members[r.IntN(vouchers)] = true
			}
		}
		for v := range members {
			limit := fmt.Sprint(1_000_000_000 + r.IntN(1_000_000_000))
			price := "0"
			if r.IntN(14) == 0 {
				price = "10000000"
			}
			balance := fmt.Sprint(r.IntN(900_000_000))
			if r.IntN(3) == 0 {
				balance = "0"
			}
			fees := "0"
			rows.Edges = append(rows.Edges, EdgeRow{
				PoolID: uint64(p + 1), VoucherID: uint64(v + 1), Allowed: true,
				Limit: &limit, PriceIndex: &price, Balance: &balance, FeesHeld: &fees, BalanceBlock: 100,
			})
		}
	}
	return rows
}

func TestDeltasAreIdempotentAndOrdered(t *testing.T) {
	rows := syntheticRows(10, 40, 1)
	st := NewState(rows)
	key := EdgeKey{rows.Edges[0].PoolID, rows.Edges[0].VoucherID}

	newer := EdgeLimitDelta{Key: key, Limit: *uint256.NewInt(5), Pos: Position{Block: 200, LogIndex: 2}}
	changed, missing := st.Apply(newer)
	assert.True(t, changed)
	assert.False(t, missing)
	changed, _ = st.Apply(newer)
	assert.False(t, changed, "applying a delta twice changes nothing")

	older := EdgeLimitDelta{Key: key, Limit: *uint256.NewInt(9), Pos: Position{Block: 200, LogIndex: 1}}
	changed, _ = st.Apply(older)
	assert.False(t, changed, "an older delta after a newer one changes nothing")
	assert.Equal(t, uint64(5), st.Edges[key].Limit.Uint64())

	stale := EdgeLiquidityDelta{Key: key, Balance: *uint256.NewInt(1), Block: 50}
	changed, _ = st.Apply(stale)
	assert.False(t, changed, "a balance read at an older block is ignored")

	_, missing = st.Apply(EdgeAllowedDelta{Key: EdgeKey{PoolID: 999, VoucherID: 1}, Allowed: true, Pos: Position{Block: 1}})
	assert.True(t, missing, "a row the graph has never seen asks for a reload")
}

// A reload equals the database, whatever deltas were applied before it.
func TestReloadConvergesOnTheRows(t *testing.T) {
	rows := syntheticRows(20, 80, 2)
	fresh := NewState(rows).Build(BuildConfig{}, 1, NewFeeBook())

	st := NewState(rows)
	for i, e := range rows.Edges {
		st.Apply(EdgeLiquidityDelta{Key: EdgeKey{e.PoolID, e.VoucherID}, Balance: *uint256.NewInt(uint64(i)), Block: 500})
	}
	drifted := st.Build(BuildConfig{}, 2, NewFeeBook())
	assert.NotEqual(t, fresh.LegCount(), 0)

	reloaded := NewState(rows).Build(BuildConfig{}, 3, NewFeeBook())
	assert.Equal(t, fresh.LegCount(), reloaded.LegCount())
	assert.Equal(t, fresh.Edges, reloaded.Edges)
	assert.NotEqual(t, drifted.Edges, reloaded.Edges)
}

// Readers load the pointer once and work on a consistent view while the
// writer publishes. Run with -race.
func TestConcurrentReadersDuringPublish(t *testing.T) {
	rows := syntheticRows(30, 120, 3)
	st := NewState(rows)
	var current atomic.Pointer[Snapshot]
	fees := NewFeeBook()
	current.Store(st.Build(BuildConfig{}, 1, fees))

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				snap := current.Load()
				holdings := []Holding{{Token: 0, Balance: *uint256.NewInt(1_000_000)}, {Token: 1, Balance: *uint256.NewInt(1_000_000)}}
				r := Reach(snap, holdings, 2)
				for _, v := range r.Vouchers {
					_ = snap.Tokens[v.Token].Address
				}
				FindRoutes(snap, holdings, 5, 2, nil, ExactInput, nil, DefaultSearchBudget, RankConfig{})
			}
		})
	}
	for i := range 50 {
		e := rows.Edges[i%len(rows.Edges)]
		st.Apply(EdgeLiquidityDelta{Key: EdgeKey{e.PoolID, e.VoucherID}, Balance: *uint256.NewInt(uint64(i * 1000)), Block: uint64(200 + i)})
		fees.Put(common.HexToAddress(addr(0xb0, i%30)), common.HexToAddress(addr(0xa0, 1)), common.HexToAddress(addr(0xa0, 2)), 3_000, uint64(i))
		current.Store(st.Build(BuildConfig{}, uint64(i+2), fees))
	}
	close(stop)
	wg.Wait()
}

// Reach must agree with exhaustive enumeration: a voucher is reachable at
// depth d exactly when some route of at most d legs delivers it.
func TestReachAgreesWithEnumeration(t *testing.T) {
	rows := syntheticRows(25, 60, 4)
	snap := NewState(rows).Build(BuildConfig{}, 1, NewFeeBook())
	r := rand.New(rand.NewPCG(9, 9))
	for range 20 {
		var holdings []Holding
		for range 1 + r.IntN(4) {
			holdings = append(holdings, Holding{Token: int32(r.IntN(len(snap.Tokens))), Balance: *uint256.NewInt(1)})
		}
		for _, depth := range []int{1, 2} {
			got := map[int32]int{}
			for _, v := range Reach(snap, holdings, depth).Vouchers {
				got[v.Token] = v.MinHops
			}
			want := map[int32]int{}
			inputs := make([]int32, 0, len(holdings))
			for _, h := range spendable(snap, holdings) {
				inputs = append(inputs, h.Token)
			}
			for target := range snap.Tokens {
				for _, route := range EnumerateRoutes(snap, inputs, int32(target), depth) {
					if hops, ok := want[int32(target)]; !ok || len(route) < hops {
						want[int32(target)] = len(route)
					}
				}
			}
			require.Equal(t, want, got, "depth %d", depth)
		}
	}
}

func TestPlainTransferEligibility(t *testing.T) {
	rows := syntheticRows(5, 20, 5)
	rows.Tokens[0].VoucherType = "ERC20"
	rows.Tokens[1].VoucherType = "DEMURRAGE"
	snap := NewState(rows).Build(BuildConfig{}, 1, NewFeeBook())
	ok, reason := snap.TokenRoutable(0)
	assert.False(t, ok)
	assert.Equal(t, ReasonUnsupportedToken, reason, "an external ERC-20 needs an explicit allowlist entry")
	ok, _ = snap.TokenRoutable(1)
	assert.False(t, ok, "a rebasing voucher is not routable")

	allow := BuildConfig{PlainERC20: map[common.Address]struct{}{common.HexToAddress(rows.Tokens[0].Address): {}}}
	snap = NewState(rows).Build(allow, 2, NewFeeBook())
	ok, _ = snap.TokenRoutable(0)
	assert.True(t, ok)
}

func TestFeeBookKeepsTheNewestWrite(t *testing.T) {
	book := NewFeeBook()
	pool, in, out := common.HexToAddress("0x01"), common.HexToAddress("0x02"), common.HexToAddress("0x03")
	book.Put(pool, in, out, 3_000, 10)
	book.Remove(pool, in, out, 12)
	_, ok := book.Get(pool, in, out)
	assert.False(t, ok, "a removed override falls back to the default")
	book.Put(pool, in, out, 9_000, 11)
	_, ok = book.Get(pool, in, out)
	assert.False(t, ok, "an older read cannot bring a removed override back")
	book.Put(pool, in, out, 5_000, 13)
	fee, ok := book.Get(pool, in, out)
	assert.True(t, ok)
	assert.Equal(t, uint64(5_000), fee)

	book.Put(pool, out, in, 0, 13)
	book.ForgetValue(pool, 0)
	_, ok = book.Get(pool, out, in)
	assert.False(t, ok, "entries equal to the old default go when the default changes")
	_, ok = book.Get(pool, in, out)
	assert.True(t, ok, "real overrides stay")
}
