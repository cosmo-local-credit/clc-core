package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedComposablePool creates a catalog pool wired to its three dependency
// contracts, plus one voucher on the same chain, and returns their ids.
func seedComposablePool(t *testing.T, tx pgx.Tx) (poolID, voucherID uint64, registry, limiter, quoter, pool, token string) {
	t.Helper()
	ctx := context.Background()

	registry = randomAddress(t, tx)
	limiter = randomAddress(t, tx)
	quoter = randomAddress(t, tx)
	pool = randomAddress(t, tx)
	token = randomAddress(t, tx)

	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO pools (chain_id, address, name, symbol, token_registry_address, limiter_address, quoter_address)
		VALUES (42220, $1, 'Composable', 'CMP', $2, $3, $4)
		RETURNING id`, pool, registry, limiter, quoter).Scan(&poolID))

	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO vouchers (chain_id, address, name, symbol, decimals, active)
		VALUES (42220, $1, 'Composable Voucher', 'CMPV', 6, TRUE)
		RETURNING id`, token).Scan(&voucherID))

	return poolID, voucherID, registry, limiter, quoter, pool, token
}

func readComposition(t *testing.T, tx pgx.Tx, poolID uint64) (allowed bool, limit *string, feedID *uint64) {
	t.Helper()
	require.NoError(t, tx.QueryRow(context.Background(), `
		SELECT is_allowed, token_limit::TEXT, oracle_feed_id FROM pool_tokens WHERE pool_id = $1`,
		poolID).Scan(&allowed, &limit, &feedID))
	return allowed, limit, feedID
}

// The three events arrive from three contracts with no ordering guarantee
// between subjects, so the projection has to converge on the same row either
// way round — and a cap or a feed must never claim membership the registry has
// not granted.
func TestPoolCompositionConvergesInEitherOrder(t *testing.T) {
	for _, registryFirst := range []bool{true, false} {
		name := "registry first"
		if !registryFirst {
			name = "registry last"
		}

		t.Run(name, func(t *testing.T) {
			pg, tx, cleanup := newMarketTestPg(t)
			defer cleanup()
			ctx := context.Background()

			poolID, _, registry, _, quoter, pool, token := seedComposablePool(t, tx)

			var feedAddress string
			require.NoError(t, tx.QueryRow(ctx,
				`SELECT address FROM oracle_feeds ORDER BY id LIMIT 1`).Scan(&feedAddress))

			admit := func() {
				rows, err := pg.SetPoolTokenMembership(ctx, tx, registry, token, true)
				require.NoError(t, err)
				assert.EqualValues(t, 1, rows)
			}

			if registryFirst {
				admit()
			}

			rows, err := pg.SetPoolTokenLimit(ctx, tx, pool, token, "1000000000")
			require.NoError(t, err)
			assert.EqualValues(t, 1, rows)

			rows, err = pg.SetPoolTokenOracle(ctx, tx, quoter, token, feedAddress)
			require.NoError(t, err)
			assert.EqualValues(t, 1, rows)

			if !registryFirst {
				// Before the registry speaks, the pair is recorded but not allowed,
				// which keeps it out of voucher_count and the pool voucher list.
				allowed, _, _ := readComposition(t, tx, poolID)
				assert.False(t, allowed, "a cap must not imply registry membership")
				admit()
			}

			allowed, limit, feedID := readComposition(t, tx, poolID)
			assert.True(t, allowed)
			require.NotNil(t, limit)
			assert.Equal(t, "1000000000", *limit, "the registry event must not clear the cap")
			require.NotNil(t, feedID, "the registry event must not clear the feed")
		})
	}
}

// NATS redelivers; every statement has to be an upsert rather than an insert.
func TestPoolCompositionIsIdempotent(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	poolID, _, registry, _, _, pool, token := seedComposablePool(t, tx)

	for range 3 {
		_, err := pg.SetPoolTokenMembership(ctx, tx, registry, token, true)
		require.NoError(t, err)
		_, err = pg.SetPoolTokenLimit(ctx, tx, pool, token, "500")
		require.NoError(t, err)
	}

	var count int
	require.NoError(t, tx.QueryRow(ctx,
		`SELECT COUNT(*) FROM pool_tokens WHERE pool_id = $1`, poolID).Scan(&count))
	assert.Equal(t, 1, count)
}

// oracle_feeds is unique on name, not address: USDC and EURC share one
// aggregator. A non-lateral join fans out and Postgres aborts the upsert with
// "ON CONFLICT DO UPDATE command cannot affect row a second time".
func TestPoolCompositionHandlesAnAggregatorSharedByTwoFeeds(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	poolID, _, _, _, quoter, _, token := seedComposablePool(t, tx)

	var shared string
	require.NoError(t, tx.QueryRow(ctx, `
		SELECT address FROM oracle_feeds GROUP BY address HAVING COUNT(*) > 1 LIMIT 1`).Scan(&shared))

	rows, err := pg.SetPoolTokenOracle(ctx, tx, quoter, token, shared)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rows, "a shared aggregator must still write exactly one row")

	_, _, feedID := readComposition(t, tx, poolID)
	require.NotNil(t, feedID)
}

// The mapping is real even when this service cannot name the feed; that is a
// different fact from having no mapping, and must not fail the message.
func TestPoolCompositionStoresAnUnknownAggregatorAsNoFeed(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	poolID, _, _, _, quoter, _, token := seedComposablePool(t, tx)

	rows, err := pg.SetPoolTokenOracle(ctx, tx, quoter, token, randomAddress(t, tx))
	require.NoError(t, err)
	assert.EqualValues(t, 1, rows)

	_, _, feedID := readComposition(t, tx, poolID)
	assert.Nil(t, feedID)
}

// An event from a registry or pool this service never catalogued is the wider
// network's business. It must report no rows rather than error, or the consumer
// parks the message and stalls.
func TestPoolCompositionIgnoresUnknownContracts(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	_, _, _, _, quoter, _, token := seedComposablePool(t, tx)

	rows, err := pg.SetPoolTokenMembership(ctx, tx, randomAddress(t, tx), token, true)
	require.NoError(t, err)
	assert.EqualValues(t, 0, rows)

	rows, err = pg.SetPoolTokenLimit(ctx, tx, randomAddress(t, tx), token, "1")
	require.NoError(t, err)
	assert.EqualValues(t, 0, rows)

	rows, err = pg.ClearPoolTokenOracle(ctx, tx, quoter, token)
	require.NoError(t, err)
	assert.EqualValues(t, 0, rows, "removal must not conjure a row for an unpriced pair")
}
