-- Federated / Warehouse Queries
-- These queries target the federated PostgreSQL database.
-- Use fully qualified table names (schema_name.table_name)
-- since no default search_path is set on this connection.

--name: legacy-user-exists-by-blockchain-address
SELECT EXISTS (
    SELECT 1
    FROM sarafu_network.accounts
    WHERE blockchain_address = $1
);

--name: list-user-token-holdings
WITH movements AS (
    SELECT tt.contract_address, tt.transfer_value AS delta, tx.id AS tx_id, tx.date_block
    FROM chain_data_v2.token_transfer tt
    JOIN chain_data_v2.tx tx ON tx.id = tt.tx_id
    WHERE tx.success = true AND tt.recipient_address = $1

    UNION ALL

    SELECT tt.contract_address, -tt.transfer_value AS delta, tx.id AS tx_id, tx.date_block
    FROM chain_data_v2.token_transfer tt
    JOIN chain_data_v2.tx tx ON tx.id = tt.tx_id
    WHERE tx.success = true AND tt.sender_address = $1
), balances AS (
    SELECT
        contract_address,
        SUM(delta) AS balance,
        MAX(tx_id) AS last_tx_id,
        MAX(date_block) AS last_date_block
    FROM movements
    GROUP BY contract_address
    HAVING SUM(delta) > 0
)
SELECT
    balances.last_tx_id AS id,
    tokens.contract_address AS token_address,
    tokens.token_symbol,
    tokens.token_name,
    tokens.token_decimals,
    balances.balance::text AS balance,
    vouchers.icon_url,
    balances.last_date_block
FROM balances
JOIN chain_data_v2.tokens tokens ON tokens.contract_address = balances.contract_address
LEFT JOIN sarafu_network.vouchers vouchers ON vouchers.voucher_address = tokens.contract_address
ORDER BY balances.last_tx_id DESC
LIMIT $2;

--name: list-user-token-holdings-next
WITH movements AS (
    SELECT tt.contract_address, tt.transfer_value AS delta, tx.id AS tx_id, tx.date_block
    FROM chain_data_v2.token_transfer tt
    JOIN chain_data_v2.tx tx ON tx.id = tt.tx_id
    WHERE tx.success = true AND tt.recipient_address = $1

    UNION ALL

    SELECT tt.contract_address, -tt.transfer_value AS delta, tx.id AS tx_id, tx.date_block
    FROM chain_data_v2.token_transfer tt
    JOIN chain_data_v2.tx tx ON tx.id = tt.tx_id
    WHERE tx.success = true AND tt.sender_address = $1
), balances AS (
    SELECT
        contract_address,
        SUM(delta) AS balance,
        MAX(tx_id) AS last_tx_id,
        MAX(date_block) AS last_date_block
    FROM movements
    GROUP BY contract_address
    HAVING SUM(delta) > 0
)
SELECT
    balances.last_tx_id AS id,
    tokens.contract_address AS token_address,
    tokens.token_symbol,
    tokens.token_name,
    tokens.token_decimals,
    balances.balance::text AS balance,
    vouchers.icon_url,
    balances.last_date_block
FROM balances
JOIN chain_data_v2.tokens tokens ON tokens.contract_address = balances.contract_address
LEFT JOIN sarafu_network.vouchers vouchers ON vouchers.voucher_address = tokens.contract_address
WHERE balances.last_tx_id < $2
ORDER BY balances.last_tx_id DESC
LIMIT $3;

--name: list-user-token-holdings-prev
WITH movements AS (
    SELECT tt.contract_address, tt.transfer_value AS delta, tx.id AS tx_id, tx.date_block
    FROM chain_data_v2.token_transfer tt
    JOIN chain_data_v2.tx tx ON tx.id = tt.tx_id
    WHERE tx.success = true AND tt.recipient_address = $1

    UNION ALL

    SELECT tt.contract_address, -tt.transfer_value AS delta, tx.id AS tx_id, tx.date_block
    FROM chain_data_v2.token_transfer tt
    JOIN chain_data_v2.tx tx ON tx.id = tt.tx_id
    WHERE tx.success = true AND tt.sender_address = $1
), balances AS (
    SELECT
        contract_address,
        SUM(delta) AS balance,
        MAX(tx_id) AS last_tx_id,
        MAX(date_block) AS last_date_block
    FROM movements
    GROUP BY contract_address
    HAVING SUM(delta) > 0
)
SELECT *
FROM (
    SELECT
        balances.last_tx_id AS id,
        tokens.contract_address AS token_address,
        tokens.token_symbol,
        tokens.token_name,
        tokens.token_decimals,
        balances.balance::text AS balance,
        vouchers.icon_url,
        balances.last_date_block
    FROM balances
    JOIN chain_data_v2.tokens tokens ON tokens.contract_address = balances.contract_address
    LEFT JOIN sarafu_network.vouchers vouchers ON vouchers.voucher_address = tokens.contract_address
    WHERE balances.last_tx_id > $2
    ORDER BY balances.last_tx_id ASC
    LIMIT $3
) page
ORDER BY id DESC;

--name: list-user-transfer-history
SELECT
    token_transfer.id,
    tx.tx_hash,
    tx.date_block,
    CASE
        WHEN token_transfer.recipient_address = $1 THEN 'IN'
        ELSE 'OUT'
    END AS direction,
    CASE
        WHEN token_transfer.recipient_address = $1 THEN token_transfer.sender_address
        ELSE token_transfer.recipient_address
    END AS counterparty_address,
    token_transfer.sender_address,
    token_transfer.recipient_address,
    token_transfer.contract_address,
    tokens.token_symbol,
    tokens.token_name,
    tokens.token_decimals,
    token_transfer.transfer_value::text AS amount,
    tx.success
FROM chain_data_v2.token_transfer
JOIN chain_data_v2.tx ON tx.id = token_transfer.tx_id
JOIN chain_data_v2.tokens tokens ON tokens.contract_address = token_transfer.contract_address
WHERE token_transfer.sender_address = $1 OR token_transfer.recipient_address = $1
ORDER BY token_transfer.id DESC
LIMIT $2;

--name: list-user-transfer-history-next
SELECT
    token_transfer.id,
    tx.tx_hash,
    tx.date_block,
    CASE
        WHEN token_transfer.recipient_address = $1 THEN 'IN'
        ELSE 'OUT'
    END AS direction,
    CASE
        WHEN token_transfer.recipient_address = $1 THEN token_transfer.sender_address
        ELSE token_transfer.recipient_address
    END AS counterparty_address,
    token_transfer.sender_address,
    token_transfer.recipient_address,
    token_transfer.contract_address,
    tokens.token_symbol,
    tokens.token_name,
    tokens.token_decimals,
    token_transfer.transfer_value::text AS amount,
    tx.success
FROM chain_data_v2.token_transfer
JOIN chain_data_v2.tx ON tx.id = token_transfer.tx_id
JOIN chain_data_v2.tokens tokens ON tokens.contract_address = token_transfer.contract_address
WHERE (token_transfer.sender_address = $1 OR token_transfer.recipient_address = $1)
  AND token_transfer.id < $2
ORDER BY token_transfer.id DESC
LIMIT $3;

--name: list-user-transfer-history-prev
SELECT *
FROM (
    SELECT
        token_transfer.id,
        tx.tx_hash,
        tx.date_block,
        CASE
            WHEN token_transfer.recipient_address = $1 THEN 'IN'
            ELSE 'OUT'
        END AS direction,
        CASE
            WHEN token_transfer.recipient_address = $1 THEN token_transfer.sender_address
            ELSE token_transfer.recipient_address
        END AS counterparty_address,
        token_transfer.sender_address,
        token_transfer.recipient_address,
        token_transfer.contract_address,
        tokens.token_symbol,
        tokens.token_name,
        tokens.token_decimals,
        token_transfer.transfer_value::text AS amount,
        tx.success
    FROM chain_data_v2.token_transfer
    JOIN chain_data_v2.tx ON tx.id = token_transfer.tx_id
    JOIN chain_data_v2.tokens tokens ON tokens.contract_address = token_transfer.contract_address
    WHERE (token_transfer.sender_address = $1 OR token_transfer.recipient_address = $1)
      AND token_transfer.id > $2
    ORDER BY token_transfer.id ASC
    LIMIT $3
) page
ORDER BY id DESC;
