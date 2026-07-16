--name: insert-keypair
INSERT INTO keystore(public_key, private_key) VALUES($1, $2) ON CONFLICT (public_key) DO NOTHING RETURNING id;

--name: activate-keypair
UPDATE keystore
SET active = true
WHERE public_key = $1;

--name: load-master-key
SELECT public_key, private_key FROM keystore
INNER JOIN (SELECT id FROM master_key LIMIT 1) master_key ON keystore.id = master_key.id;

--name: bootstrap-master-key
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

--name: acquire-nonce
WITH updated_nonce AS (
    UPDATE noncestore
    SET next_nonce = next_nonce + 1
    WHERE keystore_id = (
        SELECT id FROM keystore WHERE public_key = $1
    )
    RETURNING next_nonce - 1 AS current_nonce
)
SELECT current_nonce FROM updated_nonce;

--name: insert-otx
INSERT INTO otx(
    tracking_id,
    otx_type,
    signer_account,
    raw_tx,
    tx_hash,
    nonce
) VALUES($1, $2, (SELECT id FROM keystore WHERE public_key = $3), $4, $5, $6) RETURNING id;

--name: get-otx-by-tx-hash
SELECT otx.id, otx.tracking_id, otx.otx_type, otx.signer_account AS public_key, otx.raw_tx, otx.tx_hash, otx.nonce, otx.replaced, otx.created_at, otx.updated_at, dispatch.status FROM otx
INNER JOIN dispatch ON otx.id = dispatch.otx_id
WHERE otx.tx_hash = $1;

--name: insert-dispatch-tx
INSERT INTO dispatch(
    otx_id,
    status
) VALUES($1, $2) RETURNING id;

--name: update-dispatch-tx-status
UPDATE dispatch
SET status = $1
WHERE otx_id = $2;

--name: get-platform-contracts
SELECT id, chain_id, contract_type, factory_address, implementation_address, version, created_at, updated_at
FROM platform_contracts
WHERE chain_id = $1 AND contract_type = $2::contract_type;

--name: insert-deployed-token
INSERT INTO deployed_tokens (
    otx_id, platform_contract_id, status, symbol, name, decimals, admin,
    description, website, email, phone, voucher_uoa, voucher_value,
    location_name, geo_lat, geo_lng
) VALUES ($1, $2, 'PENDING', $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
RETURNING id;

--name: confirm-deployed-token
UPDATE deployed_tokens
SET address = $2, status = 'CONFIRMED'
WHERE otx_id = $1 AND status = 'PENDING'
RETURNING id;

--name: fail-deployed-token
UPDATE deployed_tokens
SET status = 'FAILED'
WHERE otx_id = $1 AND status = 'PENDING';

--name: insert-deployed-token-tag
INSERT INTO deployed_token_tags (deployed_token_id, tag_value) VALUES ($1, $2);

--name: get-deployed-token-by-address
SELECT dt.id, dt.otx_id, dt.platform_contract_id, dt.status, dt.address, dt.symbol, dt.name, dt.decimals, dt.admin,
       dt.description, dt.website, dt.email, dt.phone, dt.voucher_uoa, dt.voucher_value,
       dt.location_name, dt.geo_lat, dt.geo_lng, dt.created_at, dt.updated_at, o.tracking_id,
       COALESCE(array_agg(dtt.tag_value ORDER BY dtt.tag_value) FILTER (WHERE dtt.tag_value IS NOT NULL), ARRAY[]::TEXT[]) AS tags
FROM deployed_tokens dt
INNER JOIN otx o ON dt.otx_id = o.id
LEFT JOIN deployed_token_tags dtt ON dtt.deployed_token_id = dt.id
WHERE dt.address = $1 AND dt.status = 'CONFIRMED'
GROUP BY dt.id, o.tracking_id;

--name: get-deployed-token-by-tracking-id
SELECT dt.id, dt.otx_id, dt.platform_contract_id, dt.status, dt.address, dt.symbol, dt.name, dt.decimals, dt.admin,
       dt.description, dt.website, dt.email, dt.phone, dt.voucher_uoa, dt.voucher_value,
       dt.location_name, dt.geo_lat, dt.geo_lng, dt.created_at, dt.updated_at, o.tracking_id,
       COALESCE(array_agg(dtt.tag_value ORDER BY dtt.tag_value) FILTER (WHERE dtt.tag_value IS NOT NULL), ARRAY[]::TEXT[]) AS tags
FROM deployed_tokens dt
INNER JOIN otx o ON dt.otx_id = o.id
LEFT JOIN deployed_token_tags dtt ON dtt.deployed_token_id = dt.id
WHERE o.tracking_id = $1::uuid AND dt.status = 'CONFIRMED'
GROUP BY dt.id, o.tracking_id
LIMIT 1;

--name: list-token-tags
SELECT value FROM token_tag ORDER BY value;

-- Pool addresses (shared singleton addresses)
--name: get-pool-address
SELECT address FROM pool_addresses WHERE key = $1;

--name: set-pool-address
INSERT INTO pool_addresses (key, address) VALUES ($1, $2)
ON CONFLICT (key) DO UPDATE SET address = EXCLUDED.address, updated_at = CURRENT_TIMESTAMP;

-- Deployed pools
--name: insert-deployed-pool
INSERT INTO deployed_pools (
    otx_id, platform_contract_id, status, name, symbol, decimals,
    token_registry_address, limiter_address, fee_policy_address, quoter_address,
    protocol_fee_controller_address, fees_decoupled, admin,
    description, website, email, phone, location_name, geo_lat, geo_lng, image_url, banner_url
) VALUES ($1, $2, 'PENDING', $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)
RETURNING id;

--name: confirm-deployed-pool
UPDATE deployed_pools
SET address = $2, status = 'CONFIRMED'
WHERE otx_id = $1 AND status = 'PENDING'
RETURNING id;

--name: fail-deployed-pool
UPDATE deployed_pools
SET status = 'FAILED'
WHERE otx_id = $1 AND status = 'PENDING';

--name: insert-deployed-pool-tag
INSERT INTO deployed_pool_tags (deployed_pool_id, tag_value) VALUES ($1, $2);

--name: get-deployed-pool-by-address
SELECT dp.id, dp.otx_id, dp.platform_contract_id, dp.status, dp.address, dp.name, dp.symbol, dp.decimals,
    dp.token_registry_address, dp.limiter_address, dp.fee_policy_address, dp.quoter_address,
    dp.protocol_fee_controller_address, dp.fees_decoupled, dp.admin,
    dp.description, dp.website, dp.email, dp.phone, dp.location_name, dp.geo_lat, dp.geo_lng,
    dp.image_url, dp.banner_url, dp.created_at, dp.updated_at, o.tracking_id,
    COALESCE(array_agg(dpt.tag_value ORDER BY dpt.tag_value) FILTER (WHERE dpt.tag_value IS NOT NULL), ARRAY[]::TEXT[]) AS tags
FROM deployed_pools dp
INNER JOIN otx o ON dp.otx_id = o.id
LEFT JOIN deployed_pool_tags dpt ON dpt.deployed_pool_id = dp.id
WHERE dp.address = $1 AND dp.status = 'CONFIRMED'
GROUP BY dp.id, o.tracking_id;

--name: get-deployed-pool-by-tracking-id
SELECT dp.id, dp.otx_id, dp.platform_contract_id, dp.status, dp.address, dp.name, dp.symbol, dp.decimals,
    dp.token_registry_address, dp.limiter_address, dp.fee_policy_address, dp.quoter_address,
    dp.protocol_fee_controller_address, dp.fees_decoupled, dp.admin,
    dp.description, dp.website, dp.email, dp.phone, dp.location_name, dp.geo_lat, dp.geo_lng,
    dp.image_url, dp.banner_url, dp.created_at, dp.updated_at, o.tracking_id,
    COALESCE(array_agg(dpt.tag_value ORDER BY dpt.tag_value) FILTER (WHERE dpt.tag_value IS NOT NULL), ARRAY[]::TEXT[]) AS tags
FROM deployed_pools dp
INNER JOIN otx o ON dp.otx_id = o.id
LEFT JOIN deployed_pool_tags dpt ON dpt.deployed_pool_id = dp.id
WHERE o.tracking_id = $1::uuid AND dp.status = 'CONFIRMED'
GROUP BY dp.id, o.tracking_id
LIMIT 1;

-- Oracle feeds
--name: get-oracle-feed-by-name
SELECT id, name, address, created_at, updated_at
FROM oracle_feeds
WHERE name = $1;

--name: list-oracle-feeds
SELECT id, name, address, created_at, updated_at
FROM oracle_feeds
ORDER BY name;

--name: get-otx-by-tracking-id
SELECT otx.id, otx.tracking_id, otx.otx_type, k.public_key, otx.raw_tx, otx.tx_hash,
    otx.nonce, otx.replaced, otx.created_at, otx.updated_at, d.status
FROM otx
INNER JOIN dispatch d ON otx.id = d.otx_id
INNER JOIN keystore k ON otx.signer_account = k.id
WHERE otx.tracking_id = $1::uuid
ORDER BY otx.id;

--name: get-otx-by-account
SELECT otx.id, otx.tracking_id, otx.otx_type, k.public_key, otx.raw_tx, otx.tx_hash,
    otx.nonce, otx.replaced, otx.created_at, otx.updated_at, d.status
FROM otx
INNER JOIN dispatch d ON otx.id = d.otx_id
INNER JOIN keystore k ON otx.signer_account = k.id
WHERE k.public_key = $1
ORDER BY otx.id DESC
LIMIT $2;

--name: get-otx-by-account-next
-- Keyset window is fetched ascending (the rows immediately after the cursor)
-- then re-sorted descending in SQL so the API never reorders rows in memory.
SELECT * FROM (
    SELECT otx.id, otx.tracking_id, otx.otx_type, k.public_key, otx.raw_tx, otx.tx_hash,
        otx.nonce, otx.replaced, otx.created_at, otx.updated_at, d.status
    FROM otx
    INNER JOIN dispatch d ON otx.id = d.otx_id
    INNER JOIN keystore k ON otx.signer_account = k.id
    WHERE k.public_key = $1 AND otx.id > $2
    ORDER BY otx.id ASC
    LIMIT $3
) page
ORDER BY page.id DESC;

--name: get-otx-by-account-prev
SELECT otx.id, otx.tracking_id, otx.otx_type, k.public_key, otx.raw_tx, otx.tx_hash,
    otx.nonce, otx.replaced, otx.created_at, otx.updated_at, d.status
FROM otx
INNER JOIN dispatch d ON otx.id = d.otx_id
INNER JOIN keystore k ON otx.signer_account = k.id
WHERE k.public_key = $1 AND otx.id < $2
ORDER BY otx.id DESC
LIMIT $3;

--name: peek-nonce
SELECT next_nonce FROM noncestore
INNER JOIN keystore ON keystore.id = noncestore.keystore_id
WHERE keystore.public_key = $1;

--name: check-keypair
SELECT active FROM keystore WHERE public_key = $1;

-- Users
--name: insert-user
INSERT INTO users (blockchain_address, name, email, role, status, max_pools, max_tokens)
VALUES ($1, $2, NULLIF($3, ''), $4::user_role, $5::user_status, $6, $7)
RETURNING id, blockchain_address AS ethereum_address, name, COALESCE(email, '') AS email, COALESCE(phone, '') AS phone, COALESCE(country, '') AS country, role, status, max_pools, max_tokens, created_at, updated_at;

--name: get-user-by-address
SELECT id, blockchain_address AS ethereum_address, name, COALESCE(email, '') AS email, COALESCE(phone, '') AS phone, COALESCE(country, '') AS country, role, status, max_pools, max_tokens, created_at, updated_at
FROM users
WHERE blockchain_address = $1;

--name: get-user-by-id
SELECT id, blockchain_address AS ethereum_address, name, COALESCE(email, '') AS email, COALESCE(phone, '') AS phone, COALESCE(country, '') AS country, role, status, max_pools, max_tokens, created_at, updated_at
FROM users
WHERE id = $1;

--name: get-user-by-email
SELECT id, blockchain_address AS ethereum_address, name, COALESCE(email, '') AS email, COALESCE(phone, '') AS phone, COALESCE(country, '') AS country, role, status, max_pools, max_tokens, created_at, updated_at
FROM users
WHERE email = $1;

--name: update-user-status
UPDATE users
SET status = $2::user_status
WHERE blockchain_address = $1
RETURNING id, blockchain_address AS ethereum_address, name, COALESCE(email, '') AS email, COALESCE(phone, '') AS phone, COALESCE(country, '') AS country, role, status, max_pools, max_tokens, created_at, updated_at;

--name: approve-pending-user-by-address
UPDATE users
SET status = 'APPROVED'::user_status
WHERE blockchain_address = $1 AND status = 'PENDING'::user_status
RETURNING id, blockchain_address AS ethereum_address, name, COALESCE(email, '') AS email, COALESCE(phone, '') AS phone, COALESCE(country, '') AS country, role, status, max_pools, max_tokens, created_at, updated_at;

--name: update-user-limits
UPDATE users
SET max_pools = $2, max_tokens = $3
WHERE blockchain_address = $1
RETURNING id, blockchain_address AS ethereum_address, name, COALESCE(email, '') AS email, COALESCE(phone, '') AS phone, COALESCE(country, '') AS country, role, status, max_pools, max_tokens, created_at, updated_at;

--name: update-user-profile
UPDATE users
SET name = $2, email = NULLIF($3, ''), phone = NULLIF($4, ''), country = $5
WHERE blockchain_address = $1
RETURNING id, blockchain_address AS ethereum_address, name, COALESCE(email, '') AS email, COALESCE(phone, '') AS phone, COALESCE(country, '') AS country, role, status, max_pools, max_tokens, created_at, updated_at;

--name: list-users
SELECT id, blockchain_address AS ethereum_address, name, COALESCE(email, '') AS email, COALESCE(phone, '') AS phone, COALESCE(country, '') AS country, role, status, max_pools, max_tokens, created_at, updated_at
FROM users
ORDER BY created_at DESC;

--name: list-users-by-status
SELECT id, blockchain_address AS ethereum_address, name, COALESCE(email, '') AS email, COALESCE(phone, '') AS phone, COALESCE(country, '') AS country, role, status, max_pools, max_tokens, created_at, updated_at
FROM users
WHERE status = $1::user_status
ORDER BY created_at DESC;

-- User deployments
--name: count-user-tokens
SELECT COUNT(*) FROM user_deployments
WHERE user_id = $1 AND resource_type = 'TOKEN';

--name: count-user-pools
SELECT COUNT(*) FROM user_deployments
WHERE user_id = $1 AND resource_type = 'POOL';

--name: insert-user-deployment
INSERT INTO user_deployments (user_id, tracking_id, resource_type)
VALUES ($1, $2::uuid, $3)
RETURNING id;

--name: list-user-deployments
SELECT id, user_id, tracking_id, resource_type, created_at
FROM user_deployments
WHERE user_id = $1
ORDER BY created_at DESC;

--name: get-user-deployment-by-tracking-id
SELECT id, user_id, tracking_id, resource_type, created_at
FROM user_deployments
WHERE tracking_id = $1::uuid AND user_id = $2;

--name: list-all-deployments
SELECT ud.id, ud.user_id, ud.tracking_id, ud.resource_type, ud.created_at,
    u.blockchain_address AS ethereum_address, u.name AS user_name
FROM user_deployments ud
JOIN users u ON u.id = ud.user_id
ORDER BY ud.created_at DESC;

--name: list-tokens-by-user
SELECT dt.id, dt.otx_id, dt.platform_contract_id, dt.status, dt.address, dt.symbol, dt.name, dt.decimals, dt.admin, dt.created_at, dt.updated_at, o.tracking_id
FROM deployed_tokens dt
INNER JOIN otx o ON dt.otx_id = o.id
INNER JOIN user_deployments ud ON ud.tracking_id = o.tracking_id
WHERE ud.user_id = $1 AND ud.resource_type = 'TOKEN' AND dt.status = 'CONFIRMED'
ORDER BY dt.id DESC
LIMIT $2;

--name: list-tokens-by-user-next
-- Keyset window is fetched ascending (the rows immediately after the cursor)
-- then re-sorted descending in SQL so the API never reorders rows in memory.
SELECT * FROM (
    SELECT dt.id, dt.otx_id, dt.platform_contract_id, dt.status, dt.address, dt.symbol, dt.name, dt.decimals, dt.admin, dt.created_at, dt.updated_at, o.tracking_id
    FROM deployed_tokens dt
    INNER JOIN otx o ON dt.otx_id = o.id
    INNER JOIN user_deployments ud ON ud.tracking_id = o.tracking_id
    WHERE ud.user_id = $1 AND ud.resource_type = 'TOKEN' AND dt.status = 'CONFIRMED' AND dt.id > $2
    ORDER BY dt.id ASC
    LIMIT $3
) page
ORDER BY page.id DESC;

--name: list-tokens-by-user-prev
SELECT dt.id, dt.otx_id, dt.platform_contract_id, dt.status, dt.address, dt.symbol, dt.name, dt.decimals, dt.admin, dt.created_at, dt.updated_at, o.tracking_id
FROM deployed_tokens dt
INNER JOIN otx o ON dt.otx_id = o.id
INNER JOIN user_deployments ud ON ud.tracking_id = o.tracking_id
WHERE ud.user_id = $1 AND ud.resource_type = 'TOKEN' AND dt.status = 'CONFIRMED' AND dt.id < $2
ORDER BY dt.id DESC
LIMIT $3;

--name: list-pools-by-user
SELECT dp.id, dp.otx_id, dp.platform_contract_id, dp.status, dp.address, dp.name, dp.symbol, dp.decimals, dp.token_registry_address, dp.limiter_address, dp.fee_policy_address, dp.quoter_address, dp.protocol_fee_controller_address, dp.fees_decoupled, dp.admin, dp.image_url, dp.banner_url, dp.created_at, dp.updated_at, o.tracking_id
FROM deployed_pools dp
INNER JOIN otx o ON dp.otx_id = o.id
INNER JOIN user_deployments ud ON ud.tracking_id = o.tracking_id
WHERE ud.user_id = $1 AND ud.resource_type = 'POOL' AND dp.status = 'CONFIRMED'
ORDER BY dp.id DESC
LIMIT $2;

--name: list-pools-by-user-next
-- Keyset window is fetched ascending (the rows immediately after the cursor)
-- then re-sorted descending in SQL so the API never reorders rows in memory.
SELECT * FROM (
    SELECT dp.id, dp.otx_id, dp.platform_contract_id, dp.status, dp.address, dp.name, dp.symbol, dp.decimals, dp.token_registry_address, dp.limiter_address, dp.fee_policy_address, dp.quoter_address, dp.protocol_fee_controller_address, dp.fees_decoupled, dp.admin, dp.image_url, dp.banner_url, dp.created_at, dp.updated_at, o.tracking_id
    FROM deployed_pools dp
    INNER JOIN otx o ON dp.otx_id = o.id
    INNER JOIN user_deployments ud ON ud.tracking_id = o.tracking_id
    WHERE ud.user_id = $1 AND ud.resource_type = 'POOL' AND dp.status = 'CONFIRMED' AND dp.id > $2
    ORDER BY dp.id ASC
    LIMIT $3
) page
ORDER BY page.id DESC;

--name: list-pools-by-user-prev
SELECT dp.id, dp.otx_id, dp.platform_contract_id, dp.status, dp.address, dp.name, dp.symbol, dp.decimals, dp.token_registry_address, dp.limiter_address, dp.fee_policy_address, dp.quoter_address, dp.protocol_fee_controller_address, dp.fees_decoupled, dp.admin, dp.image_url, dp.banner_url, dp.created_at, dp.updated_at, o.tracking_id
FROM deployed_pools dp
INNER JOIN otx o ON dp.otx_id = o.id
INNER JOIN user_deployments ud ON ud.tracking_id = o.tracking_id
WHERE ud.user_id = $1 AND ud.resource_type = 'POOL' AND dp.status = 'CONFIRMED' AND dp.id < $2
ORDER BY dp.id DESC
LIMIT $3;

--name: list-all-tokens
SELECT id, otx_id, platform_contract_id, status, address, symbol, name, decimals, admin, created_at, updated_at
FROM deployed_tokens
WHERE status = 'CONFIRMED'
ORDER BY created_at DESC;

--name: list-all-pools
SELECT id, otx_id, platform_contract_id, status, address, name, symbol, decimals,
    token_registry_address, limiter_address, fee_policy_address, quoter_address,
    protocol_fee_controller_address, fees_decoupled, admin, image_url, banner_url, created_at, updated_at
FROM deployed_pools
WHERE status = 'CONFIRMED'
ORDER BY created_at DESC;

-- Set platform contracts implementation and version
--name: set-platform-contracts
UPDATE platform_contracts
SET implementation_address = $1, version = $2, updated_at = CURRENT_TIMESTAMP
WHERE chain_id = $3 AND contract_type = $4::contract_type
RETURNING id, chain_id, contract_type, factory_address, implementation_address, version, created_at, updated_at;

-- Get all platform contracts for a chain
--name: list-platform-contracts
SELECT id, chain_id, contract_type, factory_address, implementation_address, version, created_at, updated_at
FROM platform_contracts
WHERE chain_id = $1;

-- Insert upgrade meta
--name: insert-upgrade-meta
INSERT INTO upgrade_meta (otx_id, proxy_address, contract_type, new_implementation, version)
VALUES ($1, $2, $3::contract_type, $4, $5)
RETURNING id;

-- Get upgrade meta by OTX ID
--name: get-upgrade-meta-by-otx-id
SELECT id, otx_id, proxy_address, contract_type, new_implementation, version, created_at
FROM upgrade_meta
WHERE otx_id = $1;

-- Insert (idempotently) a WebAuthn credential registered during relayer
-- registration. Re-registering with the same credential_id is a no-op.
--name: upsert-webauthn-credential
INSERT INTO webauthn_credentials (credential_id, address, key_hash, public_key_x, public_key_y)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (credential_id) DO NOTHING;

-- Look up a WebAuthn credential by its credential_id for recovery flow.
--name: get-webauthn-credential
SELECT credential_id, address, key_hash, public_key_x::TEXT AS public_key_x, public_key_y::TEXT AS public_key_y, created_at
FROM webauthn_credentials
WHERE credential_id = $1;

-- Fast-forward the relayer noncestore to the on-chain pending nonce, if
-- the stored next_nonce is behind.
--name: set-nonce-if-higher
UPDATE noncestore
SET next_nonce = $2
WHERE keystore_id = (SELECT id FROM keystore WHERE public_key = $1)
  AND next_nonce < $2;
