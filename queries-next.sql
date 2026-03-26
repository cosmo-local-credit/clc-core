--name: insert-keypair
-- Save hex encoded private key
-- $1: public_key
-- $2: private_key
INSERT INTO keystore(public_key, private_key) VALUES($1, $2) RETURNING id;

--name: activate-keypair
-- Save hex encoded private key
-- $1: public_key
UPDATE keystore
SET active = true
WHERE public_key = $1;

--name: load-key
-- Load saved key pair
-- $1: public_key
SELECT public_key, private_key FROM keystore WHERE public_key=$1;

--name: check-keypair
-- Check if a key exists in the keystore and is activated
-- $1: public_key
SELECT active FROM keystore WHERE public_key=$1;

--name: load-master-key
-- Load saved master key pair
SELECT public_key, private_key FROM keystore
INNER JOIN (SELECT id FROM master_key LIMIT 1) master_key ON keystore.id = master_key.id;

--name: bootstrap-master-key
-- Save newely hex encoded private key to be used as a master key
-- $1: public_key
-- $2: private_key
WITH new_key AS (
    INSERT INTO keystore (public_key, private_key, active)
    SELECT $1, $2, true
    WHERE NOT EXISTS (
        SELECT 1 FROM master_key
    )
    RETURNING id
)
INSERT INTO master_key (id)
SELECT id FROM new_key
RETURNING id;

--name: peek-nonce
-- Get current nonce
-- $1: public_key
WITH peek_nonce AS (
    SELECT
        CASE
            WHEN next_nonce = 0 THEN 0
            ELSE next_nonce - 1
        END AS current_nonce
    FROM noncestore
    WHERE keystore_id = (
        SELECT id FROM keystore WHERE public_key = $1
    )
)
SELECT COALESCE(current_nonce, 0) AS current_nonce
FROM peek_nonce;

--name: acquire-nonce
-- Fetch next nonce and decrement by 1 to get the current nonce
-- $1: public_key
WITH updated_nonce AS (
    UPDATE noncestore
    SET next_nonce = next_nonce + 1
    WHERE keystore_id = (
        SELECT id FROM keystore WHERE public_key = $1
    )
    RETURNING next_nonce - 1 AS current_nonce
)
SELECT current_nonce FROM updated_nonce;

--name: set-account-nonce
-- Set accout nonce manually
-- $1: public_key
-- $2: nonce_value
UPDATE noncestore
SET next_nonce = $2
WHERE keystore_id = (
    SELECT id FROM keystore WHERE public_key = $1
);

--name: insert-otx
-- Create a new locally originating tx
-- $1: tracking_id
-- $2: otx_type
-- $3: signer_account
-- $4: raw_tx
-- $5: tx_hash
-- $6: nonce
INSERT INTO otx(
    tracking_id,
    otx_type,
    signer_account,
    raw_tx,
    tx_hash,
    nonce
) VALUES($1, $2, (SELECT id FROM keystore WHERE public_key = $3), $4, $5, $6) RETURNING id;

--name: get-otx-by-tx-hash
-- Get OTX by tracking id
-- $1: tx_hash
SELECT otx.id, otx.tracking_id, otx.otx_type, otx.signer_account AS public_key, otx.raw_tx, otx.tx_hash, otx.nonce, otx.replaced, otx.created_at, otx.updated_at, dispatch.status FROM otx
INNER JOIN dispatch ON otx.id = dispatch.otx_id
WHERE otx.tx_hash = $1;

--name: get-otx-by-tracking-id
-- Get OTX by tracking id
-- $1: tracking_id
SELECT otx.id, otx.tracking_id, otx.otx_type, keystore.public_key, otx.raw_tx, otx.tx_hash, otx.nonce, otx.replaced, otx.created_at, otx.updated_at, dispatch.status FROM otx
INNER JOIN keystore ON otx.signer_account = keystore.id
INNER JOIN dispatch ON otx.id = dispatch.otx_id
WHERE otx.tracking_id = $1 ORDER BY otx.nonce ASC;

--name: get-otx-by-account
-- Get OTX by account
-- $1: public_key
-- $2: limit
SELECT otx.id, otx.tracking_id, otx.otx_type, keystore.public_key, otx.raw_tx, otx.tx_hash, otx.nonce, otx.replaced, otx.created_at, otx.updated_at, dispatch.status FROM keystore
INNER JOIN otx ON keystore.id = otx.signer_account
INNER JOIN dispatch ON otx.id = dispatch.otx_id
WHERE keystore.public_key = $1
ORDER BY otx.id ASC LIMIT $2;

--name: get-otx-by-account-next
-- Get OTX by account
-- $1: public_key
-- $2: cursor
-- $3: limit
SELECT otx.id, otx.tracking_id, otx.otx_type, keystore.public_key, otx.raw_tx, otx.tx_hash, otx.nonce, otx.replaced, otx.created_at, otx.updated_at, dispatch.status FROM keystore
INNER JOIN otx ON keystore.id = otx.signer_account
INNER JOIN dispatch ON otx.id = dispatch.otx_id
WHERE keystore.public_key = $1
AND otx.id > $2
ORDER BY otx.id ASC LIMIT $3;

--name: get-otx-by-account-previous
-- Get OTX by account
-- $1: public_key
-- $2: cursor
-- $3: limit
SELECT * FROM (
  SELECT otx.id, otx.tracking_id, otx.otx_type, keystore.public_key, otx.raw_tx, otx.tx_hash, otx.nonce, otx.replaced, otx.created_at, otx.updated_at, dispatch.status FROM keystore
	INNER JOIN otx ON keystore.id = otx.signer_account
    INNER JOIN dispatch ON otx.id = dispatch.otx_id
	WHERE keystore.public_key = $1
  AND otx.id < $2
  ORDER BY otx.id DESC LIMIT $3
) AS previous_page ORDER BY id ASC;

--name: insert-dispatch-tx
-- Create a new dispatch request
-- $1: otx_id
-- $2: status
INSERT INTO dispatch(
    otx_id,
    "status"
) VALUES($1, $2) RETURNING id;

--name: update-dispatch-tx-status
-- Create a new dispatch request
-- $1: status
-- $2: otx_id
UPDATE dispatch
SET "status" = $1
WHERE otx_id = $2;

--name: get-failed-otx
SELECT otx.id, otx.tracking_id, otx.otx_type, keystore.public_key, otx.raw_tx, otx.tx_hash, otx.nonce, otx.replaced, otx.created_at, otx.updated_at, dispatch.status FROM keystore
INNER JOIN otx ON keystore.id = otx.signer_account
INNER JOIN dispatch ON otx.id = dispatch.otx_id
WHERE dispatch.status NOT IN ('SUCCESS', 'REVERTED', 'PENDING') AND otx.otx_type NOT IN ('GENERIC_SIGN', 'OTHER_MANUAL')
ORDER BY otx.id ASC LIMIT 100;

--name: insert-next-erc20-workflow
INSERT INTO next_erc20_workflow(
    tracking_id,
    action,
    status,
    name,
    symbol,
    decimals,
    initial_supply,
    initial_mintee,
    owner,
    expiry_timestamp,
    factory_address,
    admin_address,
    proxy_address,
    implementation_address,
    previous_implementation_address,
    version,
    upgrade_calldata,
    implementation_tx_hash,
    action_tx_hash
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
    $11, $12, $13, $14, $15, $16, $17, $18, $19
);

--name: get-next-erc20-by-tracking-id
SELECT id, tracking_id, action, status, name, symbol, decimals, initial_supply, initial_mintee, owner, expiry_timestamp, factory_address, admin_address, proxy_address, implementation_address, previous_implementation_address, version, upgrade_calldata, implementation_tx_hash, action_tx_hash, created_at, updated_at
FROM next_erc20_workflow
WHERE tracking_id = $1;

--name: get-latest-next-erc20-by-symbol
SELECT id, tracking_id, action, status, name, symbol, decimals, initial_supply, initial_mintee, owner, expiry_timestamp, factory_address, admin_address, proxy_address, implementation_address, previous_implementation_address, version, upgrade_calldata, implementation_tx_hash, action_tx_hash, created_at, updated_at
FROM next_erc20_workflow
WHERE symbol = $1
ORDER BY version DESC, id DESC
LIMIT 1;

--name: get-latest-next-erc20-by-proxy
SELECT id, tracking_id, action, status, name, symbol, decimals, initial_supply, initial_mintee, owner, expiry_timestamp, factory_address, admin_address, proxy_address, implementation_address, previous_implementation_address, version, upgrade_calldata, implementation_tx_hash, action_tx_hash, created_at, updated_at
FROM next_erc20_workflow
WHERE proxy_address = $1
ORDER BY version DESC, id DESC
LIMIT 1;

--name: get-latest-successful-next-erc20-by-proxy
SELECT id, tracking_id, action, status, name, symbol, decimals, initial_supply, initial_mintee, owner, expiry_timestamp, factory_address, admin_address, proxy_address, implementation_address, previous_implementation_address, version, upgrade_calldata, implementation_tx_hash, action_tx_hash, created_at, updated_at
FROM next_erc20_workflow
WHERE proxy_address = $1 AND status = 'SUCCESS'
ORDER BY version DESC, id DESC
LIMIT 1;

--name: update-next-erc20-workflow
UPDATE next_erc20_workflow
SET status = $2,
    proxy_address = CASE WHEN $3 = '' THEN proxy_address ELSE $3 END,
    implementation_address = CASE WHEN $4 = '' THEN implementation_address ELSE $4 END,
    previous_implementation_address = CASE WHEN $5 = '' THEN previous_implementation_address ELSE $5 END,
    version = CASE WHEN $6 = 0 THEN version ELSE $6 END,
    implementation_tx_hash = CASE WHEN $7 = '' THEN implementation_tx_hash ELSE $7 END,
    action_tx_hash = CASE WHEN $8 = '' THEN action_tx_hash ELSE $8 END,
    updated_at = CURRENT_TIMESTAMP
WHERE tracking_id = $1;
