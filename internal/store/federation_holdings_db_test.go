package store

import (
	"context"
	"testing"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/stretchr/testify/require"
)

// Run the actual federation SQL against transaction-local source tables. This
// needs only CORE_TEST_DSN and does not change the indexer or its live data.
func TestFederationHoldingsKeepTransactionTiesDB(t *testing.T) {
	_, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()
	queries, err := loadFederationQueries("../../federated_queries.sql")
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `
        CREATE TEMP TABLE tx(id BIGINT, success BOOLEAN, date_block TIMESTAMP) ON COMMIT DROP;
        CREATE TEMP TABLE token_transfer(tx_id BIGINT, contract_address TEXT,
            transfer_value NUMERIC, sender_address TEXT, recipient_address TEXT) ON COMMIT DROP;
        INSERT INTO tx VALUES (90,true,'2026-10-01'),(100,true,'2026-10-02'),(110,true,'2026-10-03');
        INSERT INTO token_transfer SELECT 100,'token-'||n,1,'sender','holder' FROM generate_series(1,6) n;
        INSERT INTO token_transfer VALUES (90,'older',1,'sender','holder'),(110,'newer',1,'sender','holder');
    `)
	require.NoError(t, err)
	var first, next, previous, boundary []FederationTokenBalance
	require.NoError(t, pgxscan.Select(ctx, tx, &first, queries.ListUserTokenHoldings, "holder", 5))
	// One recent row plus all six rows tied at the requested page boundary.
	require.Len(t, first, 7)
	require.EqualValues(t, 110, first[0].ID)
	require.EqualValues(t, 100, first[6].ID)
	require.NoError(t, pgxscan.Select(ctx, tx, &next, queries.ListUserTokenHoldingsNext, "holder", first[6].ID, 5))
	require.Len(t, next, 1)
	require.Equal(t, "older", next[0].TokenAddress)
	require.NoError(t, pgxscan.Select(ctx, tx, &boundary, queries.ListUserTokenHoldingsNext, "holder", 110, 5))
	require.Len(t, boundary, 6)
	require.NoError(t, pgxscan.Select(ctx, tx, &previous, queries.ListUserTokenHoldingsPrevious, "holder", 90, 5))
	require.Len(t, previous, 6)
	addresses := make(map[string]bool)
	for _, row := range append(first, next...) {
		require.False(t, addresses[row.TokenAddress], "duplicate token %s", row.TokenAddress)
		addresses[row.TokenAddress] = true
	}
	require.Len(t, addresses, 8)
}

// The issued list reads balances for tokens the holder may hold none of, so a
// fully spent token answers zero rather than dropping out like a holding does.
func TestFederationTokenBalancesKeepZeroDB(t *testing.T) {
	_, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()
	queries, err := loadFederationQueries("../../federated_queries.sql")
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `
        CREATE TEMP TABLE tx(id BIGINT, success BOOLEAN, date_block TIMESTAMP) ON COMMIT DROP;
        CREATE TEMP TABLE token_transfer(tx_id BIGINT, contract_address TEXT,
            transfer_value NUMERIC, sender_address TEXT, recipient_address TEXT) ON COMMIT DROP;
        INSERT INTO tx VALUES (1,true,'2026-10-01'),(2,true,'2026-10-02'),(3,false,'2026-10-03');
        INSERT INTO token_transfer VALUES
            (1,'spent',5,'minter','holder'),(2,'spent',5,'holder','buyer'),
            (1,'kept',5,'minter','holder'),(2,'kept',2,'holder','buyer'),
            (3,'failed',5,'minter','holder'),
            (1,'unasked',5,'minter','holder');
    `)
	require.NoError(t, err)
	var balances []FederationTokenBalance
	require.NoError(t, pgxscan.Select(ctx, tx, &balances, queries.ListUserTokenBalances,
		"holder", []string{"spent", "kept", "failed", "never"}))
	got := make(map[string]string, len(balances))
	for _, row := range balances {
		got[row.TokenAddress] = row.Balance
	}
	// A failed transfer moves nothing, and a token never moved has no row.
	require.Equal(t, map[string]string{"spent": "0", "kept": "3"}, got)
}
