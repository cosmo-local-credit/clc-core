package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/stretchr/testify/require"
)

func TestPublicProfileActivityQueryPrivacyPaginationAndPlansDB(t *testing.T) {
	_, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()
	queries, err := loadFederationQueries("../../federated_queries.sql")
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `
        CREATE TEMP TABLE tx(id BIGINT PRIMARY KEY,tx_hash TEXT,success BOOLEAN,date_block TIMESTAMP) ON COMMIT DROP;
        CREATE TEMP TABLE token_transfer(tx_id BIGINT,log_index BIGINT,sender_address TEXT,recipient_address TEXT,transfer_value NUMERIC,private_note TEXT) ON COMMIT DROP;
        CREATE TEMP TABLE pool_swap(tx_id BIGINT,log_index BIGINT,initiator_address TEXT,private_note TEXT) ON COMMIT DROP;
        INSERT INTO tx VALUES
            (1,'creation',true,'2026-01-01'),(2,'received',true,'2026-01-02'),
            (3,'sent',true,'2026-01-03'),(4,'multi-hop',true,'2026-01-04'),
            (5,'failed',false,'2026-01-05'),(6,'unrelated',true,'2026-01-06'),
            (7,'self',true,'2026-01-07'),(8,'swap-only',true,'2026-01-08');
        INSERT INTO token_transfer VALUES
            (1,1,'0x0000000000000000000000000000000000000000','User',123,'private creation note'),
            (2,1,'friend','USER',234,'private incoming note'),
            (3,1,'user','friend',345,'private outgoing note'),
            (3,2,'USER','other friend',456,'second transfer same transaction'),
            (4,1,'User','pool',567,'private swap note'),(4,2,'pool','user',678,'private swap note'),
            (5,1,'user','friend',789,'failed'),(6,1,'friend','other friend',890,'unrelated'),
            (7,1,'USER','user',901,'self transfer');
        INSERT INTO pool_swap VALUES (4,3,'USER','private swap note'),(4,4,'user','second route leg'),(8,1,'user','private swap-only note');
    `)
	require.NoError(t, err)
	read := func(cursor any, limit int) []PublicAccountActivity {
		var rows []PublicAccountActivity
		require.NoError(t, pgxscan.Select(ctx, tx, &rows, queries.ListPublicAccountActivity, "uSeR", cursor, limit))
		return rows
	}
	all := read(nil, 20)
	require.Len(t, all, 6)
	kinds := []string{}
	for _, row := range all {
		kinds = append(kinds, row.Kind)
	}
	require.Equal(t, []string{"swapped", "sent", "swapped", "sent", "received", "creation"}, kinds)
	first := read(nil, 2)
	second := read(int64(first[1].ID), 2)
	third := read(int64(second[1].ID), 2)
	require.Equal(t, all, append(append(first, second...), third...))
	require.Empty(t, read(int64(third[1].ID), 2))
	wire, err := json.Marshal(all)
	require.NoError(t, err)
	var projected []map[string]any
	require.NoError(t, json.Unmarshal(wire, &projected))
	for _, row := range projected {
		require.Len(t, row, 4, "the public DTO has only id, txHash, timestamp and kind")
		for _, key := range []string{"id", "txHash", "timestamp", "kind"} {
			require.Contains(t, row, key)
		}
	}
	require.NotContains(t, string(wire), "private")
	require.NotContains(t, string(wire), "failed")
	require.NotContains(t, string(wire), "unrelated")

	// Representative irrelevant history must remain behind indexed candidate
	// lookups. The named query limits each source before combining sources.
	_, err = tx.Exec(ctx, `
        INSERT INTO tx SELECT g,'background-'||g,true,'2026-01-01'::timestamp FROM generate_series(1000,21000) g;
        INSERT INTO token_transfer SELECT g,1,'background','other',1,'private background' FROM generate_series(1000,21000) g;
        INSERT INTO pool_swap SELECT g,1,'background','private background' FROM generate_series(1000,21000) g;
        CREATE INDEX public_activity_sender_page_idx ON token_transfer(lower(sender_address),tx_id DESC);
        CREATE INDEX public_activity_recipient_page_idx ON token_transfer(lower(recipient_address),tx_id DESC);
        CREATE INDEX public_activity_initiator_page_idx ON pool_swap(lower(initiator_address),tx_id DESC);
        CREATE INDEX public_activity_transfer_tx_idx ON token_transfer(tx_id);
        CREATE INDEX public_activity_swap_tx_idx ON pool_swap(tx_id);
        ANALYZE tx; ANALYZE token_transfer; ANALYZE pool_swap;
    `)
	require.NoError(t, err)
	for _, cursor := range []any{nil, int64(first[1].ID)} {
		rows, err := tx.Query(ctx, "EXPLAIN (ANALYZE, COSTS OFF) "+queries.ListPublicAccountActivity, "uSeR", cursor, 20)
		require.NoError(t, err)
		var lines []string
		for rows.Next() {
			var line string
			require.NoError(t, rows.Scan(&line))
			lines = append(lines, line)
		}
		require.NoError(t, rows.Err())
		rows.Close()
		plan := strings.Join(lines, "\n")
		for _, index := range []string{"public_activity_sender_page_idx", "public_activity_recipient_page_idx", "public_activity_initiator_page_idx"} {
			require.Contains(t, plan, index, "source lookups must use address/page indexes:\n%s", plan)
		}
		require.GreaterOrEqual(t, strings.Count(plan, "Limit"), 4, "source candidates and the final result must remain bounded:\n%s", plan)
	}
}
