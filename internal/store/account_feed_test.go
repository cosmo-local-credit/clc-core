package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/stretchr/testify/require"
)

func TestNormalizeAccountActivityKinds(t *testing.T) {
	kinds, err := NormalizeAccountActivityKinds([]string{" SWAPPED,sent ", "creation", "sent"})
	require.NoError(t, err)
	require.Equal(t, []string{"creation", "sent", "swapped"}, kinds)

	kinds, err = NormalizeAccountActivityKinds(nil)
	require.NoError(t, err)
	require.Empty(t, kinds)

	_, err = NormalizeAccountActivityKinds([]string{"received,unknown"})
	require.EqualError(t, err, "types contains an unsupported activity type")
}

func TestAccountActivityQueryGroupsAndFiltersBeforePaging(t *testing.T) {
	queries, err := loadFederationQueries("../../federated_queries.sql")
	require.NoError(t, err)
	query := queries.ListUserActivity
	for _, fragment := range []string{
		"swap_routes AS",
		"ARRAY_AGG(ps.token_in_address ORDER BY ps.log_index ASC)",
		"ARRAY_AGG(ps.token_out_address ORDER BY ps.log_index DESC)",
		"cardinality($2::text[]) = 0 OR e.kind = ANY($2::text[])",
		"$5::bigint IS NULL",
		"LIMIT $7",
	} {
		require.Contains(t, query, fragment)
	}
	require.Less(t, strings.Index(query, "filtered AS"), strings.Index(query, "page AS"))
	require.Less(t, strings.Index(query, "page AS"), strings.LastIndex(query, "LIMIT $7"))
}

func TestLatestPoolReviewSwapQueryRequiresSuccessfulPositiveSwap(t *testing.T) {
	queries, err := loadFederationQueries("../../federated_queries.sql")
	require.NoError(t, err)
	query := queries.LatestPoolReviewSwap
	for _, fragment := range []string{
		"ps.contract_address = ANY($1::text[])",
		"ps.initiator_address = ANY($2::text[])",
		"ps.in_value > 0",
		"ps.out_value > 0",
		"tx.success",
		"tx.date_block <=",
	} {
		require.Contains(t, query, fragment)
	}
}

func TestIndexedAddressVariantsCoverIndexerCasing(t *testing.T) {
	variants := indexedAddressVariants("0x00000000000000000000000000000000000000aB")
	require.Equal(t, []string{
		"0x00000000000000000000000000000000000000ab",
		"0x00000000000000000000000000000000000000AB",
	}, variants)
}

func TestLatestPoolReviewSwapQueryQualifiesEveryRouteLegDB(t *testing.T) {
	_, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()
	queries, err := loadFederationQueries("../../federated_queries.sql")
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `
		CREATE TEMP TABLE tx(id BIGINT PRIMARY KEY, date_block TIMESTAMP NOT NULL, success BOOLEAN NOT NULL) ON COMMIT DROP;
		CREATE TEMP TABLE pool_swap(
			tx_id BIGINT NOT NULL, log_index BIGINT NOT NULL, contract_address TEXT NOT NULL,
			initiator_address TEXT NOT NULL, in_value NUMERIC NOT NULL, out_value NUMERIC NOT NULL
		) ON COMMIT DROP;
		INSERT INTO tx VALUES
			(1, CURRENT_TIMESTAMP AT TIME ZONE 'UTC' - interval '2 hours', false),
			(2, CURRENT_TIMESTAMP AT TIME ZONE 'UTC' - interval '1 hour', true),
			(3, CURRENT_TIMESTAMP AT TIME ZONE 'UTC' + interval '1 hour', true);
		INSERT INTO pool_swap VALUES
			(1, 1, '0x00000000000000000000000000000000000000a1', '0x00000000000000000000000000000000000000b1', 10, 9),
			(2, 1, '0x00000000000000000000000000000000000000a1', '0x00000000000000000000000000000000000000b1', 10, 9),
			(2, 2, '0x00000000000000000000000000000000000000a2', '0x00000000000000000000000000000000000000b1', 9, 8),
			(2, 3, '0x00000000000000000000000000000000000000a3', '0x00000000000000000000000000000000000000b1', 0, 8),
			(3, 1, '0x00000000000000000000000000000000000000a1', '0x00000000000000000000000000000000000000b1', 10, 9)`)
	require.NoError(t, err)

	for _, pool := range []string{
		"0x00000000000000000000000000000000000000a1",
		"0x00000000000000000000000000000000000000a2",
	} {
		var latest *time.Time
		require.NoError(t, tx.QueryRow(ctx, queries.LatestPoolReviewSwap, indexedAddressVariants(pool), indexedAddressVariants("0x00000000000000000000000000000000000000b1")).Scan(&latest))
		require.NotNil(t, latest, pool)
		require.WithinDuration(t, time.Now().Add(-time.Hour), latest.UTC(), time.Minute)
	}
	var invalid *time.Time
	require.NoError(t, tx.QueryRow(
		ctx,
		queries.LatestPoolReviewSwap,
		indexedAddressVariants("0x00000000000000000000000000000000000000a3"),
		indexedAddressVariants("0x00000000000000000000000000000000000000b1"),
	).Scan(&invalid))
	require.Nil(t, invalid)

	_, err = tx.Exec(ctx, `
		CREATE INDEX activity_review_swap_idx ON pool_swap(contract_address, initiator_address);
		ANALYZE pool_swap;
		SET LOCAL enable_seqscan = off`)
	require.NoError(t, err)
	rows, err := tx.Query(
		ctx,
		"EXPLAIN (COSTS OFF) "+queries.LatestPoolReviewSwap,
		indexedAddressVariants("0x00000000000000000000000000000000000000a1"),
		indexedAddressVariants("0x00000000000000000000000000000000000000b1"),
	)
	require.NoError(t, err)
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		plan = append(plan, line)
	}
	require.NoError(t, rows.Err())
	require.Contains(t, strings.Join(plan, "\n"), "activity_review_swap_idx")
}

func TestAccountActivityGroupsSwapRoutesAndKeepsUnrelatedTransfersDB(t *testing.T) {
	_, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()
	queries, err := loadFederationQueries("../../federated_queries.sql")
	require.NoError(t, err)

	_, err = tx.Exec(ctx, `
        CREATE TEMP TABLE pools(contract_address TEXT) ON COMMIT DROP;
        CREATE TEMP TABLE pool_withdrawal(id BIGINT, tx_id BIGINT, contract_address TEXT, kind TEXT,
            initiator_address TEXT, token_address TEXT, recipient_address TEXT, amount NUMERIC, log_index BIGINT) ON COMMIT DROP;
        CREATE TEMP TABLE tx(
            id BIGINT, tx_hash TEXT, success BOOLEAN, date_block TIMESTAMP
        ) ON COMMIT DROP;
        CREATE TEMP TABLE token_transfer(
            tx_id BIGINT, log_index BIGINT, contract_address TEXT,
            transfer_value NUMERIC, sender_address TEXT, recipient_address TEXT
        ) ON COMMIT DROP;
        CREATE TEMP TABLE pool_swap(
            tx_id BIGINT, log_index BIGINT, contract_address TEXT, initiator_address TEXT,
            token_in_address TEXT, token_out_address TEXT,
            in_value NUMERIC, out_value NUMERIC, fee NUMERIC
        ) ON COMMIT DROP;

        INSERT INTO tx VALUES
            (1,'0xcreation',true,'2026-10-01'),
            (2,'0xreceived',true,'2026-10-02'),
            (3,'0xsent',true,'2026-10-03'),
            (4,'0xswap',true,'2026-10-04');
        INSERT INTO token_transfer VALUES
            (1,1,'token-create',10,'0x0000000000000000000000000000000000000000','user'),
            (2,1,'token-received',11,'friend','user'),
            (3,1,'token-sent',12,'user','friend'),
            (4,1,'token-a',13,'user','pool-1'),
            (4,2,'token-b',14,'pool-1','pool-2'),
            (4,3,'token-c',15,'pool-2','user'),
            (4,50,'token-extra',16,'user','friend');
        INSERT INTO pool_swap VALUES
            (4,10,'pool-1','user','token-a','token-b',13,14,1),
            (4,20,'pool-2','user','token-b','token-c',14,15,2);
    `)
	require.NoError(t, err)

	query := func(kinds, tokens []string, term string, cursor any, next bool, limit int) []FederationAccountActivity {
		t.Helper()
		var rows []FederationAccountActivity
		require.NoError(t, pgxscan.Select(
			ctx, tx, &rows, queries.ListUserActivity,
			"user", kinds, tokens, term, cursor, next, limit, []string{},
		))
		return rows
	}

	all := query([]string{}, []string{}, "", nil, true, 20)
	require.Len(t, all, 5)
	require.Equal(t, []string{"sent", "swapped", "sent", "received", "creation"}, []string{
		all[0].Kind, all[1].Kind, all[2].Kind, all[3].Kind, all[4].Kind,
	})
	require.Equal(t, "token-a", *all[1].TokenInAddress)
	require.Equal(t, "13", *all[1].AmountIn)
	require.Equal(t, "token-c", *all[1].TokenOutAddress)
	require.Equal(t, "15", *all[1].AmountOut)
	require.Equal(t, []string{"pool-1", "pool-2"}, all[1].PoolAddresses)
	require.Equal(t, "token-extra", *all[0].TokenAddress, "unrelated batched transfer is preserved")

	swaps := query([]string{AccountActivitySwapped}, []string{}, "", nil, true, 20)
	require.Len(t, swaps, 1)
	require.Equal(t, AccountActivitySwapped, swaps[0].Kind)
	searched := query([]string{}, []string{"token-c"}, "voucher name", nil, true, 20)
	require.Len(t, searched, 1)
	require.Equal(t, AccountActivitySwapped, searched[0].Kind)

	first := query([]string{}, []string{}, "", nil, true, 2)
	require.Len(t, first, 2)
	second := query([]string{}, []string{}, "", int64(first[1].ID), true, 2)
	require.Len(t, second, 2)
	for _, row := range second {
		require.Less(t, row.ID, first[1].ID)
	}

	_, err = tx.Exec(ctx, `
        -- Sparse-account fixture: prove lookup behavior with unrelated history,
        -- rather than forcing a particular index on a two-row source table.
        INSERT INTO pool_swap SELECT n,1,'unrelated-pool','other-'||n,'other-in','other-out',1,1,0
            FROM generate_series(1000,21000) n;
        CREATE INDEX activity_transfer_recipient_idx ON token_transfer(lower(recipient_address));
        CREATE INDEX activity_transfer_sender_idx ON token_transfer(lower(sender_address));
        CREATE INDEX activity_swap_initiator_idx ON pool_swap(lower(initiator_address));
        CREATE INDEX activity_swap_contract_idx ON pool_swap(lower(contract_address));
        CREATE INDEX activity_swap_tx_idx ON pool_swap(tx_id);
        ANALYZE token_transfer;
        ANALYZE pool_swap;
        SET LOCAL enable_seqscan = off;
    `)
	require.NoError(t, err)
	planRows, err := tx.Query(
		ctx,
		"EXPLAIN (COSTS OFF) "+queries.ListUserActivity,
		"user", []string{}, []string{}, "", nil, true, 20, []string{},
	)
	require.NoError(t, err)
	defer planRows.Close()
	var planLines []string
	for planRows.Next() {
		var line string
		require.NoError(t, planRows.Scan(&line))
		planLines = append(planLines, line)
	}
	require.NoError(t, planRows.Err())
	plan := strings.Join(planLines, "\n")
	require.Contains(t, plan, "activity_swap_initiator_idx")
	require.Contains(t, plan, "activity_transfer_recipient_idx")
	require.Contains(t, plan, "activity_transfer_sender_idx")
}
