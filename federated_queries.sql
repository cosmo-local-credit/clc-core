-- Federated / Warehouse Queries
-- These queries target the federated PostgreSQL database.
-- Use fully qualified table names (schema_name.table_name)
-- since no default search_path is set on this connection.
--
-- Account balances and transfer history are served from two materialized
-- views that are created and refreshed by clc-core (see FederationPg.Bootstrap
-- and StartMaintenance):
--   chain_data_v2.account_token_holding  — pre-aggregated (holder, token) balances
--   chain_data_v2.account_transfer       — per-holder expansion of every transfer
--
-- Token metadata (symbol/name/decimals) and voucher icon URLs are embedded
-- directly in the MVs so the read queries are join-free single-table scans —
-- the fastest possible path. The trade-off is that metadata and icons lag the
-- source tables by up to the refresh interval (2 minutes), which is acceptable.
-- Addresses are stored checksummed (EIP-55) throughout the federation schema,
-- so plain equality is used everywhere — no LOWER() normalisation.

--name: legacy-user-exists-by-blockchain-address
SELECT EXISTS (
    SELECT 1
    FROM sarafu_network.accounts
    WHERE blockchain_address = $1
);

--name: list-user-token-holdings
SELECT
    last_tx_id AS id,
    contract_address AS token_address,
    token_symbol,
    token_name,
    token_decimals,
    balance::text AS balance,
    icon_url,
    last_date_block
FROM chain_data_v2.account_token_holding
WHERE holder_address = $1
ORDER BY last_tx_id DESC
LIMIT $2;

--name: list-user-token-holdings-next
SELECT
    last_tx_id AS id,
    contract_address AS token_address,
    token_symbol,
    token_name,
    token_decimals,
    balance::text AS balance,
    icon_url,
    last_date_block
FROM chain_data_v2.account_token_holding
WHERE holder_address = $1 AND last_tx_id < $2
ORDER BY last_tx_id DESC
LIMIT $3;

--name: list-user-token-holdings-prev
SELECT *
FROM (
    SELECT
        last_tx_id AS id,
        contract_address AS token_address,
        token_symbol,
        token_name,
        token_decimals,
        balance::text AS balance,
        icon_url,
        last_date_block
    FROM chain_data_v2.account_token_holding
    WHERE holder_address = $1 AND last_tx_id > $2
    ORDER BY last_tx_id ASC
    LIMIT $3
) page
ORDER BY id DESC;

--name: list-user-transfer-history
SELECT
    transfer_id AS id,
    tx_hash,
    date_block,
    direction,
    counterparty_address,
    sender_address,
    recipient_address,
    contract_address,
    token_symbol,
    token_name,
    token_decimals,
    transfer_value::text AS amount,
    success
FROM chain_data_v2.account_transfer
WHERE holder_address = $1
ORDER BY transfer_id DESC
LIMIT $2;

--name: list-user-transfer-history-next
SELECT
    transfer_id AS id,
    tx_hash,
    date_block,
    direction,
    counterparty_address,
    sender_address,
    recipient_address,
    contract_address,
    token_symbol,
    token_name,
    token_decimals,
    transfer_value::text AS amount,
    success
FROM chain_data_v2.account_transfer
WHERE holder_address = $1 AND transfer_id < $2
ORDER BY transfer_id DESC
LIMIT $3;

--name: list-user-transfer-history-prev
SELECT *
FROM (
    SELECT
        transfer_id AS id,
        tx_hash,
        date_block,
        direction,
        counterparty_address,
        sender_address,
        recipient_address,
        contract_address,
        token_symbol,
        token_name,
        token_decimals,
        transfer_value::text AS amount,
        success
    FROM chain_data_v2.account_transfer
    WHERE holder_address = $1 AND transfer_id > $2
    ORDER BY transfer_id ASC
    LIMIT $3
) page
ORDER BY id DESC;

-- Schema DDL -----------------------------------------------------------------
-- The following statements are executed once at federation-store bootstrap
-- (FederationPg.Bootstrap). They are idempotent (IF NOT EXISTS) and run
-- outside a transaction via pool.Exec. CREATE MATERIALIZED VIEW populates the
-- view on first creation; subsequent restarts skip it. The unique indexes
-- satisfy the REFRESH MATERIALIZED VIEW CONCURRENTLY requirement and serve the
-- keyset-pagination scans.

--name: create-account-token-holding-mv
CREATE MATERIALIZED VIEW IF NOT EXISTS chain_data_v2.account_token_holding AS
WITH movements AS (
    SELECT
        tt.recipient_address AS holder_address,
        tt.contract_address,
        tt.transfer_value AS delta,
        tx.id AS tx_id,
        tx.date_block
    FROM chain_data_v2.token_transfer tt
    JOIN chain_data_v2.tx tx ON tx.id = tt.tx_id
    WHERE tx.success = true
    UNION ALL
    SELECT
        tt.sender_address AS holder_address,
        tt.contract_address,
        -tt.transfer_value AS delta,
        tx.id AS tx_id,
        tx.date_block
    FROM chain_data_v2.token_transfer tt
    JOIN chain_data_v2.tx tx ON tx.id = tt.tx_id
    WHERE tx.success = true
)
SELECT
    m.holder_address,
    m.contract_address,
    SUM(m.delta) AS balance,
    MAX(m.tx_id) AS last_tx_id,
    MAX(m.date_block) AS last_date_block,
    t.token_symbol,
    t.token_name,
    t.token_decimals,
    v.icon_url
FROM movements m
JOIN chain_data_v2.tokens t ON t.contract_address = m.contract_address
LEFT JOIN sarafu_network.vouchers v ON v.voucher_address = m.contract_address
GROUP BY m.holder_address, m.contract_address, t.token_symbol, t.token_name, t.token_decimals, v.icon_url
HAVING SUM(m.delta) > 0;

--name: create-account-token-holding-holder-token-idx
CREATE UNIQUE INDEX IF NOT EXISTS account_token_holding_holder_token_idx
    ON chain_data_v2.account_token_holding (holder_address, contract_address);

--name: create-account-token-holding-holder-last-tx-idx
CREATE INDEX IF NOT EXISTS account_token_holding_holder_last_tx_idx
    ON chain_data_v2.account_token_holding (holder_address, last_tx_id DESC);

--name: create-account-transfer-mv
CREATE MATERIALIZED VIEW IF NOT EXISTS chain_data_v2.account_transfer AS
SELECT
    tt.recipient_address AS holder_address,
    tt.id AS transfer_id,
    tx.id AS tx_id,
    tx.tx_hash,
    tx.date_block,
    tx.success,
    'IN' AS direction,
    tt.sender_address AS counterparty_address,
    tt.sender_address,
    tt.recipient_address,
    tt.contract_address,
    tt.transfer_value,
    t.token_symbol,
    t.token_name,
    t.token_decimals
FROM chain_data_v2.token_transfer tt
JOIN chain_data_v2.tx ON tx.id = tt.tx_id
JOIN chain_data_v2.tokens t ON t.contract_address = tt.contract_address
UNION ALL
SELECT
    tt.sender_address AS holder_address,
    tt.id AS transfer_id,
    tx.id AS tx_id,
    tx.tx_hash,
    tx.date_block,
    tx.success,
    'OUT' AS direction,
    tt.recipient_address AS counterparty_address,
    tt.sender_address,
    tt.recipient_address,
    tt.contract_address,
    tt.transfer_value,
    t.token_symbol,
    t.token_name,
    t.token_decimals
FROM chain_data_v2.token_transfer tt
JOIN chain_data_v2.tx ON tx.id = tt.tx_id
JOIN chain_data_v2.tokens t ON t.contract_address = tt.contract_address
WHERE tt.sender_address <> tt.recipient_address;

--name: create-account-transfer-holder-transfer-idx
CREATE UNIQUE INDEX IF NOT EXISTS account_transfer_holder_transfer_idx
    ON chain_data_v2.account_transfer (holder_address, transfer_id);

-- Maintenance ----------------------------------------------------------------
-- REFRESH ... CONCURRENTLY cannot run inside a transaction; it is executed via
-- pool.Exec from FederationPg.refreshViews on a periodic timer. It requires the
-- unique indexes declared above.

--name: refresh-account-token-holding-mv
REFRESH MATERIALIZED VIEW CONCURRENTLY chain_data_v2.account_token_holding;

--name: refresh-account-transfer-mv
REFRESH MATERIALIZED VIEW CONCURRENTLY chain_data_v2.account_transfer;
