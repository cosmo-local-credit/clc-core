-- Federated / Warehouse Queries
-- These run on the federation connection (federation.dsn) and read the
-- eth-indexer tables. Table names are deliberately unqualified so the DSN
-- picks the schema (`?search_path=indexer` when the indexer shares the
-- clc-core database, or a postgres_fdw schema on a separate warehouse).
--
-- Account balances and transfer history are served LIVE from the indexer's
-- source tables, filtered to a single holder:
--   token_transfer  — one row per on-chain token transfer
--   tx              — transaction envelope (hash, block, success)
--
-- These queries return CHAIN FACTS ONLY. Token metadata (symbol, name,
-- decimals, icon) is enriched from the LOCAL vouchers catalog by the API
-- layer (the catalog is the single home for living
-- voucher attributes) — the former joins to tokens and
-- sarafu_network.vouchers are gone. A side effect is that holdings and
-- transfers are no longer silently dropped for tokens the federation
-- metadata tables did not know.
--
-- These reads used to be served from two materialized views refreshed on a
-- 2-minute timer, which made every write invisible to the API for up to that
-- interval. The access pattern is per-account (WHERE holder = $1), which prunes
-- to a small row set, so we query the source tables directly instead — reads
-- are realtime with no refresh job. This relies on indexes on
-- token_transfer(recipient_address) and token_transfer(sender_address); for
-- optimal keyset pagination prefer composite (recipient_address, id) and
-- (sender_address, id).
--
-- Addresses are stored checksummed (EIP-55) throughout the federation schema,
-- so plain equality is used everywhere — no LOWER() normalisation.

-- sarafu_network.accounts exists only where Sarafu legacy history was imported.
-- Elsewhere the store maps undefined_table to false (see FederationPg).
--name: legacy-user-exists-by-blockchain-address
SELECT EXISTS (
    SELECT 1
    FROM sarafu_network.accounts
    WHERE blockchain_address = $1
);

-- Token holdings ------------------------------------------------------------
-- A holder's balance for a token is the signed sum of every successful
-- movement (credited as recipient, debited as sender). Token metadata is NOT
-- joined here — the API enriches rows from the local vouchers catalog. `id`
-- is the holder's most recent tx id for that token and drives keyset
-- pagination.

-- Keep a transaction boundary intact: one transaction can touch several tokens.
-- The numeric cursor advances by transaction ID, so tied rows must share a page.
--name: list-user-token-holdings
WITH movements AS (
    SELECT tt.contract_address, tt.transfer_value AS delta, tx.id AS tx_id, tx.date_block
    FROM token_transfer tt
    JOIN tx ON tx.id = tt.tx_id
    WHERE tx.success = true AND tt.recipient_address = $1
    UNION ALL
    SELECT tt.contract_address, -tt.transfer_value AS delta, tx.id AS tx_id, tx.date_block
    FROM token_transfer tt
    JOIN tx ON tx.id = tt.tx_id
    WHERE tx.success = true AND tt.sender_address = $1
),
holding AS (
    SELECT
        m.contract_address,
        SUM(m.delta) AS balance,
        MAX(m.tx_id) AS last_tx_id,
        MAX(m.date_block) AS last_date_block
    FROM movements m
    GROUP BY m.contract_address
    HAVING SUM(m.delta) > 0
)
SELECT
    h.last_tx_id AS id,
    h.contract_address AS token_address,
    h.balance::text AS balance,
    h.last_date_block
FROM holding h
ORDER BY h.last_tx_id DESC
FETCH FIRST $2 ROWS WITH TIES;

--name: list-user-token-holdings-next
WITH movements AS (
    SELECT tt.contract_address, tt.transfer_value AS delta, tx.id AS tx_id, tx.date_block
    FROM token_transfer tt
    JOIN tx ON tx.id = tt.tx_id
    WHERE tx.success = true AND tt.recipient_address = $1
    UNION ALL
    SELECT tt.contract_address, -tt.transfer_value AS delta, tx.id AS tx_id, tx.date_block
    FROM token_transfer tt
    JOIN tx ON tx.id = tt.tx_id
    WHERE tx.success = true AND tt.sender_address = $1
),
holding AS (
    SELECT
        m.contract_address,
        SUM(m.delta) AS balance,
        MAX(m.tx_id) AS last_tx_id,
        MAX(m.date_block) AS last_date_block
    FROM movements m
    GROUP BY m.contract_address
    HAVING SUM(m.delta) > 0
)
SELECT
    h.last_tx_id AS id,
    h.contract_address AS token_address,
    h.balance::text AS balance,
    h.last_date_block
FROM holding h
WHERE h.last_tx_id < $2
ORDER BY h.last_tx_id DESC
FETCH FIRST $3 ROWS WITH TIES;

--name: list-user-token-holdings-prev
WITH movements AS (
    SELECT tt.contract_address, tt.transfer_value AS delta, tx.id AS tx_id, tx.date_block
    FROM token_transfer tt
    JOIN tx ON tx.id = tt.tx_id
    WHERE tx.success = true AND tt.recipient_address = $1
    UNION ALL
    SELECT tt.contract_address, -tt.transfer_value AS delta, tx.id AS tx_id, tx.date_block
    FROM token_transfer tt
    JOIN tx ON tx.id = tt.tx_id
    WHERE tx.success = true AND tt.sender_address = $1
),
holding AS (
    SELECT
        m.contract_address,
        SUM(m.delta) AS balance,
        MAX(m.tx_id) AS last_tx_id,
        MAX(m.date_block) AS last_date_block
    FROM movements m
    GROUP BY m.contract_address
    HAVING SUM(m.delta) > 0
)
SELECT * FROM (
    SELECT
        h.last_tx_id AS id,
        h.contract_address AS token_address,
        h.balance::text AS balance,
        h.last_date_block
    FROM holding h
    WHERE h.last_tx_id > $2
    ORDER BY h.last_tx_id ASC
    FETCH FIRST $3 ROWS WITH TIES
) page
ORDER BY id DESC;

-- A holder's balance of each named token, zero balances included, for the
-- wallet's issued list: an owner who never received their voucher, or handed
-- out every unit, holds none of it and the holdings above leave it out. A
-- token the holder never moved has no row. Not paginated — the caller passes
-- one page of tokens.
--name: list-user-token-balances
WITH movements AS (
    SELECT tt.contract_address, tt.transfer_value AS delta, tx.date_block
    FROM token_transfer tt
    JOIN tx ON tx.id = tt.tx_id
    WHERE tx.success = true AND tt.recipient_address = $1 AND tt.contract_address = ANY($2::text[])
    UNION ALL
    SELECT tt.contract_address, -tt.transfer_value AS delta, tx.date_block
    FROM token_transfer tt
    JOIN tx ON tx.id = tt.tx_id
    WHERE tx.success = true AND tt.sender_address = $1 AND tt.contract_address = ANY($2::text[])
)
SELECT
    m.contract_address AS token_address,
    SUM(m.delta)::text AS balance,
    MAX(m.date_block) AS last_date_block
FROM movements m
GROUP BY m.contract_address;

-- Transfer history ----------------------------------------------------------
-- Each on-chain transfer yields at most one row for a given holder — an IN row
-- when they are the recipient (self-transfers included) or an OUT row when they
-- are the sender (self-transfers excluded on the OUT side to avoid a duplicate,
-- matching the previous view). Each arm is individually keyset-paginated and
-- LIMIT-bounded so the merge only ever sorts 2*perPage rows.

--name: list-user-transfer-history
SELECT * FROM (
    (
        SELECT
            tt.id AS id,
            tx.tx_hash,
            tx.date_block,
            'IN' AS direction,
            tt.sender_address AS counterparty_address,
            tt.sender_address,
            tt.recipient_address,
            tt.contract_address,
            tt.transfer_value::text AS amount,
            tx.success
        FROM token_transfer tt
        JOIN tx ON tx.id = tt.tx_id
        WHERE tt.recipient_address = $1
        ORDER BY tt.id DESC
        LIMIT $2
    )
    UNION ALL
    (
        SELECT
            tt.id AS id,
            tx.tx_hash,
            tx.date_block,
            'OUT' AS direction,
            tt.recipient_address AS counterparty_address,
            tt.sender_address,
            tt.recipient_address,
            tt.contract_address,
            tt.transfer_value::text AS amount,
            tx.success
        FROM token_transfer tt
        JOIN tx ON tx.id = tt.tx_id
        WHERE tt.sender_address = $1 AND tt.sender_address <> tt.recipient_address
        ORDER BY tt.id DESC
        LIMIT $2
    )
) merged
ORDER BY id DESC
LIMIT $2;

--name: list-user-transfer-history-next
SELECT * FROM (
    (
        SELECT
            tt.id AS id,
            tx.tx_hash,
            tx.date_block,
            'IN' AS direction,
            tt.sender_address AS counterparty_address,
            tt.sender_address,
            tt.recipient_address,
            tt.contract_address,
            tt.transfer_value::text AS amount,
            tx.success
        FROM token_transfer tt
        JOIN tx ON tx.id = tt.tx_id
        WHERE tt.recipient_address = $1 AND tt.id < $2
        ORDER BY tt.id DESC
        LIMIT $3
    )
    UNION ALL
    (
        SELECT
            tt.id AS id,
            tx.tx_hash,
            tx.date_block,
            'OUT' AS direction,
            tt.recipient_address AS counterparty_address,
            tt.sender_address,
            tt.recipient_address,
            tt.contract_address,
            tt.transfer_value::text AS amount,
            tx.success
        FROM token_transfer tt
        JOIN tx ON tx.id = tt.tx_id
        WHERE tt.sender_address = $1 AND tt.sender_address <> tt.recipient_address AND tt.id < $2
        ORDER BY tt.id DESC
        LIMIT $3
    )
) merged
ORDER BY id DESC
LIMIT $3;

--name: list-user-transfer-history-prev
SELECT * FROM (
    SELECT * FROM (
        (
            SELECT
                tt.id AS id,
                tx.tx_hash,
                tx.date_block,
                'IN' AS direction,
                tt.sender_address AS counterparty_address,
                tt.sender_address,
                tt.recipient_address,
                tt.contract_address,
                tt.transfer_value::text AS amount,
                tx.success
            FROM token_transfer tt
            JOIN tx ON tx.id = tt.tx_id
            WHERE tt.recipient_address = $1 AND tt.id > $2
            ORDER BY tt.id ASC
            LIMIT $3
        )
        UNION ALL
        (
            SELECT
                tt.id AS id,
                tx.tx_hash,
                tx.date_block,
                'OUT' AS direction,
                tt.recipient_address AS counterparty_address,
                tt.sender_address,
                tt.recipient_address,
                tt.contract_address,
                tt.transfer_value::text AS amount,
                tx.success
            FROM token_transfer tt
            JOIN tx ON tx.id = tt.tx_id
            WHERE tt.sender_address = $1 AND tt.sender_address <> tt.recipient_address AND tt.id > $2
            ORDER BY tt.id ASC
            LIMIT $3
        )
    ) merged
    ORDER BY id ASC
    LIMIT $3
) page
ORDER BY id DESC;

-- Logical account activity --------------------------------------------------
--name: latest-pool-review-swap
SELECT max(tx.date_block AT TIME ZONE 'UTC')
FROM pool_swap ps
JOIN tx ON tx.id = ps.tx_id
WHERE ps.contract_address = ANY($1::text[])
  AND ps.initiator_address = ANY($2::text[])
  AND ps.in_value > 0
  AND ps.out_value > 0
  AND tx.success
  AND tx.date_block <= (CURRENT_TIMESTAMP AT TIME ZONE 'UTC');

-- Unlike transfer history, this feed returns one row per user-visible action.
-- All pool_swap rows initiated by the account in one transaction are one route:
-- the first input and final output are what the user sent and received. Direct
-- transfer rows consumed by those route endpoints are excluded, while unrelated
-- transfers batched into the same transaction remain visible.
--
-- $1 account, $2 selected kinds (empty = all), $3 voucher addresses matching q,
-- $4 normalized q, $5 optional cursor, $6 next direction, $7 page size.
--name: list-user-activity
WITH swap_routes AS (
    SELECT
        ps.tx_id,
        (tx.id::bigint * 1000000 + MIN(ps.log_index)::bigint) AS id,
        tx.tx_hash,
        tx.date_block,
        tx.success,
        (ARRAY_AGG(ps.token_in_address ORDER BY ps.log_index ASC))[1] AS token_in_address,
        (ARRAY_AGG(ps.in_value::text ORDER BY ps.log_index ASC))[1] AS amount_in,
        (ARRAY_AGG(ps.token_out_address ORDER BY ps.log_index DESC))[1] AS token_out_address,
        (ARRAY_AGG(ps.out_value::text ORDER BY ps.log_index DESC))[1] AS amount_out,
        (ARRAY_AGG(ps.fee::text ORDER BY ps.log_index DESC))[1] AS fee,
        ARRAY_AGG(ps.contract_address ORDER BY ps.log_index ASC) AS pool_addresses,
        (ARRAY_AGG(ps.contract_address ORDER BY ps.log_index ASC))[1] AS first_pool_address,
        (ARRAY_AGG(ps.contract_address ORDER BY ps.log_index DESC))[1] AS last_pool_address
    FROM pool_swap ps
    JOIN tx ON tx.id = ps.tx_id
    WHERE ps.initiator_address = $1
    GROUP BY ps.tx_id, tx.id, tx.tx_hash, tx.date_block, tx.success
),
direct_events AS (
    SELECT
        (tx.id::bigint * 1000000 + tt.log_index::bigint) AS id,
        CASE
            WHEN tt.sender_address = '0x0000000000000000000000000000000000000000' THEN 'creation'
            WHEN tt.recipient_address = $1 THEN 'received'
            ELSE 'sent'
        END AS kind,
        tx.tx_hash,
        tx.date_block,
        tx.success,
        CASE
            WHEN tt.recipient_address = $1 THEN tt.sender_address
            ELSE tt.recipient_address
        END AS counterparty_address,
        tt.sender_address,
        tt.recipient_address,
        tt.contract_address AS token_address,
        tt.transfer_value::text AS amount,
        NULL::text AS token_in_address,
        NULL::text AS amount_in,
        NULL::text AS token_out_address,
        NULL::text AS amount_out,
        NULL::text AS fee,
        NULL::text[] AS pool_addresses
    FROM token_transfer tt
    JOIN tx ON tx.id = tt.tx_id
    WHERE (tt.recipient_address = $1 OR tt.sender_address = $1)
      AND NOT EXISTS (
          SELECT 1
          FROM swap_routes sr
          WHERE sr.tx_id = tt.tx_id
            AND (
                (tt.sender_address = $1
                    AND tt.recipient_address = sr.first_pool_address
                    AND tt.contract_address = sr.token_in_address)
                OR
                (tt.recipient_address = $1
                    AND tt.sender_address = sr.last_pool_address
                    AND tt.contract_address = sr.token_out_address)
            )
      )
),
swap_events AS (
    SELECT
        sr.id,
        'swapped'::text AS kind,
        sr.tx_hash,
        sr.date_block,
        sr.success,
        NULL::text AS counterparty_address,
        NULL::text AS sender_address,
        NULL::text AS recipient_address,
        NULL::text AS token_address,
        NULL::text AS amount,
        sr.token_in_address,
        sr.amount_in,
        sr.token_out_address,
        sr.amount_out,
        sr.fee,
        sr.pool_addresses
    FROM swap_routes sr
),
events AS (
    SELECT * FROM direct_events
    UNION ALL
    SELECT * FROM swap_events
),
filtered AS (
    SELECT *
    FROM events e
    WHERE (cardinality($2::text[]) = 0 OR e.kind = ANY($2::text[]))
      AND (
          $4 = ''
          OR lower(e.tx_hash) LIKE '%' || lower($4) || '%'
          OR lower(COALESCE(e.counterparty_address, '')) LIKE '%' || lower($4) || '%'
          OR lower(COALESCE(e.token_address, '')) LIKE '%' || lower($4) || '%'
          OR lower(COALESCE(e.token_in_address, '')) LIKE '%' || lower($4) || '%'
          OR lower(COALESCE(e.token_out_address, '')) LIKE '%' || lower($4) || '%'
          OR lower(COALESCE(e.token_address, '')) = ANY($3::text[])
          OR lower(COALESCE(e.token_in_address, '')) = ANY($3::text[])
          OR lower(COALESCE(e.token_out_address, '')) = ANY($3::text[])
          OR EXISTS (
              SELECT 1 FROM unnest(COALESCE(e.pool_addresses, ARRAY[]::text[])) pool_address
              WHERE lower(pool_address) LIKE '%' || lower($4) || '%'
          )
      )
      AND (
          $5::bigint IS NULL
          OR ($6 AND e.id < $5::bigint)
          OR (NOT $6 AND e.id > $5::bigint)
      )
),
page AS (
    SELECT *
    FROM filtered
    ORDER BY
        CASE WHEN NOT $6 THEN id END ASC,
        CASE WHEN $6 THEN id END DESC
    LIMIT $7
)
SELECT * FROM page ORDER BY id DESC;

-- Filled envelope series from tx only. One foreign scan:
-- postgres_fdw ships the date filter but not GROUP BY, so the remote
-- returns date_block rows and we bucket locally. A separate COUNT(*) is
-- another scan of the same window — the handler sums this series instead.
-- date_block is timestamp without time zone: bind $1/$2 as timestamp, not
-- timestamptz, or the remote date filter is dropped.
-- Sampled 1d ~110ms exec / 28d ~300ms / 90d ~600ms (fetch_size 100).
-- $1 since, $2 until, $3 bucket seconds.
-- extract() needs explicit types: untyped $1 is extract(unknown, unknown)
-- (SQLSTATE 42725). Cast only in extract; leave the WHERE args bare so FDW
-- still pushes a timestamp comparison.
--name: dashboard-transaction-series
WITH buckets AS (
    SELECT g AS bucket_epoch
    FROM generate_series(
        (floor(extract(epoch from $1::timestamp) / $3::bigint) * $3::bigint)::bigint,
        (extract(epoch from $2::timestamp)::bigint - 1),
        $3::bigint
    ) AS g
),
counts AS (
    SELECT (floor(extract(epoch from date_block) / $3::bigint) * $3::bigint)::bigint AS bucket_epoch,
        COUNT(*)::bigint AS n
    FROM tx
    WHERE date_block >= $1 AND date_block < $2
    GROUP BY 1
)
SELECT to_timestamp(b.bucket_epoch) AS t,
    COALESCE(c.n, 0) AS transactions
FROM buckets b
LEFT JOIN counts c ON c.bucket_epoch = b.bucket_epoch
ORDER BY b.bucket_epoch;

-- The operator console's transaction pages are NOT here. Their SQL is assembled
-- in internal/store/admin_transactions.go, one small query per event table, from
-- literal table and column names held in that file. A named query per
-- (table x direction x filter) combination would be eighteen near-identical
-- statements for the voucher page alone; the arms differ only in their
-- projection, so they are composed rather than copied. Only the keyset bounds
-- and the contract filter are bound as parameters.

--name: account-activity-counts
-- Successful transactions only: a reverted transfer moved nothing, so counting it
-- would overstate both the total and the partner list.
SELECT
    (SELECT COUNT(*)
        FROM token_transfer tt
        JOIN tx ON tx.id = tt.tx_id
        WHERE tx.success
            AND (lower(tt.sender_address) = lower($1)
                OR lower(tt.recipient_address) = lower($1))) AS transactions,
    (SELECT COUNT(*)
        FROM pool_swap ps
        JOIN tx ON tx.id = ps.tx_id
        WHERE tx.success AND lower(ps.initiator_address) = lower($1)) AS swaps,
    (SELECT COUNT(*) FROM (
        SELECT lower(tt.recipient_address) AS party
        FROM token_transfer tt
        JOIN tx ON tx.id = tt.tx_id
        WHERE tx.success AND lower(tt.sender_address) = lower($1)
        UNION
        SELECT lower(tt.sender_address)
        FROM token_transfer tt
        JOIN tx ON tx.id = tt.tx_id
        WHERE tx.success AND lower(tt.recipient_address) = lower($1)
    ) parties WHERE party <> '' AND party <> lower($1)) AS partners;
