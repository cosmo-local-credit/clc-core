package router

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type (
	productionFixture struct {
		Meta struct {
			Block uint64 `json:"block"`
		} `json:"meta"`
		Rows     StateRows `json:"rows"`
		PairFees []PairFee `json:"pairFees"`
	}

	routeCounts struct {
		Legs         int `json:"legs"`
		OneHopPairs  int `json:"oneHopPairs"`
		TwoHopRoutes int `json:"twoHopRoutes"`
		Pairs        int `json:"pairs"`
	}

	productionExpected struct {
		Block             uint64      `json:"block"`
		PlainGiftableOnly routeCounts `json:"plainGiftableOnly"`
		WithExternalERC20 routeCounts `json:"withExternalERC20"`
		ExternalERC20     []string    `json:"externalERC20"`
	}
)

func loadProductionFixture(t testing.TB) (productionFixture, productionExpected) {
	t.Helper()
	f, err := os.Open("testdata/production_graph.json.gz")
	if os.IsNotExist(err) {
		t.Skip("no production fixture; capture one with cmd/router-snapshot")
	}
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	require.NoError(t, err)
	var fixture productionFixture
	require.NoError(t, json.NewDecoder(gz).Decode(&fixture))

	raw, err := os.ReadFile("testdata/production_graph.expected.json")
	require.NoError(t, err)
	var expected productionExpected
	require.NoError(t, json.Unmarshal(raw, &expected))
	require.Equal(t, fixture.Meta.Block, expected.Block)
	return fixture, expected
}

func productionSnapshot(fixture productionFixture, plain []string) *Snapshot {
	cfg := BuildConfig{PlainERC20: map[common.Address]struct{}{}}
	for _, a := range plain {
		cfg.PlainERC20[common.HexToAddress(a)] = struct{}{}
	}
	fees := NewFeeBook()
	for _, f := range fixture.PairFees {
		fees.Put(f.Pool, f.In, f.Out, f.FeePPM, f.Block)
	}
	return NewState(fixture.Rows).Build(cfg, 1, fees)
}

func countRoutes(snap *Snapshot) routeCounts {
	var c routeCounts
	one := map[[2]int32]bool{}
	all := map[[2]int32]bool{}
	for token := range snap.Tokens {
		for _, leg := range snap.Legs(int32(token)) {
			c.Legs++
			one[[2]int32{leg.In, leg.Out}] = true
			all[[2]int32{leg.In, leg.Out}] = true
			for _, next := range snap.Legs(leg.Out) {
				if next.Pool != leg.Pool && next.Out != leg.In {
					c.TwoHopRoutes++
					all[[2]int32{leg.In, next.Out}] = true
				}
			}
		}
	}
	c.OneHopPairs, c.Pairs = len(one), len(all)
	return c
}

// The production graph, captured from public data at a pinned Gnosis block
// (cmd/router-snapshot), must give the same counts as an independent
// implementation of the eligibility rules (testdata/count_routes.py). At block
// 49,254,469 that is 27,578 one-hop and 119,109 two-hop routes.
func TestProductionGraphCounts(t *testing.T) {
	fixture, expected := loadProductionFixture(t)

	plain := countRoutes(productionSnapshot(fixture, nil))
	assert.Equal(t, expected.PlainGiftableOnly, plain, "routed vouchers: GiftableToken only")

	withERC20 := countRoutes(productionSnapshot(fixture, expected.ExternalERC20))
	assert.Equal(t, expected.WithExternalERC20, withERC20, "with every external ERC-20 allowlisted")
	t.Logf("block %d: plain %+v; with external ERC-20 %+v", fixture.Meta.Block, plain, withERC20)
}

// Reach and route enumeration on the production graph, for a wallet holding
// the five vouchers that sit in the most pools.
func TestProductionGraphReachAgreesWithEnumeration(t *testing.T) {
	fixture, _ := loadProductionFixture(t)
	snap := productionSnapshot(fixture, nil)
	type bridge struct {
		token int32
		pools int
	}
	var bridges []bridge
	for token := range snap.Tokens {
		bridges = append(bridges, bridge{int32(token), len(snap.TokenEdges(int32(token)))})
	}
	for i := range bridges {
		for j := i + 1; j < len(bridges); j++ {
			if bridges[j].pools > bridges[i].pools {
				bridges[i], bridges[j] = bridges[j], bridges[i]
			}
		}
	}
	var holdings []Holding
	inputs := []int32{}
	for _, b := range bridges[:5] {
		holdings = append(holdings, Holding{Token: b.token, Balance: *uint256.NewInt(1_000_000)})
		inputs = append(inputs, b.token)
	}
	reach := Reach(snap, holdings, 2)
	got := map[int32]int{}
	for _, v := range reach.Vouchers {
		got[v.Token] = v.MinHops
	}
	want := map[int32]int{}
	for target := range snap.Tokens {
		for _, route := range EnumerateRoutes(snap, inputs, int32(target), 2) {
			if hops, ok := want[int32(target)]; !ok || len(route) < hops {
				want[int32(target)] = len(route)
			}
		}
	}
	assert.Equal(t, want, got)
	t.Logf("five largest bridges reach %d vouchers and %d pools", len(reach.Vouchers), len(reach.Pools))
}

func BenchmarkProductionReach(b *testing.B) {
	fixture, _ := loadProductionFixture(b)
	snap := productionSnapshot(fixture, nil)
	holdings := make([]Holding, 0, 66)
	for token := 0; token < len(snap.Tokens) && len(holdings) < 66; token += 7 {
		holdings = append(holdings, Holding{Token: int32(token), Balance: *uint256.NewInt(1_000_000)})
	}
	for b.Loop() {
		Reach(snap, holdings, 2)
	}
}
