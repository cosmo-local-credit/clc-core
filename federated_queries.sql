-- Federated / Warehouse Queries
-- These queries target the federated PostgreSQL database.
-- Use fully qualified table names (schema_name.table_name)
-- since no default search_path is set on this connection.
--
-- Account balances and transfer history are served LIVE from the indexer's
-- source tables, filtered to a single holder:
--   chain_data_v2.token_transfer  — one row per on-chain token transfer
--   chain_data_v2.tx              — transaction envelope (hash, block, success)
--   chain_data_v2.tokens          — token metadata (symbol/name/decimals)
--   sarafu_network.vouchers       — voucher icon URLs
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

--name: legacy-user-exists-by-blockchain-address
SELECT EXISTS (
    SELECT 1
    FROM sarafu_network.accounts
    WHERE blockchain_address = $1
);

-- Token holdings ------------------------------------------------------------
-- A holder's balance for a token is the signed sum of every successful
-- movement (credited as recipient, debited as sender). Metadata and icon are
-- joined onto the aggregated holdings only — a handful of rows per holder — so
-- the join cost is negligible. Tokens absent from chain_data_v2.tokens are
-- dropped (INNER JOIN), matching the previous view semantics. `id` is the
-- holder's most recent tx id for that token and drives keyset pagination.

--name: list-user-token-holdings
WITH movements AS (
    SELECT tt.contract_address, tt.transfer_value AS delta, tx.id AS tx_id, tx.date_block
    FROM chain_data_v2.token_transfer tt
    JOIN chain_data_v2.tx ON tx.id = tt.tx_id
    WHERE tx.success = true AND tt.recipient_address = $1
    UNION ALL
    SELECT tt.contract_address, -tt.transfer_value AS delta, tx.id AS tx_id, tx.date_block
    FROM chain_data_v2.token_transfer tt
    JOIN chain_data_v2.tx ON tx.id = tt.tx_id
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
    t.token_symbol,
    t.token_name,
    t.token_decimals,
    h.balance::text AS balance,
    v.icon_url,
    h.last_date_block
FROM holding h
JOIN chain_data_v2.tokens t ON t.contract_address = h.contract_address
LEFT JOIN sarafu_network.vouchers v ON v.voucher_address = h.contract_address
ORDER BY h.last_tx_id DESC
LIMIT $2;

--name: list-user-token-holdings-next
WITH movements AS (
    SELECT tt.contract_address, tt.transfer_value AS delta, tx.id AS tx_id, tx.date_block
    FROM chain_data_v2.token_transfer tt
    JOIN chain_data_v2.tx ON tx.id = tt.tx_id
    WHERE tx.success = true AND tt.recipient_address = $1
    UNION ALL
    SELECT tt.contract_address, -tt.transfer_value AS delta, tx.id AS tx_id, tx.date_block
    FROM chain_data_v2.token_transfer tt
    JOIN chain_data_v2.tx ON tx.id = tt.tx_id
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
    t.token_symbol,
    t.token_name,
    t.token_decimals,
    h.balance::text AS balance,
    v.icon_url,
    h.last_date_block
FROM holding h
JOIN chain_data_v2.tokens t ON t.contract_address = h.contract_address
LEFT JOIN sarafu_network.vouchers v ON v.voucher_address = h.contract_address
WHERE h.last_tx_id < $2
ORDER BY h.last_tx_id DESC
LIMIT $3;

--name: list-user-token-holdings-prev
WITH movements AS (
    SELECT tt.contract_address, tt.transfer_value AS delta, tx.id AS tx_id, tx.date_block
    FROM chain_data_v2.token_transfer tt
    JOIN chain_data_v2.tx ON tx.id = tt.tx_id
    WHERE tx.success = true AND tt.recipient_address = $1
    UNION ALL
    SELECT tt.contract_address, -tt.transfer_value AS delta, tx.id AS tx_id, tx.date_block
    FROM chain_data_v2.token_transfer tt
    JOIN chain_data_v2.tx ON tx.id = tt.tx_id
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
        t.token_symbol,
        t.token_name,
        t.token_decimals,
        h.balance::text AS balance,
        v.icon_url,
        h.last_date_block
    FROM holding h
    JOIN chain_data_v2.tokens t ON t.contract_address = h.contract_address
    LEFT JOIN sarafu_network.vouchers v ON v.voucher_address = h.contract_address
    WHERE h.last_tx_id > $2
    ORDER BY h.last_tx_id ASC
    LIMIT $3
) page
ORDER BY id DESC;

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
            t.token_symbol,
            t.token_name,
            t.token_decimals,
            tt.transfer_value::text AS amount,
            tx.success
        FROM chain_data_v2.token_transfer tt
        JOIN chain_data_v2.tx ON tx.id = tt.tx_id
        JOIN chain_data_v2.tokens t ON t.contract_address = tt.contract_address
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
            t.token_symbol,
            t.token_name,
            t.token_decimals,
            tt.transfer_value::text AS amount,
            tx.success
        FROM chain_data_v2.token_transfer tt
        JOIN chain_data_v2.tx ON tx.id = tt.tx_id
        JOIN chain_data_v2.tokens t ON t.contract_address = tt.contract_address
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
            t.token_symbol,
            t.token_name,
            t.token_decimals,
            tt.transfer_value::text AS amount,
            tx.success
        FROM chain_data_v2.token_transfer tt
        JOIN chain_data_v2.tx ON tx.id = tt.tx_id
        JOIN chain_data_v2.tokens t ON t.contract_address = tt.contract_address
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
            t.token_symbol,
            t.token_name,
            t.token_decimals,
            tt.transfer_value::text AS amount,
            tx.success
        FROM chain_data_v2.token_transfer tt
        JOIN chain_data_v2.tx ON tx.id = tt.tx_id
        JOIN chain_data_v2.tokens t ON t.contract_address = tt.contract_address
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
                t.token_symbol,
                t.token_name,
                t.token_decimals,
                tt.transfer_value::text AS amount,
                tx.success
            FROM chain_data_v2.token_transfer tt
            JOIN chain_data_v2.tx ON tx.id = tt.tx_id
            JOIN chain_data_v2.tokens t ON t.contract_address = tt.contract_address
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
                t.token_symbol,
                t.token_name,
                t.token_decimals,
                tt.transfer_value::text AS amount,
                tx.success
            FROM chain_data_v2.token_transfer tt
            JOIN chain_data_v2.tx ON tx.id = tt.tx_id
            JOIN chain_data_v2.tokens t ON t.contract_address = tt.contract_address
            WHERE tt.sender_address = $1 AND tt.sender_address <> tt.recipient_address AND tt.id > $2
            ORDER BY tt.id ASC
            LIMIT $3
        )
    ) merged
    ORDER BY id ASC
    LIMIT $3
) page
ORDER BY id DESC;
