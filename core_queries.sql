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

--name: get-protocol-address
SELECT address FROM protocol_addresses WHERE key = $1;

--name: set-protocol-address
INSERT INTO protocol_addresses (key, address) VALUES ($1, $2)
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
WHERE lower(dp.address) = lower($1) AND dp.status = 'CONFIRMED'
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
SELECT dp.id, dp.otx_id, dp.platform_contract_id, dp.status, dp.address, dp.name, dp.symbol, dp.decimals,
    dp.token_registry_address, dp.limiter_address, dp.fee_policy_address, dp.quoter_address,
    dp.protocol_fee_controller_address, dp.fees_decoupled, dp.admin,
    dp.description, dp.website, dp.email, dp.phone, dp.location_name, dp.geo_lat, dp.geo_lng,
    dp.image_url, dp.banner_url, dp.created_at, dp.updated_at, o.tracking_id,
    COALESCE(array_agg(dpt.tag_value ORDER BY dpt.tag_value) FILTER (WHERE dpt.tag_value IS NOT NULL), ARRAY[]::TEXT[]) AS tags
FROM deployed_pools dp
INNER JOIN otx o ON dp.otx_id = o.id
INNER JOIN user_deployments ud ON ud.tracking_id = o.tracking_id
LEFT JOIN deployed_pool_tags dpt ON dpt.deployed_pool_id = dp.id
WHERE ud.user_id = $1 AND ud.resource_type = 'POOL' AND dp.status = 'CONFIRMED'
GROUP BY dp.id, o.tracking_id
ORDER BY dp.id DESC
LIMIT $2;

--name: list-pools-by-user-next
-- Same keyset direction as transfer history: next pages are older (id < cursor).
SELECT dp.id, dp.otx_id, dp.platform_contract_id, dp.status, dp.address, dp.name, dp.symbol, dp.decimals,
    dp.token_registry_address, dp.limiter_address, dp.fee_policy_address, dp.quoter_address,
    dp.protocol_fee_controller_address, dp.fees_decoupled, dp.admin,
    dp.description, dp.website, dp.email, dp.phone, dp.location_name, dp.geo_lat, dp.geo_lng,
    dp.image_url, dp.banner_url, dp.created_at, dp.updated_at, o.tracking_id,
    COALESCE(array_agg(dpt.tag_value ORDER BY dpt.tag_value) FILTER (WHERE dpt.tag_value IS NOT NULL), ARRAY[]::TEXT[]) AS tags
FROM deployed_pools dp
INNER JOIN otx o ON dp.otx_id = o.id
INNER JOIN user_deployments ud ON ud.tracking_id = o.tracking_id
LEFT JOIN deployed_pool_tags dpt ON dpt.deployed_pool_id = dp.id
WHERE ud.user_id = $1 AND ud.resource_type = 'POOL' AND dp.status = 'CONFIRMED' AND dp.id < $2
GROUP BY dp.id, o.tracking_id
ORDER BY dp.id DESC
LIMIT $3;

--name: list-pools-by-user-prev
-- Previous pages move toward newer records (id > cursor).
SELECT * FROM (
    SELECT dp.id, dp.otx_id, dp.platform_contract_id, dp.status, dp.address, dp.name, dp.symbol, dp.decimals,
        dp.token_registry_address, dp.limiter_address, dp.fee_policy_address, dp.quoter_address,
        dp.protocol_fee_controller_address, dp.fees_decoupled, dp.admin,
        dp.description, dp.website, dp.email, dp.phone, dp.location_name, dp.geo_lat, dp.geo_lng,
        dp.image_url, dp.banner_url, dp.created_at, dp.updated_at, o.tracking_id,
        COALESCE(array_agg(dpt.tag_value ORDER BY dpt.tag_value) FILTER (WHERE dpt.tag_value IS NOT NULL), ARRAY[]::TEXT[]) AS tags
    FROM deployed_pools dp
    INNER JOIN otx o ON dp.otx_id = o.id
    INNER JOIN user_deployments ud ON ud.tracking_id = o.tracking_id
    LEFT JOIN deployed_pool_tags dpt ON dpt.deployed_pool_id = dp.id
    WHERE ud.user_id = $1 AND ud.resource_type = 'POOL' AND dp.status = 'CONFIRMED' AND dp.id > $2
    GROUP BY dp.id, o.tracking_id
    ORDER BY dp.id ASC
    LIMIT $3
) page
ORDER BY page.id DESC;

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

-- Global confirmed pools (any user). Full metadata + tags for the app pools tab.
--name: list-confirmed-pools
SELECT dp.id, dp.otx_id, dp.platform_contract_id, dp.status, dp.address, dp.name, dp.symbol, dp.decimals,
    dp.token_registry_address, dp.limiter_address, dp.fee_policy_address, dp.quoter_address,
    dp.protocol_fee_controller_address, dp.fees_decoupled, dp.admin,
    dp.description, dp.website, dp.email, dp.phone, dp.location_name, dp.geo_lat, dp.geo_lng,
    dp.image_url, dp.banner_url, dp.created_at, dp.updated_at, o.tracking_id,
    COALESCE(array_agg(dpt.tag_value ORDER BY dpt.tag_value) FILTER (WHERE dpt.tag_value IS NOT NULL), ARRAY[]::TEXT[]) AS tags
FROM deployed_pools dp
INNER JOIN otx o ON dp.otx_id = o.id
LEFT JOIN deployed_pool_tags dpt ON dpt.deployed_pool_id = dp.id
WHERE dp.status = 'CONFIRMED'
GROUP BY dp.id, o.tracking_id
ORDER BY dp.id DESC
LIMIT $1;

--name: list-confirmed-pools-next
-- Next pages are older records (id < cursor).
SELECT dp.id, dp.otx_id, dp.platform_contract_id, dp.status, dp.address, dp.name, dp.symbol, dp.decimals,
    dp.token_registry_address, dp.limiter_address, dp.fee_policy_address, dp.quoter_address,
    dp.protocol_fee_controller_address, dp.fees_decoupled, dp.admin,
    dp.description, dp.website, dp.email, dp.phone, dp.location_name, dp.geo_lat, dp.geo_lng,
    dp.image_url, dp.banner_url, dp.created_at, dp.updated_at, o.tracking_id,
    COALESCE(array_agg(dpt.tag_value ORDER BY dpt.tag_value) FILTER (WHERE dpt.tag_value IS NOT NULL), ARRAY[]::TEXT[]) AS tags
FROM deployed_pools dp
INNER JOIN otx o ON dp.otx_id = o.id
LEFT JOIN deployed_pool_tags dpt ON dpt.deployed_pool_id = dp.id
WHERE dp.status = 'CONFIRMED' AND dp.id < $1
GROUP BY dp.id, o.tracking_id
ORDER BY dp.id DESC
LIMIT $2;

--name: list-confirmed-pools-prev
-- Previous pages move toward newer records (id > cursor).
SELECT * FROM (
    SELECT dp.id, dp.otx_id, dp.platform_contract_id, dp.status, dp.address, dp.name, dp.symbol, dp.decimals,
        dp.token_registry_address, dp.limiter_address, dp.fee_policy_address, dp.quoter_address,
        dp.protocol_fee_controller_address, dp.fees_decoupled, dp.admin,
        dp.description, dp.website, dp.email, dp.phone, dp.location_name, dp.geo_lat, dp.geo_lng,
        dp.image_url, dp.banner_url, dp.created_at, dp.updated_at, o.tracking_id,
        COALESCE(array_agg(dpt.tag_value ORDER BY dpt.tag_value) FILTER (WHERE dpt.tag_value IS NOT NULL), ARRAY[]::TEXT[]) AS tags
    FROM deployed_pools dp
    INNER JOIN otx o ON dp.otx_id = o.id
    LEFT JOIN deployed_pool_tags dpt ON dpt.deployed_pool_id = dp.id
    WHERE dp.status = 'CONFIRMED' AND dp.id > $1
    GROUP BY dp.id, o.tracking_id
    ORDER BY dp.id ASC
    LIMIT $2
) page
ORDER BY page.id DESC;

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

-- Voucher catalog metadata for a set of token addresses (case-insensitive).
-- Serves the token metadata previously joined from federation tables
-- (chain_data_v2.tokens / sarafu_network.vouchers) — the local catalog is the
-- single home for living voucher attributes (MIGRATION.md §2 V2).
--name: list-voucher-metadata-by-addresses
SELECT address, symbol, name, decimals, icon_url
FROM vouchers
WHERE lower(address) = ANY($1);

-- Pool catalog directory (MIGRATION.md §3.5) — reads come from the pools
-- catalog, not the deploy pipeline. Cursor-paginated newest-first by id,
-- tags aggregated from pool_tags.
--name: list-pools
SELECT p.id, p.chain_id, p.address, p.name, p.symbol, p.owner_address, p.fee_ppm,
    p.fee_address, p.token_registry_address, p.limiter_address, p.quoter_address,
    p.fee_policy_address, p.protocol_fee_controller_address, p.fees_decoupled,
    p.decimals, p.description, p.unit_of_account, p.location_name, p.geo_lat, p.geo_lng,
    p.email, p.website, p.phone, p.image_url, p.banner_url, p.terms,
    p.default_voucher_id, p.platform_contract_id, p.created_at, p.updated_at,
    (SELECT COUNT(*) FROM pool_tokens pv WHERE pv.pool_id = p.id AND pv.is_allowed) AS voucher_count,
    COALESCE(array_agg(pt.tag_value ORDER BY pt.tag_value) FILTER (WHERE pt.tag_value IS NOT NULL), ARRAY[]::TEXT[]) AS tags
FROM pools p
LEFT JOIN pool_tags pt ON pt.pool_id = p.id
GROUP BY p.id
ORDER BY p.id DESC
LIMIT $1;

--name: list-pools-next
-- Next pages are older records (id < cursor).
SELECT p.id, p.chain_id, p.address, p.name, p.symbol, p.owner_address, p.fee_ppm,
    p.fee_address, p.token_registry_address, p.limiter_address, p.quoter_address,
    p.fee_policy_address, p.protocol_fee_controller_address, p.fees_decoupled,
    p.decimals, p.description, p.unit_of_account, p.location_name, p.geo_lat, p.geo_lng,
    p.email, p.website, p.phone, p.image_url, p.banner_url, p.terms,
    p.default_voucher_id, p.platform_contract_id, p.created_at, p.updated_at,
    (SELECT COUNT(*) FROM pool_tokens pv WHERE pv.pool_id = p.id AND pv.is_allowed) AS voucher_count,
    COALESCE(array_agg(pt.tag_value ORDER BY pt.tag_value) FILTER (WHERE pt.tag_value IS NOT NULL), ARRAY[]::TEXT[]) AS tags
FROM pools p
LEFT JOIN pool_tags pt ON pt.pool_id = p.id
WHERE p.id < $1
GROUP BY p.id
ORDER BY p.id DESC
LIMIT $2;

--name: list-pools-prev
-- Previous pages move toward newer records (id > cursor).
SELECT * FROM (
    SELECT p.id, p.chain_id, p.address, p.name, p.symbol, p.owner_address, p.fee_ppm,
        p.fee_address, p.token_registry_address, p.limiter_address, p.quoter_address,
        p.fee_policy_address, p.protocol_fee_controller_address, p.fees_decoupled,
        p.decimals, p.description, p.unit_of_account, p.location_name, p.geo_lat, p.geo_lng,
        p.email, p.website, p.phone, p.image_url, p.banner_url, p.terms,
        p.default_voucher_id, p.platform_contract_id, p.created_at, p.updated_at,
        (SELECT COUNT(*) FROM pool_tokens pv WHERE pv.pool_id = p.id AND pv.is_allowed) AS voucher_count,
        COALESCE(array_agg(pt.tag_value ORDER BY pt.tag_value) FILTER (WHERE pt.tag_value IS NOT NULL), ARRAY[]::TEXT[]) AS tags
    FROM pools p
    LEFT JOIN pool_tags pt ON pt.pool_id = p.id
    WHERE p.id > $1
    GROUP BY p.id
    ORDER BY p.id ASC
    LIMIT $2
) page
ORDER BY id DESC;

-- Single pool catalog row by contract address (case-insensitive; stored
-- addresses are EIP-55, lookups may arrive in any casing).
--name: get-pool-by-address
SELECT p.id, p.chain_id, p.address, p.name, p.symbol, p.owner_address, p.fee_ppm,
    p.fee_address, p.token_registry_address, p.limiter_address, p.quoter_address,
    p.fee_policy_address, p.protocol_fee_controller_address, p.fees_decoupled,
    p.decimals, p.description, p.unit_of_account, p.location_name, p.geo_lat, p.geo_lng,
    p.email, p.website, p.phone, p.image_url, p.banner_url, p.terms,
    p.default_voucher_id, p.platform_contract_id, p.created_at, p.updated_at,
    (SELECT COUNT(*) FROM pool_tokens pv WHERE pv.pool_id = p.id AND pv.is_allowed) AS voucher_count,
    COALESCE(array_agg(pt.tag_value ORDER BY pt.tag_value) FILTER (WHERE pt.tag_value IS NOT NULL), ARRAY[]::TEXT[]) AS tags
FROM pools p
LEFT JOIN pool_tags pt ON pt.pool_id = p.id
WHERE lower(p.address) = lower($1)
GROUP BY p.id;

-- Pool voucher composition (MIGRATION.md §3.4). Oracle feeds replace legacy
-- fixed exchange rates; offering counts are local catalog facts.
--name: list-pool-vouchers
SELECT v.id, v.chain_id, v.address, v.name, v.symbol, v.decimals, v.owner_address,
    v.voucher_type, v.sink_address, v.redemption_address, v.description, v.voucher_uoa,
    v.voucher_value::TEXT AS voucher_value, v.location_name, v.geo_lat, v.geo_lng,
    v.email, v.website, v.phone, v.icon_url, v.banner_url, v.terms, v.contract_version,
    v.active, v.internal, pt.is_allowed, pt.token_limit::TEXT AS token_limit, pt.synced_at,
    oracle.id AS oracle_feed_id, oracle.name AS oracle_name, oracle.address AS oracle_address,
    (SELECT COUNT(*) FROM offerings o WHERE o.voucher_id = v.id) AS offer_count,
    (SELECT COUNT(*) FROM offerings o WHERE o.voucher_id = v.id AND o.available) AS available_offer_count,
    COALESCE((
        SELECT array_agg(vt.tag_value ORDER BY vt.tag_value)
        FROM voucher_tags vt
        WHERE vt.voucher_id = v.id
    ), ARRAY[]::TEXT[]) AS tags,
    v.created_at, v.updated_at
FROM pool_tokens pt
INNER JOIN pools p ON p.id = pt.pool_id
INNER JOIN vouchers v ON v.id = pt.voucher_id
LEFT JOIN oracle_feeds oracle ON oracle.id = pt.oracle_feed_id
WHERE lower(p.address) = lower($1) AND pt.is_allowed
ORDER BY v.id DESC
LIMIT $2;

--name: list-pool-vouchers-next
SELECT v.id, v.chain_id, v.address, v.name, v.symbol, v.decimals, v.owner_address,
    v.voucher_type, v.sink_address, v.redemption_address, v.description, v.voucher_uoa,
    v.voucher_value::TEXT AS voucher_value, v.location_name, v.geo_lat, v.geo_lng,
    v.email, v.website, v.phone, v.icon_url, v.banner_url, v.terms, v.contract_version,
    v.active, v.internal, pt.is_allowed, pt.token_limit::TEXT AS token_limit, pt.synced_at,
    oracle.id AS oracle_feed_id, oracle.name AS oracle_name, oracle.address AS oracle_address,
    (SELECT COUNT(*) FROM offerings o WHERE o.voucher_id = v.id) AS offer_count,
    (SELECT COUNT(*) FROM offerings o WHERE o.voucher_id = v.id AND o.available) AS available_offer_count,
    COALESCE((
        SELECT array_agg(vt.tag_value ORDER BY vt.tag_value)
        FROM voucher_tags vt
        WHERE vt.voucher_id = v.id
    ), ARRAY[]::TEXT[]) AS tags,
    v.created_at, v.updated_at
FROM pool_tokens pt
INNER JOIN pools p ON p.id = pt.pool_id
INNER JOIN vouchers v ON v.id = pt.voucher_id
LEFT JOIN oracle_feeds oracle ON oracle.id = pt.oracle_feed_id
WHERE lower(p.address) = lower($1) AND pt.is_allowed AND v.id < $2
ORDER BY v.id DESC
LIMIT $3;

--name: list-pool-vouchers-prev
SELECT * FROM (
    SELECT v.id, v.chain_id, v.address, v.name, v.symbol, v.decimals, v.owner_address,
        v.voucher_type, v.sink_address, v.redemption_address, v.description, v.voucher_uoa,
        v.voucher_value::TEXT AS voucher_value, v.location_name, v.geo_lat, v.geo_lng,
        v.email, v.website, v.phone, v.icon_url, v.banner_url, v.terms, v.contract_version,
        v.active, v.internal, pt.is_allowed, pt.token_limit::TEXT AS token_limit, pt.synced_at,
        oracle.id AS oracle_feed_id, oracle.name AS oracle_name, oracle.address AS oracle_address,
        (SELECT COUNT(*) FROM offerings o WHERE o.voucher_id = v.id) AS offer_count,
        (SELECT COUNT(*) FROM offerings o WHERE o.voucher_id = v.id AND o.available) AS available_offer_count,
        COALESCE((
            SELECT array_agg(vt.tag_value ORDER BY vt.tag_value)
            FROM voucher_tags vt
            WHERE vt.voucher_id = v.id
        ), ARRAY[]::TEXT[]) AS tags,
        v.created_at, v.updated_at
    FROM pool_tokens pt
    INNER JOIN pools p ON p.id = pt.pool_id
    INNER JOIN vouchers v ON v.id = pt.voucher_id
    LEFT JOIN oracle_feeds oracle ON oracle.id = pt.oracle_feed_id
    WHERE lower(p.address) = lower($1) AND pt.is_allowed AND v.id > $2
    ORDER BY v.id ASC
    LIMIT $3
) page
ORDER BY id DESC;

-- Marketplace offerings, enriched with voucher identity and shared tags.
--name: list-offerings
SELECT o.id, o.source_id, o.voucher_id, v.address AS voucher_address,
    v.name AS voucher_name, v.symbol AS voucher_symbol, v.decimals AS voucher_decimals,
    o.seller_address, o.commodity_name, o.commodity_type, o.description,
    o.quantity::TEXT AS quantity, o.unit, o.frequency, o.price::TEXT AS price,
    o.available, o.location_name, o.geo_lat, o.geo_lng, o.image_url,
    COALESCE((
        SELECT array_agg(ot.tag_value ORDER BY ot.tag_value)
        FROM offering_tags ot
        WHERE ot.offering_id = o.id
    ), ARRAY[]::TEXT[]) AS tags,
    o.created_at, o.updated_at
FROM offerings o
INNER JOIN vouchers v ON v.id = o.voucher_id
ORDER BY o.id DESC
LIMIT $1;

--name: list-offerings-next
SELECT o.id, o.source_id, o.voucher_id, v.address AS voucher_address,
    v.name AS voucher_name, v.symbol AS voucher_symbol, v.decimals AS voucher_decimals,
    o.seller_address, o.commodity_name, o.commodity_type, o.description,
    o.quantity::TEXT AS quantity, o.unit, o.frequency, o.price::TEXT AS price,
    o.available, o.location_name, o.geo_lat, o.geo_lng, o.image_url,
    COALESCE((
        SELECT array_agg(ot.tag_value ORDER BY ot.tag_value)
        FROM offering_tags ot
        WHERE ot.offering_id = o.id
    ), ARRAY[]::TEXT[]) AS tags,
    o.created_at, o.updated_at
FROM offerings o
INNER JOIN vouchers v ON v.id = o.voucher_id
WHERE o.id < $1
ORDER BY o.id DESC
LIMIT $2;

--name: list-offerings-prev
SELECT * FROM (
    SELECT o.id, o.source_id, o.voucher_id, v.address AS voucher_address,
        v.name AS voucher_name, v.symbol AS voucher_symbol, v.decimals AS voucher_decimals,
        o.seller_address, o.commodity_name, o.commodity_type, o.description,
        o.quantity::TEXT AS quantity, o.unit, o.frequency, o.price::TEXT AS price,
        o.available, o.location_name, o.geo_lat, o.geo_lng, o.image_url,
        COALESCE((
            SELECT array_agg(ot.tag_value ORDER BY ot.tag_value)
            FROM offering_tags ot
            WHERE ot.offering_id = o.id
        ), ARRAY[]::TEXT[]) AS tags,
        o.created_at, o.updated_at
    FROM offerings o
    INNER JOIN vouchers v ON v.id = o.voucher_id
    WHERE o.id > $1
    ORDER BY o.id ASC
    LIMIT $2
) page
ORDER BY id DESC;

--name: list-voucher-offerings
SELECT o.id, o.source_id, o.voucher_id, v.address AS voucher_address,
    v.name AS voucher_name, v.symbol AS voucher_symbol, v.decimals AS voucher_decimals,
    o.seller_address, o.commodity_name, o.commodity_type, o.description,
    o.quantity::TEXT AS quantity, o.unit, o.frequency, o.price::TEXT AS price,
    o.available, o.location_name, o.geo_lat, o.geo_lng, o.image_url,
    COALESCE((
        SELECT array_agg(ot.tag_value ORDER BY ot.tag_value)
        FROM offering_tags ot
        WHERE ot.offering_id = o.id
    ), ARRAY[]::TEXT[]) AS tags,
    o.created_at, o.updated_at
FROM offerings o
INNER JOIN vouchers v ON v.id = o.voucher_id
WHERE lower(v.address) = lower($1) AND o.available AND v.active
ORDER BY o.id DESC
LIMIT $2;

--name: list-voucher-offerings-next
SELECT o.id, o.source_id, o.voucher_id, v.address AS voucher_address,
    v.name AS voucher_name, v.symbol AS voucher_symbol, v.decimals AS voucher_decimals,
    o.seller_address, o.commodity_name, o.commodity_type, o.description,
    o.quantity::TEXT AS quantity, o.unit, o.frequency, o.price::TEXT AS price,
    o.available, o.location_name, o.geo_lat, o.geo_lng, o.image_url,
    COALESCE((
        SELECT array_agg(ot.tag_value ORDER BY ot.tag_value)
        FROM offering_tags ot
        WHERE ot.offering_id = o.id
    ), ARRAY[]::TEXT[]) AS tags,
    o.created_at, o.updated_at
FROM offerings o
INNER JOIN vouchers v ON v.id = o.voucher_id
WHERE lower(v.address) = lower($1) AND o.available AND v.active AND o.id < $2
ORDER BY o.id DESC
LIMIT $3;

--name: list-voucher-offerings-prev
SELECT * FROM (
    SELECT o.id, o.source_id, o.voucher_id, v.address AS voucher_address,
        v.name AS voucher_name, v.symbol AS voucher_symbol, v.decimals AS voucher_decimals,
        o.seller_address, o.commodity_name, o.commodity_type, o.description,
        o.quantity::TEXT AS quantity, o.unit, o.frequency, o.price::TEXT AS price,
        o.available, o.location_name, o.geo_lat, o.geo_lng, o.image_url,
        COALESCE((
            SELECT array_agg(ot.tag_value ORDER BY ot.tag_value)
            FROM offering_tags ot
            WHERE ot.offering_id = o.id
        ), ARRAY[]::TEXT[]) AS tags,
        o.created_at, o.updated_at
    FROM offerings o
    INNER JOIN vouchers v ON v.id = o.voucher_id
    WHERE lower(v.address) = lower($1) AND o.available AND v.active AND o.id > $2
    ORDER BY o.id ASC
    LIMIT $3
) page
ORDER BY id DESC;

-- Every available offering backed by a voucher this pool allows. The pool page
-- lists these instead of walking its vouchers one request at a time.
-- `pt.is_allowed` matches list-pool-vouchers; `v.active` and `o.available`
-- match list-voucher-offerings, so a voucher's offerings read the same whether
-- they are reached through the pool or through the voucher.
--name: list-pool-offerings
SELECT o.id, o.source_id, o.voucher_id, v.address AS voucher_address,
    v.name AS voucher_name, v.symbol AS voucher_symbol, v.decimals AS voucher_decimals,
    o.seller_address, o.commodity_name, o.commodity_type, o.description,
    o.quantity::TEXT AS quantity, o.unit, o.frequency, o.price::TEXT AS price,
    o.available, o.location_name, o.geo_lat, o.geo_lng, o.image_url,
    COALESCE((
        SELECT array_agg(ot.tag_value ORDER BY ot.tag_value)
        FROM offering_tags ot
        WHERE ot.offering_id = o.id
    ), ARRAY[]::TEXT[]) AS tags,
    o.created_at, o.updated_at
FROM offerings o
INNER JOIN vouchers v ON v.id = o.voucher_id
INNER JOIN pool_tokens pt ON pt.voucher_id = v.id
INNER JOIN pools p ON p.id = pt.pool_id
WHERE lower(p.address) = lower($1) AND pt.is_allowed AND o.available AND v.active
ORDER BY o.id DESC
LIMIT $2;

--name: list-pool-offerings-next
SELECT o.id, o.source_id, o.voucher_id, v.address AS voucher_address,
    v.name AS voucher_name, v.symbol AS voucher_symbol, v.decimals AS voucher_decimals,
    o.seller_address, o.commodity_name, o.commodity_type, o.description,
    o.quantity::TEXT AS quantity, o.unit, o.frequency, o.price::TEXT AS price,
    o.available, o.location_name, o.geo_lat, o.geo_lng, o.image_url,
    COALESCE((
        SELECT array_agg(ot.tag_value ORDER BY ot.tag_value)
        FROM offering_tags ot
        WHERE ot.offering_id = o.id
    ), ARRAY[]::TEXT[]) AS tags,
    o.created_at, o.updated_at
FROM offerings o
INNER JOIN vouchers v ON v.id = o.voucher_id
INNER JOIN pool_tokens pt ON pt.voucher_id = v.id
INNER JOIN pools p ON p.id = pt.pool_id
WHERE lower(p.address) = lower($1) AND pt.is_allowed AND o.available AND v.active
    AND o.id < $2
ORDER BY o.id DESC
LIMIT $3;

--name: list-pool-offerings-prev
SELECT * FROM (
    SELECT o.id, o.source_id, o.voucher_id, v.address AS voucher_address,
        v.name AS voucher_name, v.symbol AS voucher_symbol, v.decimals AS voucher_decimals,
        o.seller_address, o.commodity_name, o.commodity_type, o.description,
        o.quantity::TEXT AS quantity, o.unit, o.frequency, o.price::TEXT AS price,
        o.available, o.location_name, o.geo_lat, o.geo_lng, o.image_url,
        COALESCE((
            SELECT array_agg(ot.tag_value ORDER BY ot.tag_value)
            FROM offering_tags ot
            WHERE ot.offering_id = o.id
        ), ARRAY[]::TEXT[]) AS tags,
        o.created_at, o.updated_at
    FROM offerings o
    INNER JOIN vouchers v ON v.id = o.voucher_id
    INNER JOIN pool_tokens pt ON pt.voucher_id = v.id
    INNER JOIN pools p ON p.id = pt.pool_id
    WHERE lower(p.address) = lower($1) AND pt.is_allowed AND o.available AND v.active
        AND o.id > $2
    ORDER BY o.id ASC
    LIMIT $3
) page
ORDER BY id DESC;

--name: list-market-vouchers
WITH page AS MATERIALIZED (
    SELECT v.id, v.chain_id, v.address, v.name, v.symbol, v.icon_url,
        v.location_name, v.geo_lat, v.geo_lng
    FROM vouchers v
    WHERE v.active AND v.geo_lat IS NOT NULL
    ORDER BY v.id DESC
    LIMIT $1
)
SELECT page.*,
    CASE dominant.commodity_type
        WHEN 'GOOD' THEN 'goods'
        WHEN 'SERVICE' THEN 'services'
        -- commodity_type is FK-constrained to GOOD/SERVICE, so ELSE represents
        -- the LEFT JOIN miss (no available offerings), never an unknown type.
        -- A future third commodity type must extend this CASE, not fall here.
        ELSE 'voucher'
    END AS kind
FROM page
LEFT JOIN LATERAL (
    SELECT o.commodity_type
    FROM offerings o
    WHERE o.voucher_id = page.id AND o.available
    GROUP BY o.commodity_type
    ORDER BY COUNT(*) DESC, o.commodity_type ASC
    LIMIT 1
) dominant ON TRUE
ORDER BY page.id DESC;

--name: list-market-vouchers-next
WITH page AS MATERIALIZED (
    SELECT v.id, v.chain_id, v.address, v.name, v.symbol, v.icon_url,
        v.location_name, v.geo_lat, v.geo_lng
    FROM vouchers v
    WHERE v.active AND v.geo_lat IS NOT NULL AND v.id < $1
    ORDER BY v.id DESC
    LIMIT $2
)
SELECT page.*,
    CASE dominant.commodity_type
        WHEN 'GOOD' THEN 'goods'
        WHEN 'SERVICE' THEN 'services'
        -- commodity_type is FK-constrained to GOOD/SERVICE, so ELSE represents
        -- the LEFT JOIN miss (no available offerings), never an unknown type.
        -- A future third commodity type must extend this CASE, not fall here.
        ELSE 'voucher'
    END AS kind
FROM page
LEFT JOIN LATERAL (
    SELECT o.commodity_type
    FROM offerings o
    WHERE o.voucher_id = page.id AND o.available
    GROUP BY o.commodity_type
    ORDER BY COUNT(*) DESC, o.commodity_type ASC
    LIMIT 1
) dominant ON TRUE
ORDER BY page.id DESC;

--name: list-market-vouchers-prev
WITH page AS MATERIALIZED (
    SELECT * FROM (
        SELECT v.id, v.chain_id, v.address, v.name, v.symbol, v.icon_url,
            v.location_name, v.geo_lat, v.geo_lng
        FROM vouchers v
        WHERE v.active AND v.geo_lat IS NOT NULL AND v.id > $1
        ORDER BY v.id ASC
        LIMIT $2
    ) window_page
)
SELECT page.*,
    CASE dominant.commodity_type
        WHEN 'GOOD' THEN 'goods'
        WHEN 'SERVICE' THEN 'services'
        -- commodity_type is FK-constrained to GOOD/SERVICE, so ELSE represents
        -- the LEFT JOIN miss (no available offerings), never an unknown type.
        -- A future third commodity type must extend this CASE, not fall here.
        ELSE 'voucher'
    END AS kind
FROM page
LEFT JOIN LATERAL (
    SELECT o.commodity_type
    FROM offerings o
    WHERE o.voucher_id = page.id AND o.available
    GROUP BY o.commodity_type
    ORDER BY COUNT(*) DESC, o.commodity_type ASC
    LIMIT 1
) dominant ON TRUE
ORDER BY page.id DESC;

--name: list-market-vouchers-bounded
WITH page AS MATERIALIZED (
    SELECT v.id, v.chain_id, v.address, v.name, v.symbol, v.icon_url,
        v.location_name, v.geo_lat, v.geo_lng
    FROM vouchers v
    WHERE v.active AND v.geo_lat IS NOT NULL
        AND point(v.geo_lng, v.geo_lat) <@ box(point($1, $2), point($3, $4))
    ORDER BY v.id DESC
    LIMIT $5
)
SELECT page.*,
    CASE dominant.commodity_type
        WHEN 'GOOD' THEN 'goods'
        WHEN 'SERVICE' THEN 'services'
        -- commodity_type is FK-constrained to GOOD/SERVICE, so ELSE represents
        -- the LEFT JOIN miss (no available offerings), never an unknown type.
        -- A future third commodity type must extend this CASE, not fall here.
        ELSE 'voucher'
    END AS kind
FROM page
LEFT JOIN LATERAL (
    SELECT o.commodity_type
    FROM offerings o
    WHERE o.voucher_id = page.id AND o.available
    GROUP BY o.commodity_type
    ORDER BY COUNT(*) DESC, o.commodity_type ASC
    LIMIT 1
) dominant ON TRUE
ORDER BY page.id DESC;

--name: list-market-vouchers-next-bounded
WITH page AS MATERIALIZED (
    SELECT v.id, v.chain_id, v.address, v.name, v.symbol, v.icon_url,
        v.location_name, v.geo_lat, v.geo_lng
    FROM vouchers v
    WHERE v.active AND v.geo_lat IS NOT NULL
        AND point(v.geo_lng, v.geo_lat) <@ box(point($1, $2), point($3, $4))
        AND v.id < $5
    ORDER BY v.id DESC
    LIMIT $6
)
SELECT page.*,
    CASE dominant.commodity_type
        WHEN 'GOOD' THEN 'goods'
        WHEN 'SERVICE' THEN 'services'
        -- commodity_type is FK-constrained to GOOD/SERVICE, so ELSE represents
        -- the LEFT JOIN miss (no available offerings), never an unknown type.
        -- A future third commodity type must extend this CASE, not fall here.
        ELSE 'voucher'
    END AS kind
FROM page
LEFT JOIN LATERAL (
    SELECT o.commodity_type
    FROM offerings o
    WHERE o.voucher_id = page.id AND o.available
    GROUP BY o.commodity_type
    ORDER BY COUNT(*) DESC, o.commodity_type ASC
    LIMIT 1
) dominant ON TRUE
ORDER BY page.id DESC;

--name: list-market-vouchers-prev-bounded
WITH page AS MATERIALIZED (
    SELECT * FROM (
        SELECT v.id, v.chain_id, v.address, v.name, v.symbol, v.icon_url,
            v.location_name, v.geo_lat, v.geo_lng
        FROM vouchers v
        WHERE v.active AND v.geo_lat IS NOT NULL
            AND point(v.geo_lng, v.geo_lat) <@ box(point($1, $2), point($3, $4))
            AND v.id > $5
        ORDER BY v.id ASC
        LIMIT $6
    ) window_page
)
SELECT page.*,
    CASE dominant.commodity_type
        WHEN 'GOOD' THEN 'goods'
        WHEN 'SERVICE' THEN 'services'
        -- commodity_type is FK-constrained to GOOD/SERVICE, so ELSE represents
        -- the LEFT JOIN miss (no available offerings), never an unknown type.
        -- A future third commodity type must extend this CASE, not fall here.
        ELSE 'voucher'
    END AS kind
FROM page
LEFT JOIN LATERAL (
    SELECT o.commodity_type
    FROM offerings o
    WHERE o.voucher_id = page.id AND o.available
    GROUP BY o.commodity_type
    ORDER BY COUNT(*) DESC, o.commodity_type ASC
    LIMIT 1
) dominant ON TRUE
ORDER BY page.id DESC;

-- Public voucher detail for one contract address. Backs the map card and the
-- voucher page, so it carries the contact and image fields the list omits.
-- Visibility matches the list (`active`), but geolocation is not required: a
-- voucher without coordinates still has a readable page, just no map.
--name: get-market-voucher-by-address
SELECT v.id, v.chain_id, v.address, v.name, v.symbol, v.decimals, v.owner_address,
    v.description, v.voucher_uoa, v.voucher_value::TEXT AS voucher_value,
    v.location_name, v.geo_lat, v.geo_lng,
    v.email, v.website, v.phone, v.icon_url, v.banner_url,
    CASE dominant.commodity_type
        WHEN 'GOOD' THEN 'goods'
        WHEN 'SERVICE' THEN 'services'
        -- Same derivation as the list statements: ELSE is the LEFT JOIN miss
        -- (no available offerings), never an unknown commodity type.
        ELSE 'voucher'
    END AS kind
FROM vouchers v
LEFT JOIN LATERAL (
    SELECT o.commodity_type
    FROM offerings o
    WHERE o.voucher_id = v.id AND o.available
    GROUP BY o.commodity_type
    ORDER BY COUNT(*) DESC, o.commodity_type ASC
    LIMIT 1
) dominant ON TRUE
WHERE lower(v.address) = lower($1) AND v.active;
