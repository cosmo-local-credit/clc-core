package store

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/router"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/holiman/uint256"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func at(block uint64, logIndex uint32) router.Position {
	return router.Position{Block: block, LogIndex: logIndex}
}

func readLimit(t *testing.T, tx pgx.Tx, poolID uint64) string {
	t.Helper()
	var limit string
	require.NoError(t, tx.QueryRow(context.Background(),
		`SELECT token_limit::TEXT FROM pool_tokens WHERE pool_id = $1`, poolID).Scan(&limit))
	return limit
}

// An older LIMIT_SET redelivered after a newer one must change nothing and
// emit no delta. This is a hazard the projection had before the router.
func TestOrderedProjectionRefusesAnOlderRedelivery(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()
	poolID, _, _, _, _, pool, token := seedComposablePool(t, tx)

	rows, err := pg.SetPoolTokenLimit(ctx, tx, pool, token, "900", at(200, 3))
	require.NoError(t, err)
	require.Len(t, rows, 1)

	rows, err = pg.SetPoolTokenLimit(ctx, tx, pool, token, "100", at(200, 2))
	require.NoError(t, err)
	assert.Empty(t, rows, "an earlier log in the same block is stale")
	rows, err = pg.SetPoolTokenLimit(ctx, tx, pool, token, "100", at(199, 9))
	require.NoError(t, err)
	assert.Empty(t, rows, "an earlier block is stale")
	assert.Equal(t, "900", readLimit(t, tx, poolID))

	rows, err = pg.SetPoolTokenLimit(ctx, tx, pool, token, "950", at(200, 4))
	require.NoError(t, err)
	assert.Len(t, rows, 1)
	assert.Equal(t, "950", readLimit(t, tx, poolID))
}

// A chain read at block N includes every event in N: a late event from N
// loses to it, one from N+1 wins.
func TestReconciliationPositionSitsAtTheEndOfItsBlock(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()
	poolID, _, _, _, _, pool, token := seedComposablePool(t, tx)

	_, err := pg.SetPoolTokenLimit(ctx, tx, pool, token, "500", router.ReconcilePosition(300))
	require.NoError(t, err)

	rows, err := pg.SetPoolTokenLimit(ctx, tx, pool, token, "1", at(300, 7))
	require.NoError(t, err)
	assert.Empty(t, rows)
	assert.Equal(t, "500", readLimit(t, tx, poolID))

	rows, err = pg.SetPoolTokenLimit(ctx, tx, pool, token, "2", at(301, 0))
	require.NoError(t, err)
	assert.Len(t, rows, 1)
	assert.Equal(t, "2", readLimit(t, tx, poolID))
}

func TestPriceIndexProjectsByQuoterAndKeepsZero(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()
	poolID, _, _, _, quoter, _, token := seedComposablePool(t, tx)

	rows, err := pg.SetPoolTokenPriceIndex(ctx, tx, quoter, token, "10000000", at(10, 0))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, poolID, rows[0].PoolID)

	var price *string
	var allowed bool
	require.NoError(t, tx.QueryRow(ctx,
		`SELECT price_index::TEXT, is_allowed FROM pool_tokens WHERE pool_id = $1`, poolID).Scan(&price, &allowed))
	require.NotNil(t, price)
	assert.Equal(t, "10000000", *price)
	assert.False(t, allowed, "a price says nothing about membership")

	// 0 means parity on chain and is a real value, distinct from never heard.
	_, err = pg.SetPoolTokenPriceIndex(ctx, tx, quoter, token, "0", at(11, 0))
	require.NoError(t, err)
	require.NoError(t, tx.QueryRow(ctx,
		`SELECT price_index::TEXT FROM pool_tokens WHERE pool_id = $1`, poolID).Scan(&price))
	require.NotNil(t, price)
	assert.Equal(t, "0", *price)

	rows, err = pg.SetPoolTokenPriceIndex(ctx, tx, randomAddress(t, tx), token, "1", at(12, 0))
	require.NoError(t, err)
	assert.Empty(t, rows, "an unknown quoter matches no pool")
}

// committedPg opens a store whose methods manage their own transactions, for
// the router writes. The pool closes after every other cleanup has run, so
// fixtures can still delete their rows.
func committedPg(t *testing.T) *Pg {
	t.Helper()
	dsn := os.Getenv("CORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CORE_TEST_DSN not set; skipping live router store tests")
	}
	st, err := NewPgStore(PgOpts{
		DSN: dsn, Logg: slog.New(slog.DiscardHandler),
		MigrationsFolderPath: "../../migrations", QueriesFolderPath: "../../core_queries.sql",
	})
	require.NoError(t, err)
	pg, ok := st.(*Pg)
	require.True(t, ok)
	t.Cleanup(pg.db.Close)
	return pg
}

type (
	routerFixture struct {
		chainID                         int64
		poolID, voucherID               uint64
		pool, registry, limiter, quoter string
		token                           string
	}
)

func seedRouterFixture(t *testing.T, pg *Pg) routerFixture {
	t.Helper()
	ctx := context.Background()
	// A chain id no other suite uses keeps LoadRouterState to this fixture.
	f := routerFixture{chainID: 900_000 + int64(uuid.New().ID()%90_000)}
	f.pool, f.registry, f.limiter, f.quoter, f.token = randAddr(), randAddr(), randAddr(), randAddr(), randAddr()
	suffix := uuid.NewString()[:8]
	require.NoError(t, pg.db.QueryRow(ctx, `
		INSERT INTO pools (chain_id, address, name, symbol, token_registry_address, limiter_address, quoter_address, quoter_type)
		VALUES ($1, $2, $6, $7, $3, $4, $5, 'relative') RETURNING id`,
		f.chainID, f.pool, f.registry, f.limiter, f.quoter, "Router pool "+suffix, "RP"+suffix).Scan(&f.poolID))
	require.NoError(t, pg.db.QueryRow(ctx, `
		INSERT INTO vouchers (chain_id, address, name, symbol, decimals, voucher_type)
		VALUES ($1, $2, $3, $4, 6, 'GIFTABLE') RETURNING id`,
		f.chainID, f.token, "Router voucher "+suffix, "RV"+suffix).Scan(&f.voucherID))
	t.Cleanup(func() {
		_, _ = pg.db.Exec(context.Background(), `DELETE FROM pools WHERE id = $1`, f.poolID)
		_, _ = pg.db.Exec(context.Background(), `DELETE FROM vouchers WHERE id = $1`, f.voucherID)
	})
	return f
}

func randAddr() string {
	id := uuid.New()
	return common.BytesToAddress(id[:]).Hex()
}

func commitProjection(t *testing.T, pg *Pg, fn func(tx pgx.Tx)) {
	t.Helper()
	ctx := context.Background()
	tx, err := pg.db.Begin(ctx)
	require.NoError(t, err)
	fn(tx)
	require.NoError(t, tx.Commit(ctx))
}

// After a sequence of out-of-order events and a reconciliation, a reload
// from the database holds the newest value of every field.
func TestRouterReloadKeepsTheNewestValues(t *testing.T) {
	pg := committedPg(t)
	ctx := context.Background()
	f := seedRouterFixture(t, pg)

	commitProjection(t, pg, func(tx pgx.Tx) {
		_, err := pg.SetPoolTokenMembership(ctx, tx, f.registry, f.token, true, at(100, 1))
		require.NoError(t, err)
		_, err = pg.SetPoolTokenLimit(ctx, tx, f.pool, f.token, "700", at(105, 0))
		require.NoError(t, err)
		_, err = pg.SetPoolTokenPriceIndex(ctx, tx, f.quoter, f.token, "2000000", at(105, 1))
		require.NoError(t, err)
	})
	// The reconciler reads the chain at 104: older than the events above for
	// limit and price, so those keep the event values.
	limit := uint256.NewInt(123)
	price := uint256.NewInt(5)
	allowed := true
	require.NoError(t, pg.WriteStructure(ctx, f.chainID, []router.StructureWrite{{
		Registry: f.registry, Pool: f.pool, Quoter: f.quoter, Token: common.HexToAddress(f.token),
		Allowed: &allowed, Limit: limit, PriceIndex: price, Pos: router.ReconcilePosition(104),
	}}))
	// A redelivered older event.
	commitProjection(t, pg, func(tx pgx.Tx) {
		rows, err := pg.SetPoolTokenLimit(ctx, tx, f.pool, f.token, "1", at(101, 0))
		require.NoError(t, err)
		assert.Empty(t, rows)
	})
	applied, err := pg.WriteLiquidity(ctx, []router.LiquidityWrite{
		{Key: router.EdgeKey{PoolID: f.poolID, VoucherID: f.voucherID}, Balance: *uint256.NewInt(40), Block: 110},
	})
	require.NoError(t, err)
	assert.Len(t, applied, 1)
	applied, err = pg.WriteLiquidity(ctx, []router.LiquidityWrite{
		{Key: router.EdgeKey{PoolID: f.poolID, VoucherID: f.voucherID}, Balance: *uint256.NewInt(99), Block: 109},
	})
	require.NoError(t, err)
	assert.Empty(t, applied, "a slower read of an older block must not overwrite a newer balance")

	rows, err := pg.LoadRouterState(ctx, f.chainID)
	require.NoError(t, err)
	require.Len(t, rows.Pools, 1)
	require.Len(t, rows.Tokens, 1)
	require.Len(t, rows.Edges, 1)
	edge := rows.Edges[0]
	assert.True(t, edge.Allowed)
	require.NotNil(t, edge.Limit)
	assert.Equal(t, "700", *edge.Limit)
	require.NotNil(t, edge.PriceIndex)
	assert.Equal(t, "2000000", *edge.PriceIndex)
	assert.Equal(t, at(105, 0), edge.LimitPos)
	require.NotNil(t, edge.Balance)
	assert.Equal(t, "40", *edge.Balance)
	assert.Equal(t, uint64(110), edge.BalanceBlock)

	st := router.NewState(rows)
	snap := st.Build(router.BuildConfig{}, 1, router.NewFeeBook())
	require.Len(t, snap.Edges, 1)
	assert.Equal(t, "700", snap.Edges[0].Limit.Dec())
}

func TestWritePoolParamsGuardsAndCorrectsPointers(t *testing.T) {
	pg := committedPg(t)
	ctx := context.Background()
	f := seedRouterFixture(t, pg)

	newQuoter := common.HexToAddress(randAddr())
	params := router.PoolParams{
		Registry: common.HexToAddress(f.registry), Limiter: common.HexToAddress(f.limiter), Quoter: newQuoter,
		DefaultFeePPM: 1_000, ProtocolFeePPM: 20_000, ProtocolRecipient: common.HexToAddress(randAddr()),
		SealState: 3,
	}
	require.NoError(t, pg.WritePoolParams(ctx, []router.PoolParamsWrite{{PoolID: f.poolID, Params: params, QuoterVerified: true, Block: 50}}))
	stale := params
	stale.DefaultFeePPM = 9
	require.NoError(t, pg.WritePoolParams(ctx, []router.PoolParamsWrite{{PoolID: f.poolID, Params: stale, QuoterVerified: false, Block: 49}}))

	rows, err := pg.LoadRouterState(ctx, f.chainID)
	require.NoError(t, err)
	require.Len(t, rows.Pools, 1)
	p := rows.Pools[0]
	require.NotNil(t, p.ChainState)
	assert.Equal(t, uint64(1_000), p.ChainState.DefaultFeePPM, "an older refresh must not win")
	assert.True(t, p.ChainState.QuoterVerified)
	assert.Equal(t, uint8(3), p.ChainState.SealState)
	assert.Equal(t, newQuoter, common.HexToAddress(p.QuoterAddress), "the pool's quoter pointer follows the chain")

	require.NoError(t, pg.SetQuoterVerified(ctx, f.poolID, false, 50))
	rows, err = pg.LoadRouterState(ctx, f.chainID)
	require.NoError(t, err)
	assert.False(t, rows.Pools[0].ChainState.QuoterVerified)
}

func TestRoutedSwapReceiptSettlesOnlyItsOwnHops(t *testing.T) {
	pg := committedPg(t)
	ctx := context.Background()

	trackingID := uuid.NewString()
	wallet, poolOne, poolTwo := randAddr(), randAddr(), randAddr()
	a, b, c := randAddr(), randAddr(), randAddr()
	deadline := uint64(1_800_000_000)
	commitProjection(t, pg, func(tx pgx.Tx) {
		require.NoError(t, pg.InsertRoutedSwap(ctx, tx, RoutedSwapRecord{
			TrackingID: trackingID, ChainID: 100, Wallet: wallet, Recipient: wallet,
			TokenIn: a, TokenOut: c, AmountIn: "1000000", MinAmountOut: "999600", Bounded: true, Deadline: &deadline,
			Hops: []RoutedSwapHopRecord{
				{Pool: poolOne, TokenIn: a, TokenOut: b, AmountIn: "1000000", MinAmountOut: "999800", Recipient: wallet},
				{Pool: poolTwo, TokenIn: b, TokenOut: c, AmountIn: "999800", MinAmountOut: "999600", Recipient: wallet},
			},
		}))
	})
	t.Cleanup(func() {
		_, _ = pg.db.Exec(context.Background(), `DELETE FROM routed_swaps WHERE tracking_id = $1`, trackingID)
	})

	quoted, protocol := "999800", "200"
	commitProjection(t, pg, func(tx pgx.Tx) {
		matched, err := pg.SettleRoutedSwapHop(ctx, tx, trackingID, RoutedSwapSettlement{
			Pool: poolTwo, TokenIn: b, TokenOut: c, TxHash: "0xabc", Block: 7, LogIndex: 9,
			AmountIn: "999800", QuotedAmountOut: &quoted, AmountOut: "999601", PoolFee: "0", ProtocolFee: &protocol,
		})
		require.NoError(t, err)
		assert.True(t, matched)

		matched, err = pg.SettleRoutedSwapHop(ctx, tx, trackingID, RoutedSwapSettlement{
			Pool: randAddr(), TokenIn: a, TokenOut: b, TxHash: "0xabc", Block: 7, LogIndex: 3,
			AmountIn: "1", AmountOut: "1", PoolFee: "0",
		})
		require.NoError(t, err)
		assert.False(t, matched, "a swap in another pool is not this route's")
	})

	tx, err := pg.db.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	receipt, err := pg.GetRoutedSwap(ctx, tx, trackingID, wallet)
	require.NoError(t, err)
	require.Len(t, receipt.Hops, 2)
	assert.Nil(t, receipt.Hops[0].SettledAt)
	require.NotNil(t, receipt.Hops[1].AmountOut)
	assert.Equal(t, "999601", *receipt.Hops[1].AmountOut)

	_, err = pg.GetRoutedSwap(ctx, tx, trackingID, randAddr())
	assert.ErrorIs(t, err, pgx.ErrNoRows, "another wallet cannot read the receipt")

	list, err := pg.ListRoutedSwaps(ctx, tx, wallet, nil, 10)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, trackingID, list[0].TrackingID)
}
