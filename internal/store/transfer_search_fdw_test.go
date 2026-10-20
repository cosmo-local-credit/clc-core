package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// Opt in only on an isolated, privileged PostgreSQL instance. The FDW connects
// back to that instance's TCP port, independent of the test host's mapped port:
// CORE_TEST_DSN=postgres://... CORE_FDW_TEST=1 go test ./internal/store -run TestTransferHistorySearchFDW
func TestTransferHistorySearchFDW(t *testing.T) {
	if os.Getenv("CORE_FDW_TEST") != "1" {
		t.Skip("CORE_FDW_TEST=1 requires an isolated PostgreSQL instance with postgres_fdw privileges")
	}
	_, source := newCatalogTestPool(t)
	t.Cleanup(source.Close)
	ctx := context.Background()
	config := source.Config()
	var port string
	require.NoError(t, source.QueryRow(ctx, "SHOW port").Scan(&port))
	_, err := source.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS postgres_fdw")
	require.NoError(t, err)
	name := fmt.Sprintf("transfer_fdw_%d", time.Now().UnixNano())
	src, foreign, server := name+"_source", name+"_foreign", name+"_server"
	identifier := func(value string) string { return pgx.Identifier{value}.Sanitize() }
	literal := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
	// Source tables must be committed before the FDW's separate connection reads them.
	_, err = source.Exec(ctx, `CREATE SCHEMA `+identifier(src)+`;
        CREATE TABLE `+identifier(src)+`.tx(id BIGINT PRIMARY KEY, tx_hash TEXT, date_block TIMESTAMP, success BOOLEAN);
        CREATE TABLE `+identifier(src)+`.token_transfer(id BIGINT PRIMARY KEY, tx_id BIGINT,
            contract_address TEXT, transfer_value NUMERIC, sender_address TEXT, recipient_address TEXT);
        INSERT INTO `+identifier(src)+`.tx
            SELECT n, CASE WHEN n=6 THEN 'rare-query-match' ELSE 'hash-'||n END, '2026-10-01', true
            FROM generate_series(1,100000) n;
        INSERT INTO `+identifier(src)+`.token_transfer
            SELECT n,n,'voucher',1,CASE WHEN n%5=0 THEN 'owner' ELSE 'other' END,
                CASE WHEN n%5=1 THEN 'owner' ELSE 'other' END FROM generate_series(1,100000) n;
        CREATE INDEX ON `+identifier(src)+`.token_transfer(sender_address);
        CREATE INDEX ON `+identifier(src)+`.token_transfer(recipient_address);
        CREATE INDEX ON `+identifier(src)+`.token_transfer(tx_id);
        ANALYZE `+identifier(src)+`.tx;
        ANALYZE `+identifier(src)+`.token_transfer;`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := source.Exec(ctx, "DROP SCHEMA "+identifier(src)+" CASCADE")
		require.NoError(t, cleanupErr)
	})
	_, err = source.Exec(ctx, "CREATE SERVER "+identifier(server)+
		" FOREIGN DATA WRAPPER postgres_fdw OPTIONS(host '127.0.0.1', port "+literal(port)+
		", dbname "+literal(config.ConnConfig.Database)+")")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := source.Exec(ctx, "DROP SERVER "+identifier(server)+" CASCADE")
		require.NoError(t, cleanupErr)
	})
	_, err = source.Exec(ctx, "CREATE USER MAPPING FOR CURRENT_USER SERVER "+identifier(server)+
		" OPTIONS(user "+literal(config.ConnConfig.User)+", password "+literal(config.ConnConfig.Password)+")")
	require.NoError(t, err)
	_, err = source.Exec(ctx, "CREATE SCHEMA "+identifier(foreign)+"; IMPORT FOREIGN SCHEMA "+identifier(src)+
		" FROM SERVER "+identifier(server)+" INTO "+identifier(foreign))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := source.Exec(ctx, "DROP SCHEMA "+identifier(foreign)+" CASCADE")
		require.NoError(t, cleanupErr)
	})
	// Deliberately leave foreign-table statistics absent and use_remote_estimate
	// at its default false: this reproduces the review's underestimate. Start the
	// session on generic plans as a guard against prepared-statement regressions.
	config.ConnConfig.RuntimeParams["search_path"] = foreign + ",public"
	config.ConnConfig.RuntimeParams["plan_cache_mode"] = "force_generic_plan"
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	federation := &FederationPg{db: pool}
	filter, err := NewTransferHistoryFilter("voucher")
	require.NoError(t, err)
	search := TransferHistorySearch{Filter: filter, TokenAddresses: []string{"voucher"}}
	first, err := federation.SearchUserTransferHistory(ctx, "owner", search, nil, 20, true)
	require.NoError(t, err)
	require.Len(t, first, 20)
	require.EqualValues(t, 100000, first[0].ID)
	cursor := first[len(first)-1].ID
	next, err := federation.SearchUserTransferHistory(ctx, "owner", search, &cursor, 20, true)
	require.NoError(t, err)
	require.Len(t, next, 20)
	require.Less(t, next[0].ID, cursor)
	previous, err := federation.SearchUserTransferHistory(ctx, "owner", search, &next[0].ID, 20, false)
	require.NoError(t, err)
	require.Len(t, previous, 20)
	for index := range first {
		require.Equal(t, first[index].ID, previous[len(previous)-1-index].ID)
	}
	for label, bounds := range map[string]struct {
		cursor *uint64
		next   bool
	}{"first": {nil, true}, "next": {&cursor, true}, "previous": {&next[0].ID, false}} {
		t.Run(label, func(t *testing.T) {
			tx, beginErr := pool.Begin(ctx)
			require.NoError(t, beginErr)
			defer tx.Rollback(ctx)
			require.NoError(t, configureTransferSearch(ctx, tx))
			query := composeTransferHistorySearch("owner", search, bounds.cursor, 20, bounds.next)
			var raw []byte
			require.NoError(t, tx.QueryRow(ctx, "EXPLAIN (ANALYZE, VERBOSE, FORMAT JSON) "+query.sql, query.args...).Scan(&raw))
			t.Log(string(raw))
			var plan []map[string]any
			require.NoError(t, json.Unmarshal(raw, &plan))
			scans := 0
			var inspect func(map[string]any)
			inspect = func(node map[string]any) {
				if node["Node Type"] == "Foreign Scan" {
					scans++
					remote, ok := node["Remote SQL"].(string)
					require.True(t, ok)
					require.Contains(t, remote, "ORDER BY", "ordering must run on the source")
					require.Nil(t, node["Filter"], "text predicates must not filter locally")
					rows, ok := node["Actual Rows"].(float64)
					require.True(t, ok)
					require.LessOrEqual(t, rows, float64(20), "must not consume all 20000 matches")
				}
				if children, ok := node["Plans"].([]any); ok {
					for _, child := range children {
						childNode, ok := child.(map[string]any)
						require.True(t, ok)
						inspect(childNode)
					}
				}
			}
			root, ok := plan[0]["Plan"].(map[string]any)
			require.True(t, ok)
			inspect(root)
			require.Equal(t, 2, scans)
		})
	}
	rare, err := NewTransferHistoryFilter("rare-query-match")
	require.NoError(t, err)
	match, err := federation.SearchUserTransferHistory(ctx, "owner", TransferHistorySearch{Filter: rare}, nil, 20, true)
	require.NoError(t, err)
	require.Len(t, match, 1)
	require.EqualValues(t, 6, match[0].ID, "a sparse old match must still be found")
	// A failed remote read must roll back the scoped settings as well.
	_, err = pool.Exec(ctx, "ALTER FOREIGN TABLE "+identifier(foreign)+".tx OPTIONS (SET table_name 'missing_tx')")
	require.NoError(t, err)
	_, err = federation.SearchUserTransferHistory(ctx, "owner", search, nil, 20, true)
	require.Error(t, err)
	_, err = pool.Exec(ctx, "ALTER FOREIGN TABLE "+identifier(foreign)+".tx OPTIONS (SET table_name 'tx')")
	require.NoError(t, err)
	var sortSetting, cacheSetting string
	require.NoError(t, pool.QueryRow(ctx, "SHOW enable_sort").Scan(&sortSetting))
	require.NoError(t, pool.QueryRow(ctx, "SHOW plan_cache_mode").Scan(&cacheSetting))
	require.Equal(t, "on", sortSetting)
	require.Equal(t, "force_generic_plan", cacheSetting, "transaction settings must not leak to pooled reads")
}
