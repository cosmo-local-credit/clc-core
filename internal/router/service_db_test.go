package router_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"slices"
	"sync"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/router"
	"github.com/cosmo-local-credit/clc-core/internal/router/routertest"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newStore(t *testing.T) store.Store {
	t.Helper()
	dsn := os.Getenv("CORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CORE_TEST_DSN not set; skipping router database tests")
	}
	st, err := store.NewPgStore(store.PgOpts{
		DSN: dsn, Logg: slog.New(slog.DiscardHandler),
		MigrationsFolderPath: "../../migrations", QueriesFolderPath: "../../core_queries.sql",
	})
	require.NoError(t, err)
	// Registered first, so it runs after every fixture's own cleanup.
	t.Cleanup(st.Pool().Close)
	return st
}

func newService(t *testing.T, st store.Store, n *routertest.Network) *router.Service {
	t.Helper()
	svc := router.New(router.Opts{
		Config: router.Config{ChainID: n.ChainID, MaxHops: 2},
		Store:  st, Chain: n.Chain, Relay: n.Relay(),
	})
	require.NoError(t, svc.Load(context.Background()))
	return svc
}

func ready(t *testing.T) (store.Store, *routertest.Network, *router.Service) {
	t.Helper()
	st := newStore(t)
	n := routertest.SeedNetwork(t, st.Pool())
	svc := newService(t, st, n)
	require.NoError(t, svc.ReconcileOnce(context.Background()))
	return st, n, svc
}

func u(v uint64) uint256.Int { return *uint256.NewInt(v) }

// Before reconciliation the graph knows membership and caps but no prices,
// balances or parameters: nothing is routable, and nothing is guessed.
func TestFreshGraphRoutesNothingUntilReconciled(t *testing.T) {
	st := newStore(t)
	n := routertest.SeedNetwork(t, st.Pool())
	svc := newService(t, st, n)

	snap := svc.Snapshot()
	require.NotNil(t, snap)
	assert.Len(t, snap.Pools, 4)
	assert.Len(t, snap.Edges, 8)
	assert.Zero(t, snap.LegCount())

	require.NoError(t, svc.ReconcileOnce(context.Background()))
	snap = svc.Snapshot()
	// Each two-member pool yields two directed legs.
	assert.Equal(t, 8, snap.LegCount())
	for _, p := range snap.Pools {
		assert.True(t, p.Verified, "pool %s verified by the differential probe", p.Address)
		assert.Equal(t, uint64(20_000), p.ProtocolPPM)
	}
}

func TestReconciliationRestoresWhatTheDatabaseMissed(t *testing.T) {
	st := newStore(t)
	n := routertest.SeedNetwork(t, st.Pool())
	ctx := context.Background()
	db := st.Pool()

	// Events the database never received.
	_, err := db.Exec(ctx, `UPDATE pool_tokens SET is_allowed = FALSE WHERE pool_id = $1 AND voucher_id = $2`,
		n.PoolIDs[n.P1], n.VoucherIDs[n.A])
	require.NoError(t, err)
	_, err = db.Exec(ctx, `UPDATE pool_tokens SET token_limit = 1 WHERE pool_id = $1 AND voucher_id = $2`,
		n.PoolIDs[n.P2], n.VoucherIDs[n.B])
	require.NoError(t, err)

	// A registry member the database never heard of, whose voucher is in the
	// catalog; and one whose voucher is not.
	var extra uint64
	seed := fmt.Sprint(n.ChainID, t.Name())
	extraAddr := common.BytesToAddress(crypto.Keccak256([]byte(seed))[:20])
	require.NoError(t, db.QueryRow(ctx, `
		INSERT INTO vouchers (chain_id, address, name, symbol, decimals, voucher_type)
		VALUES ($1, $2, $3, $4, 6, 'GIFTABLE') RETURNING id`,
		n.ChainID, extraAddr.Hex(), "Extra "+extraAddr.Hex()[:10], "EX"+extraAddr.Hex()[2:8]).Scan(&extra))
	// Reconciliation gives it a pool_tokens row, which must go first.
	t.Cleanup(func() {
		_, _ = db.Exec(context.Background(), `DELETE FROM pool_tokens WHERE voucher_id = $1`, extra)
		_, _ = db.Exec(context.Background(), `DELETE FROM vouchers WHERE id = $1`, extra)
	})
	stranger := common.BytesToAddress(crypto.Keccak256([]byte("stranger" + seed))[:20])
	n.Chain.Lock()
	p4 := n.Chain.Pools[n.P4]
	p4.Members = append(p4.Members, extraAddr, stranger)
	p4.Limits[extraAddr] = u(77)
	n.Chain.Unlock()

	svc := newService(t, st, n)
	corrections, err := svc.RunStructure(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, corrections, 3)

	var (
		allowed bool
		limit   string
		price   *string
	)
	require.NoError(t, db.QueryRow(ctx, `SELECT is_allowed FROM pool_tokens WHERE pool_id = $1 AND voucher_id = $2`,
		n.PoolIDs[n.P1], n.VoucherIDs[n.A]).Scan(&allowed))
	assert.True(t, allowed, "the missed INDEX_ADD is restored")
	require.NoError(t, db.QueryRow(ctx, `SELECT token_limit::TEXT FROM pool_tokens WHERE pool_id = $1 AND voucher_id = $2`,
		n.PoolIDs[n.P2], n.VoucherIDs[n.B]).Scan(&limit))
	assert.Equal(t, "10000000000", limit, "the missed LIMIT_SET is restored")
	require.NoError(t, db.QueryRow(ctx, `SELECT price_index::TEXT FROM pool_tokens WHERE pool_id = $1 AND voucher_id = $2`,
		n.PoolIDs[n.P3], n.VoucherIDs[n.C]).Scan(&price))
	require.NotNil(t, price)
	assert.Equal(t, "2000000", *price, "the price index the database never received")
	require.NoError(t, db.QueryRow(ctx, `SELECT is_allowed, token_limit::TEXT FROM pool_tokens WHERE pool_id = $1 AND voucher_id = $2`,
		n.PoolIDs[n.P4], extra).Scan(&allowed, &limit))
	assert.True(t, allowed, "an unknown registry member is added")
	assert.Equal(t, "77", limit)
	var strangers int
	require.NoError(t, db.QueryRow(ctx, `SELECT count(*) FROM pool_tokens WHERE pool_id = $1`, n.PoolIDs[n.P4]).Scan(&strangers))
	assert.Equal(t, 3, strangers, "a member with no catalog voucher is counted, not routed")

	// Running it again finds nothing left to correct.
	corrections, err = svc.RunStructure(ctx)
	require.NoError(t, err)
	assert.Zero(t, corrections)
}

func TestReachIsInclusiveByDepth(t *testing.T) {
	_, n, svc := ready(t)
	ctx := context.Background()

	one, err := svc.Reach(ctx, n.Wallet, 1)
	require.NoError(t, err)
	snap := one.Snap
	hops := func(r router.ReachResult) (map[common.Address]int, map[common.Address]int) {
		v, p := map[common.Address]int{}, map[common.Address]int{}
		for _, x := range r.Vouchers {
			v[snap.Tokens[x.Token].Address] = x.MinHops
		}
		for _, x := range r.Pools {
			p[snap.Pools[x.Pool].Addr] = x.MinHops
		}
		return v, p
	}
	v, p := hops(one.Result)
	assert.Equal(t, map[common.Address]int{n.B: 1, n.C: 1}, v)
	assert.Equal(t, map[common.Address]int{n.P1: 1, n.P3: 1}, p)
	require.Len(t, one.Result.Inputs, 1)
	assert.Equal(t, uint64(5_000_000), one.Result.Inputs[0].Balance.Uint64())

	two, err := svc.Reach(ctx, n.Wallet, 2)
	require.NoError(t, err)
	v, p = hops(two.Result)
	assert.Equal(t, map[common.Address]int{n.B: 1, n.C: 1, n.D: 2}, v, "depth 2 includes depth 1")
	assert.Equal(t, map[common.Address]int{n.P1: 1, n.P3: 1, n.P2: 2, n.P4: 2}, p)
	assert.NotEqual(t, one.Result.Digest, two.Result.Digest)

	_, err = svc.Reach(ctx, n.Wallet, 3)
	var reqErr *router.RequestError
	assert.ErrorAs(t, err, &reqErr)
}

func TestReachDistinguishesEmptyFromUnavailable(t *testing.T) {
	_, n, svc := ready(t)
	ctx := context.Background()

	stranger := common.HexToAddress("0x00000000000000000000000000000000000000ee")
	empty, err := svc.Reach(ctx, stranger, 2)
	require.NoError(t, err)
	assert.Empty(t, empty.Result.Vouchers)
	assert.Empty(t, empty.Result.Inputs)

	n.Chain.Lock()
	n.Chain.Down = true
	n.Chain.Unlock()
	svc.Forget(n.Wallet)
	_, err = svc.Reach(ctx, n.Wallet, 2)
	assert.ErrorIs(t, err, router.ErrUnavailable, "a failed balance read is never an empty wallet")
}

func TestRoutesGroupAndRankByOutput(t *testing.T) {
	_, n, svc := ready(t)
	ctx := context.Background()

	view, err := svc.Routes(ctx, n.Wallet, router.RoutesRequest{Target: n.C, Depth: 2})
	require.NoError(t, err)
	require.Len(t, view.Groups, 1)
	group := view.Groups[0]
	snap := view.Snap
	assert.Equal(t, n.A, snap.Tokens[group.Input.Token].Address)
	require.Len(t, group.Routes, 2)
	// Through B pays about twice the direct pool, which prices C at 2x.
	first, second := group.Routes[0], group.Routes[1]
	assert.Len(t, first.Route, 2, "the better-paying two-hop route ranks first")
	assert.Len(t, second.Route, 1)
	assert.Equal(t, "999601", first.RateOut.Dec())
	assert.Equal(t, "499900", second.RateOut.Dec())
	assert.True(t, first.Max.Known && first.Max.Exact)
	assert.Equal(t, "5000000", first.Max.AmountIn.Dec())
	assert.Equal(t, router.LimitWallet, first.Max.LimitedBy)
	assert.True(t, view.Complete)
	require.Len(t, view.Fallback, 3, "every pool that holds C is listed")

	// With an amount the wallet cannot cover, both routes follow their
	// maxima; an amount it can cover is carried and ranked by output.
	carried, err := svc.Routes(ctx, n.Wallet, router.RoutesRequest{
		Target: n.C, Depth: 2, Mode: router.ExactInput, Amount: uint256.NewInt(1_000_000)})
	require.NoError(t, err)
	routes := carried.Groups[0].Routes
	assert.True(t, routes[0].CarriesAmount)
	assert.Equal(t, "999601", routes[0].AmountOut.Dec())

	tooMuch, err := svc.Routes(ctx, n.Wallet, router.RoutesRequest{
		Target: n.C, Depth: 2, Mode: router.ExactInput, Amount: uint256.NewInt(6_000_000)})
	require.NoError(t, err)
	for _, r := range tooMuch.Groups[0].Routes {
		assert.False(t, r.CarriesAmount)
		require.NotNil(t, r.Refusal)
		assert.Equal(t, router.ReasonWalletBalance, r.Refusal.Reason)
	}

	exactOut, err := svc.Routes(ctx, n.Wallet, router.RoutesRequest{
		Target: n.C, Depth: 2, Mode: router.ExactOutput, Amount: uint256.NewInt(400_000)})
	require.NoError(t, err)
	routes = exactOut.Groups[0].Routes
	require.True(t, routes[0].CarriesAmount)
	assert.Len(t, routes[0].Route, 2, "lowest required input first")
	assert.True(t, routes[0].AmountIn.Lt(&routes[1].AmountIn))

	depthOne, err := svc.Routes(ctx, n.Wallet, router.RoutesRequest{Target: n.C, Depth: 1})
	require.NoError(t, err)
	require.Len(t, depthOne.Groups[0].Routes, 1)
	assert.Len(t, depthOne.Groups[0].Routes[0].Route, 1)

	_, err = svc.Routes(ctx, n.Wallet, router.RoutesRequest{Target: common.HexToAddress("0x01")})
	assert.ErrorIs(t, err, router.ErrUnknownVoucher)
}

func TestRoutesPageByCursor(t *testing.T) {
	st := newStore(t)
	n := routertest.SeedNetwork(t, st.Pool())
	svc := router.New(router.Opts{
		Config: router.Config{ChainID: n.ChainID, MaxHops: 2, MaxRoutesPerPage: 1},
		Store:  st, Chain: n.Chain, Relay: n.Relay(),
	})
	ctx := context.Background()
	require.NoError(t, svc.Load(ctx))
	require.NoError(t, svc.ReconcileOnce(ctx))

	first, err := svc.Routes(ctx, n.Wallet, router.RoutesRequest{Target: n.C})
	require.NoError(t, err)
	assert.False(t, first.Complete)
	require.NotEmpty(t, first.NextCursor)
	second, err := svc.Routes(ctx, n.Wallet, router.RoutesRequest{Target: n.C, Cursor: first.NextCursor})
	require.NoError(t, err)
	assert.True(t, second.Complete)
	require.Len(t, second.Groups, 1)
	assert.Len(t, second.Groups[0].Routes[0].Route, 1, "the second page holds the direct route")

	// The listing moves under the cursor: refused, never silently shifted.
	n.Chain.SetBalance(n.C, n.P2, 0)
	require.NoError(t, svc.ReconcileOnce(ctx))
	_, err = svc.Routes(ctx, n.Wallet, router.RoutesRequest{Target: n.C, Cursor: first.NextCursor})
	assert.ErrorAs(t, err, &router.CursorError{})
}

func twoHop(n *routertest.Network) []router.HopRef {
	return []router.HopRef{
		{Pool: n.P1, TokenIn: n.A, TokenOut: n.B},
		{Pool: n.P2, TokenIn: n.B, TokenOut: n.C},
	}
}

func TestQuoteReturnsASignableBatch(t *testing.T) {
	_, n, svc := ready(t)
	ctx := context.Background()

	q, err := svc.Quote(ctx, n.Wallet, router.QuoteRequest{Hops: twoHop(n), Mode: router.ExactInput, Amount: u(1_000_000)})
	require.NoError(t, err)
	out := q.Quote.AmountOut()
	assert.Equal(t, "999601", out.Dec())
	assert.Equal(t, "999601", q.MinAmountOut.Dec(), "exact input's minimum is the quoted output")
	require.Len(t, q.Batch.Calls, 6)
	assert.True(t, q.Batch.RevertOnFailure)
	assert.Equal(t, n.Relayer, q.Batch.Executor)
	assert.Equal(t, q.Batch.Digest(big.NewInt(n.ChainID), n.Wallet, n.Calibur), q.Digest)
	assert.True(t, q.Delegated)

	hops, ok := router.DecodeRouteShape(q.Batch.Calls)
	require.True(t, ok)
	require.NoError(t, router.ValidateRoute(hops, n.Wallet, q.ExpiresAt-300, 3600))
	assert.Equal(t, q.Quote.Inputs[1].ToBig(), hops[0].MinOut, "hop one must deliver what hop two spends")
	assert.Equal(t, q.Quote.Inputs[1].ToBig(), hops[1].Amount)
	assert.Equal(t, n.Wallet, hops[0].Recipient)
	assert.Equal(t, n.Wallet, hops[1].Recipient)
	assert.Equal(t, "200", q.Quote.Hops[0].ProtocolFee.Dec())
	assert.Equal(t, "399", q.EffectiveFeePPM.Dec())

	exact, err := svc.Quote(ctx, n.Wallet, router.QuoteRequest{Hops: twoHop(n), Mode: router.ExactOutput, Amount: u(999_601)})
	require.NoError(t, err)
	in := exact.Quote.AmountIn()
	assert.False(t, in.Gt(uint256.NewInt(1_000_000)), "exact output never needs more than exact input did")
	assert.Equal(t, "999601", exact.MinAmountOut.Dec(), "exact output's minimum is the requested quantity")
	got := exact.Quote.AmountOut()
	assert.False(t, got.Lt(uint256.NewInt(999_601)))

	recipient := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	paid, err := svc.Quote(ctx, n.Wallet, router.QuoteRequest{Hops: twoHop(n), Mode: router.ExactInput, Amount: u(10), Recipient: &recipient})
	require.NoError(t, err)
	hops, _ = router.DecodeRouteShape(paid.Batch.Calls)
	assert.Equal(t, n.Wallet, hops[0].Recipient, "intermediate output always returns to the wallet")
	assert.Equal(t, recipient, hops[1].Recipient)
}

// encodeBytes ABI-encodes one dynamic bytes argument.
func encodeBytes(data []byte) []byte {
	out := common.LeftPadBytes(big.NewInt(32).Bytes(), 32)
	out = append(out, common.LeftPadBytes(big.NewInt(int64(len(data))).Bytes(), 32)...)
	padded := make([]byte, (len(data)+31)/32*32)
	copy(padded, data)
	return append(out, padded...)
}

func TestQuoteRefusesWithReasons(t *testing.T) {
	st, n, svc := ready(t)
	ctx := context.Background()

	_, err := svc.Quote(ctx, n.Wallet, router.QuoteRequest{Hops: twoHop(n), Mode: router.ExactInput, Amount: u(6_000_000)})
	var routeErr *router.RouteError
	require.ErrorAs(t, err, &routeErr)
	assert.Equal(t, router.ReasonWalletBalance, routeErr.Reason)

	_, err = svc.Quote(ctx, n.Wallet, router.QuoteRequest{
		Hops: []router.HopRef{{Pool: n.P1, TokenIn: n.A, TokenOut: n.B}, {Pool: n.P1, TokenIn: n.B, TokenOut: n.A}},
		Mode: router.ExactInput, Amount: u(1)})
	var reqErr *router.RequestError
	require.ErrorAs(t, err, &reqErr, "pools must be distinct")

	// A quoter that disagrees with the local arithmetic.
	n.Chain.Lock()
	n.Chain.Pools[n.P2].QuoteSkew = 1
	n.Chain.Unlock()
	_, err = svc.Quote(ctx, n.Wallet, router.QuoteRequest{Hops: twoHop(n), Mode: router.ExactInput, Amount: u(1_000_000)})
	var mismatch *router.MismatchError
	require.ErrorAs(t, err, &mismatch)
	assert.Equal(t, 1, mismatch.Hop)
	var verified bool
	require.NoError(t, st.Pool().QueryRow(ctx, `SELECT quoter_verified FROM pool_chain_state WHERE pool_id = $1`,
		n.PoolIDs[n.P2]).Scan(&verified))
	assert.False(t, verified, "the pool drops out of discovery")
}

func TestQuoteDecodesASimulationRevert(t *testing.T) {
	_, n, svc := ready(t)
	inner := crypto.Keccak256([]byte("LimitExceeded()"))[:4]
	revert := slices.Concat(crypto.Keccak256([]byte("CallFailed(bytes)"))[:4], encodeBytes(inner))
	n.Chain.Lock()
	n.Chain.SimRevert = revert
	n.Chain.Unlock()
	_, err := svc.Quote(context.Background(), n.Wallet, router.QuoteRequest{Hops: twoHop(n), Mode: router.ExactInput, Amount: u(1_000)})
	var sim *router.SimulationError
	require.ErrorAs(t, err, &sim)
	assert.Equal(t, "LimitExceeded", sim.Revert.Reason)
}

func TestQuoteUnavailableWhenTheChainIs(t *testing.T) {
	_, n, svc := ready(t)
	n.Chain.Lock()
	n.Chain.Down = true
	n.Chain.Unlock()
	_, err := svc.Quote(context.Background(), n.Wallet, router.QuoteRequest{Hops: twoHop(n), Mode: router.ExactInput, Amount: u(1)})
	assert.True(t, errors.Is(err, router.ErrUnavailable))
}

// Two wallets quoting the same route at once share the pool reads and nothing
// else: each gets its own nonce, digest and recipient.
func TestConcurrentQuotesStayWalletSpecific(t *testing.T) {
	_, n, svc := ready(t)
	other := common.HexToAddress("0x00000000000000000000000000000000000000bb")
	n.Chain.SetBalance(n.A, other, 3_000_000)
	n.Chain.Delegate(other, n.Calibur)
	n.Chain.Lock()
	n.Chain.Seq[n.Wallet], n.Chain.Seq[other] = 4, 9
	n.Chain.Unlock()

	type result struct {
		view router.QuoteView
		err  error
	}
	var wg sync.WaitGroup
	results := make([]result, 2)
	for i, wallet := range []common.Address{n.Wallet, other} {
		wg.Go(func() {
			v, err := svc.Quote(context.Background(), wallet, router.QuoteRequest{Hops: twoHop(n), Mode: router.ExactInput, Amount: u(1_000_000)})
			results[i] = result{v, err}
		})
	}
	wg.Wait()
	require.NoError(t, results[0].err)
	require.NoError(t, results[1].err)
	a, b := results[0].view, results[1].view
	assert.Equal(t, n.Wallet, a.Wallet)
	assert.Equal(t, other, b.Wallet)
	assert.Equal(t, uint64(4), a.Batch.Nonce.Uint64())
	assert.Equal(t, uint64(9), b.Batch.Nonce.Uint64())
	assert.NotEqual(t, a.Digest, b.Digest)
	assert.Equal(t, n.Wallet, a.Recipient)
	assert.Equal(t, other, b.Recipient)
}
