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

// Opt in on an isolated PostgreSQL instance with postgres_fdw privileges:
// CORE_TEST_DSN=postgres://... CORE_FDW_TEST=1 go test ./internal/store -run TestPoolActivityFDW
func TestPoolActivityFDW(t *testing.T) {
	if os.Getenv("CORE_FDW_TEST") != "1" {
		t.Skip("CORE_FDW_TEST=1 requires an isolated PostgreSQL instance")
	}
	_, source := newCatalogTestPool(t)
	t.Cleanup(source.Close)
	ctx := context.Background()
	config := source.Config()
	var port string
	require.NoError(t, source.QueryRow(ctx, "SHOW port").Scan(&port))
	_, err := source.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS postgres_fdw")
	require.NoError(t, err)
	name := fmt.Sprintf("activity_fdw_%d", time.Now().UnixNano())
	src, foreign, server := name+"_source", name+"_foreign", name+"_server"
	identifier := func(value string) string { return pgx.Identifier{value}.Sanitize() }
	literal := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
	_, err = source.Exec(ctx, "CREATE SCHEMA "+identifier(src))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := source.Exec(ctx, "DROP SCHEMA "+identifier(src)+" CASCADE")
		require.NoError(t, cleanupErr)
	})
	_, err = source.Exec(ctx, strings.ReplaceAll(`
    CREATE TABLE SOURCE.tx(id BIGINT PRIMARY KEY,tx_hash TEXT,date_block TIMESTAMP,success BOOLEAN);
    CREATE TABLE SOURCE.token_transfer(id BIGINT PRIMARY KEY,tx_id BIGINT,log_index BIGINT,
        contract_address TEXT,transfer_value NUMERIC,sender_address TEXT,recipient_address TEXT);
    CREATE TABLE SOURCE.pools(contract_address TEXT PRIMARY KEY);
    CREATE TABLE SOURCE.pool_swap(id BIGINT PRIMARY KEY,tx_id BIGINT,log_index BIGINT,contract_address TEXT,
        initiator_address TEXT,token_in_address TEXT,token_out_address TEXT,in_value NUMERIC,out_value NUMERIC,fee NUMERIC);
    CREATE TABLE SOURCE.pool_withdrawal(id BIGINT PRIMARY KEY,tx_id BIGINT,log_index BIGINT,
        contract_address TEXT,kind TEXT,initiator_address TEXT,token_address TEXT,recipient_address TEXT,amount NUMERIC);
    INSERT INTO SOURCE.tx SELECT n,'tx-'||n,'2026-10-01',true FROM generate_series(1,100003) n;
    INSERT INTO SOURCE.token_transfer SELECT n,n,1,'token',1,'other','another' FROM generate_series(1,100000) n;
    INSERT INTO SOURCE.pool_swap SELECT n,n,3,'other-pool','other','token','out',1,2,0 FROM generate_series(1,100000) n;
    INSERT INTO SOURCE.pool_withdrawal SELECT n,n,4,'other-pool','liquidity','other','token','another',1 FROM generate_series(1,100000) n;
    INSERT INTO SOURCE.token_transfer VALUES
        (100001,100001,1,'token',10,'user','pool'),
        (100002,100002,1,'token',10,'user','pool'),
        (100003,100002,2,'out',12,'pool','user'),
        (100004,100003,1,'token',7,'pool','user');
    INSERT INTO SOURCE.pool_swap VALUES (100001,100002,3,'pool','user','token','out',10,12,0);
    INSERT INTO SOURCE.pool_withdrawal VALUES (100001,100003,2,'pool','liquidity','owner','token','user',7);
    INSERT INTO SOURCE.pools VALUES ('pool');
    CREATE INDEX ON SOURCE.token_transfer(lower(sender_address),tx_id,log_index);
    CREATE INDEX ON SOURCE.token_transfer(lower(recipient_address),tx_id,log_index);
    CREATE INDEX ON SOURCE.token_transfer(tx_id,log_index);
    CREATE INDEX ON SOURCE.pool_swap(lower(initiator_address),tx_id);
    CREATE INDEX ON SOURCE.pool_swap(lower(contract_address),tx_id);
    CREATE INDEX ON SOURCE.pool_swap(tx_id);
    CREATE INDEX ON SOURCE.pool_withdrawal(lower(initiator_address),tx_id);
    CREATE INDEX ON SOURCE.pool_withdrawal(tx_id);
    ANALYZE SOURCE.tx; ANALYZE SOURCE.token_transfer; ANALYZE SOURCE.pool_swap; ANALYZE SOURCE.pool_withdrawal;
    `, "SOURCE", identifier(src)))
	require.NoError(t, err)
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
	// No foreign-table statistics or use_remote_estimate option. Repeated
	// prepared reads must stay bounded even if the connection uses generic plans.
	config.ConnConfig.RuntimeParams["search_path"] = foreign + ",public"
	config.ConnConfig.RuntimeParams["plan_cache_mode"] = "force_generic_plan"
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	queries, err := loadFederationQueries("../../federated_queries.sql")
	require.NoError(t, err)
	federation := &FederationPg{db: pool, queries: queries}
	filter := AccountActivityFilter{KnownPoolAddresses: []string{"pool"}}
	for range 6 {
		rows, queryErr := federation.ListUserActivity(ctx, "user", filter, nil, 20, true)
		require.NoError(t, queryErr)
		require.Len(t, rows, 3)
		require.Equal(t, []string{"withdrawn", "swapped", "deposited"}, []string{rows[0].Kind, rows[1].Kind, rows[2].Kind})
	}
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, "SET LOCAL plan_cache_mode = force_custom_plan")
	require.NoError(t, err)
	var raw []byte
	require.NoError(t, tx.QueryRow(ctx, "EXPLAIN (ANALYZE, VERBOSE, FORMAT JSON) "+queries.ListUserActivity,
		"user", []string{}, []string{}, "", nil, true, 20, []string{"pool"}).Scan(&raw))
	var plan []map[string]any
	require.NoError(t, json.Unmarshal(raw, &plan))
	t.Logf("FDW activity execution: %v ms", plan[0]["Execution Time"])
	scans := 0
	var inspect func(map[string]any)
	inspect = func(node map[string]any) {
		if node["Node Type"] == "Foreign Scan" {
			scans++
			remote, ok := node["Remote SQL"].(string)
			require.True(t, ok)
			if node["Relation Name"] != "pools" {
				require.Contains(t, remote, "WHERE", "history must be restricted on the indexer")
			}
			rows, ok := node["Actual Rows"].(float64)
			require.True(t, ok)
			loops, ok := node["Actual Loops"].(float64)
			require.True(t, ok)
			if removed, ok := node["Rows Removed by Filter"].(float64); ok {
				rows += removed
			}
			require.Less(t, rows*loops, float64(50), "must not consume unrelated global history")
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
	require.Positive(t, scans)
	require.NoError(t, tx.Rollback(ctx))
	// Both success and failure must return scoped settings to their prior value.
	_, err = pool.Exec(ctx, "ALTER FOREIGN TABLE "+identifier(foreign)+".tx OPTIONS (SET table_name 'missing_tx')")
	require.NoError(t, err)
	_, err = federation.ListUserActivity(ctx, "user", filter, nil, 20, true)
	require.Error(t, err)
	var setting string
	require.NoError(t, pool.QueryRow(ctx, "SHOW plan_cache_mode").Scan(&setting))
	require.Equal(t, "force_generic_plan", setting)
}
