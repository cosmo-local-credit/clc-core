package store

import (
	"context"
	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestTransferHistorySearchDB(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()
	_, err := tx.Exec(ctx, `
 CREATE TEMP TABLE tx(id BIGINT, tx_hash TEXT, success BOOLEAN, date_block TIMESTAMP) ON COMMIT DROP;
 CREATE TEMP TABLE token_transfer(id BIGINT, tx_id BIGINT, contract_address TEXT, transfer_value NUMERIC,
 sender_address TEXT, recipient_address TEXT) ON COMMIT DROP;
 CREATE TEMP TABLE vouchers(address TEXT, display_name TEXT, name TEXT, symbol TEXT, active BOOLEAN) ON COMMIT DROP;
 INSERT INTO vouchers VALUES ('voucher','Old voucher','chain name','OLD',false), ('unrelated',NULL,'Other','NEW',true);
 INSERT INTO tx SELECT n, 'hash-'||lpad(n::text,3,'0'),true,'2026-10-01' FROM generate_series(1,100) n;
 INSERT INTO token_transfer SELECT n,n,'unrelated',1,'peer','owner' FROM generate_series(10,100) n;
 INSERT INTO token_transfer VALUES
 (9,9,'voucher',1,'peer','someone-else'),
 (8,8,'voucher',1,'peer','owner'),
 (7,7,'voucher',1,'owner','peer'),
 (6,6,'voucher',1,'owner','owner'),
 (5,5,'uncatalogued',1,'Peer%_literal','owner');
 `)
	require.NoError(t, err)
	filter := mustFilter(t, "old", nil, TagMatchAny)
	tokens, err := pg.ListTransferSearchTokens(ctx, tx, filter)
	require.NoError(t, err)
	require.Equal(t, []string{"voucher"}, tokens, "retired metadata must remain searchable")
	bySymbol, err := pg.ListTransferSearchTokens(ctx, tx, mustFilter(t, "OLD", nil, TagMatchAny))
	require.NoError(t, err)
	require.Equal(t, tokens, bySymbol)
	// The display override is the name shown on the transfer row.
	byHiddenName, err := pg.ListTransferSearchTokens(ctx, tx, mustFilter(t, "chain name", nil, TagMatchAny))
	require.NoError(t, err)
	require.Empty(t, byHiddenName)
	read := func(term string, tokens []string, cursor *uint64, next bool) []FederationTransfer {
		t.Helper()
		search := TransferHistorySearch{Filter: mustFilter(t, term, nil, TagMatchAny), TokenAddresses: tokens}
		query := composeTransferHistorySearch("owner", search, cursor, 2, next)
		var rows []FederationTransfer
		require.NoError(t, pgxscan.Select(ctx, tx, &rows, query.sql, query.args...))
		return rows
	}
	first := read("old", tokens, nil, true)
	require.Len(t, first, 2)
	require.EqualValues(t, 8, first[0].ID, "match behind 91 newer nonmatches must be on page one")
	require.Equal(t, "IN", first[0].Direction)
	require.EqualValues(t, 7, first[1].ID)
	require.Equal(t, "OUT", first[1].Direction)
	next := read("old", tokens, &first[1].ID, true)
	require.Len(t, next, 1)
	require.EqualValues(t, 6, next[0].ID, "self-transfer appears once")
	require.Equal(t, "IN", next[0].Direction)
	require.Empty(t, read("old", tokens, &next[0].ID, true))
	previous := read("old", tokens, &next[0].ID, false)
	require.Len(t, previous, 2)
	require.EqualValues(t, 7, previous[0].ID)
	require.EqualValues(t, 8, previous[1].ID)
	require.Len(t, read("HASH-008", nil, nil, true), 1, "hash search ignores case")
	require.Len(t, read("uncatalogued", nil, nil, true), 1, "uncatalogued contract address is searchable")
	require.Len(t, read("peer%_", nil, nil, true), 1, "LIKE metacharacters are literal")
	require.Empty(t, read("missing", nil, nil, true))
	require.Len(t, read("ash-008", nil, nil, true), 1)
	// The two-character contract is prefix-only, consistent with other searches.
	require.Empty(t, read("sh", nil, nil, true))
	require.NotEmpty(t, read("ha", nil, nil, true))
}

func TestTransferHistoryFilter(t *testing.T) {
	hash := "0x" + strings.Repeat("a", 64)
	filter, err := NewTransferHistoryFilter("  " + hash + "  ")
	require.NoError(t, err)
	require.Equal(t, hash, filter.Term)
	for _, raw := range []string{"x", strings.Repeat("a", 67)} {
		_, err := NewTransferHistoryFilter(raw)
		require.Error(t, err)
	}
	_, err = NewTransferHistoryFilter(" ")
	require.NoError(t, err)
}
