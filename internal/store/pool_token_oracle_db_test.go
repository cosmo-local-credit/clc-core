package store

import (
	"context"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readPoolTokenOracle(t *testing.T, tx pgx.Tx, poolID uint64) (feedID *uint64, address *string) {
	t.Helper()
	require.NoError(t, tx.QueryRow(context.Background(), `
		SELECT oracle_feed_id, oracle_address FROM pool_tokens WHERE pool_id = $1`,
		poolID).Scan(&feedID, &address))
	return feedID, address
}

func seedOracleFeed(t *testing.T, tx pgx.Tx, name, address string) uint64 {
	t.Helper()
	var id uint64
	require.NoError(t, tx.QueryRow(context.Background(), `
		INSERT INTO oracle_feeds (chain_id, name, address) VALUES (42220, $1, $2)
		RETURNING id`, name+" "+address, address).Scan(&id))
	return id
}

// listedVoucher is the pool voucher row the API serves for the seeded pool.
func listedVoucher(t *testing.T, pg *Pg, tx pgx.Tx, pool string) PoolVoucher {
	t.Helper()
	vouchers, err := pg.ListPoolVouchers(context.Background(), tx, pool, 10)
	require.NoError(t, err)
	require.Len(t, vouchers, 1)
	return vouchers[0]
}

func checksum(address string) string {
	return common.HexToAddress(address).Hex()
}

func TestPoolTokenOracleNamesARegisteredFeed(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	poolID, _, registry, _, quoter, pool, token := seedComposablePool(t, tx)
	aggregator := randomAddress(t, tx)
	feedID := seedOracleFeed(t, tx, "Registered", aggregator)

	_, err := pg.SetPoolTokenMembership(ctx, tx, registry, token, true)
	require.NoError(t, err)
	rows, err := pg.SetPoolTokenOracle(ctx, tx, quoter, token, aggregator)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rows)

	gotFeed, gotAddress := readPoolTokenOracle(t, tx, poolID)
	require.NotNil(t, gotFeed)
	assert.Equal(t, feedID, *gotFeed)
	require.NotNil(t, gotAddress)
	assert.Equal(t, checksum(aggregator), *gotAddress, "the aggregator is stored EIP-55 checksummed")

	voucher := listedVoucher(t, pg, tx, pool)
	require.NotNil(t, voucher.OracleFeedID)
	assert.Equal(t, feedID, *voucher.OracleFeedID)
	require.NotNil(t, voucher.OracleName)
	assert.Equal(t, "Registered "+aggregator, *voucher.OracleName)
	require.NotNil(t, voucher.OracleAddress)
	assert.Equal(t, checksum(aggregator), *voucher.OracleAddress)
}

// An aggregator this service cannot name is still the pool's feed. It must
// read back as an address with no registry name, not as no feed.
func TestPoolTokenOracleKeepsAnUnregisteredAggregator(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	poolID, _, registry, _, quoter, pool, token := seedComposablePool(t, tx)
	aggregator := randomAddress(t, tx)

	_, err := pg.SetPoolTokenMembership(ctx, tx, registry, token, true)
	require.NoError(t, err)
	rows, err := pg.SetPoolTokenOracle(ctx, tx, quoter, token, aggregator)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rows)

	gotFeed, gotAddress := readPoolTokenOracle(t, tx, poolID)
	assert.Nil(t, gotFeed)
	require.NotNil(t, gotAddress)
	assert.Equal(t, checksum(aggregator), *gotAddress)

	voucher := listedVoucher(t, pg, tx, pool)
	assert.Nil(t, voucher.OracleFeedID)
	assert.Nil(t, voucher.OracleName)
	require.NotNil(t, voucher.OracleAddress)
	assert.Equal(t, checksum(aggregator), *voucher.OracleAddress)
}

func TestPoolTokenOracleRemovalClearsFeedAndAddress(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	poolID, _, registry, _, quoter, pool, token := seedComposablePool(t, tx)
	aggregator := randomAddress(t, tx)
	seedOracleFeed(t, tx, "Removed", aggregator)

	_, err := pg.SetPoolTokenMembership(ctx, tx, registry, token, true)
	require.NoError(t, err)
	_, err = pg.SetPoolTokenOracle(ctx, tx, quoter, token, aggregator)
	require.NoError(t, err)

	rows, err := pg.ClearPoolTokenOracle(ctx, tx, quoter, token)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rows)

	gotFeed, gotAddress := readPoolTokenOracle(t, tx, poolID)
	assert.Nil(t, gotFeed)
	assert.Nil(t, gotAddress)

	voucher := listedVoucher(t, pg, tx, pool)
	assert.Nil(t, voucher.OracleFeedID)
	assert.Nil(t, voucher.OracleName)
	assert.Nil(t, voucher.OracleAddress)
}

// USDC and EURC share one aggregator on Celo. The token keeps that one address
// and resolves deterministically to the lower feed id.
func TestPoolTokenOracleAggregatorSharedByTwoNames(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	poolID, _, _, _, quoter, _, token := seedComposablePool(t, tx)
	shared := randomAddress(t, tx)
	first := seedOracleFeed(t, tx, "Shared first", shared)
	seedOracleFeed(t, tx, "Shared second", shared)

	rows, err := pg.SetPoolTokenOracle(ctx, tx, quoter, token, shared)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rows)

	gotFeed, gotAddress := readPoolTokenOracle(t, tx, poolID)
	require.NotNil(t, gotFeed)
	assert.Equal(t, first, *gotFeed)
	require.NotNil(t, gotAddress)
	assert.Equal(t, checksum(shared), *gotAddress)
}

// Re-pointing a token replaces both columns, including moving from a
// registered feed to an unregistered one, which must not keep the old name.
func TestPoolTokenOracleResetToADifferentFeed(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	poolID, _, _, _, quoter, _, token := seedComposablePool(t, tx)
	before := randomAddress(t, tx)
	after := randomAddress(t, tx)
	unregistered := randomAddress(t, tx)
	seedOracleFeed(t, tx, "Before", before)
	afterID := seedOracleFeed(t, tx, "After", after)

	_, err := pg.SetPoolTokenOracle(ctx, tx, quoter, token, before)
	require.NoError(t, err)
	rows, err := pg.SetPoolTokenOracle(ctx, tx, quoter, token, after)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rows)

	gotFeed, gotAddress := readPoolTokenOracle(t, tx, poolID)
	require.NotNil(t, gotFeed)
	assert.Equal(t, afterID, *gotFeed)
	require.NotNil(t, gotAddress)
	assert.Equal(t, checksum(after), *gotAddress)

	_, err = pg.SetPoolTokenOracle(ctx, tx, quoter, token, unregistered)
	require.NoError(t, err)
	gotFeed, gotAddress = readPoolTokenOracle(t, tx, poolID)
	assert.Nil(t, gotFeed, "an unregistered aggregator must not keep the previous feed's id")
	require.NotNil(t, gotAddress)
	assert.Equal(t, checksum(unregistered), *gotAddress)
}
