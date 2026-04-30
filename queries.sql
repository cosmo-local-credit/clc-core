--name: insert-keypair
INSERT INTO keystore(public_key, private_key) VALUES($1, $2) RETURNING id;

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
INSERT INTO deployed_tokens (otx_id, platform_contract_id, address, symbol, name, admin)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id;

--name: get-deployed-token-by-address
SELECT id, otx_id, platform_contract_id, address, symbol, name, admin, created_at, updated_at
FROM deployed_tokens
WHERE address = $1;

--name: insert-token-deploy-meta
INSERT INTO token_deploy_meta (otx_id, platform_contract_id, token_name, token_symbol, admin)
VALUES ($1, $2, $3, $4, $5)
RETURNING id;

--name: get-token-deploy-meta-by-otx-id
SELECT id, otx_id, platform_contract_id, token_name, token_symbol, admin, created_at, updated_at
FROM token_deploy_meta
WHERE otx_id = $1;

-- Pool addresses (shared singleton addresses)
--name: get-pool-address
SELECT address FROM pool_addresses WHERE key = $1;

--name: set-pool-address
INSERT INTO pool_addresses (key, address) VALUES ($1, $2)
ON CONFLICT (key) DO UPDATE SET address = EXCLUDED.address, updated_at = CURRENT_TIMESTAMP;

-- Pool deploy meta
--name: insert-pool-deploy-meta
INSERT INTO pool_deploy_meta (
    otx_id, platform_contract_id, pool_name, pool_symbol, pool_decimals,
    token_registry_address, limiter_address, fee_policy_address, quoter_address,
    protocol_fee_controller_address, fees_decoupled, admin
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING id;

--name: get-pool-deploy-meta-by-otx-id
SELECT id, otx_id, platform_contract_id, pool_name, pool_symbol, pool_decimals,
    token_registry_address, limiter_address, fee_policy_address, quoter_address,
    protocol_fee_controller_address, fees_decoupled, admin, created_at, updated_at
FROM pool_deploy_meta
WHERE otx_id = $1;

-- Oracle feeds
--name: get-oracle-feed-by-name
SELECT id, name, address, created_at, updated_at
FROM oracle_feeds
WHERE name = $1;

--name: list-oracle-feeds
SELECT id, name, address, created_at, updated_at
FROM oracle_feeds
ORDER BY name;

-- Deployed pools
--name: insert-deployed-pool
INSERT INTO deployed_pools (
    otx_id, platform_contract_id, address, name, symbol,
    token_registry_address, limiter_address, fee_policy_address, quoter_address,
    protocol_fee_controller_address, fees_decoupled, admin
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING id;

--name: get-deployed-pool-by-address
SELECT id, otx_id, platform_contract_id, address, name, symbol,
    token_registry_address, limiter_address, fee_policy_address, quoter_address,
    protocol_fee_controller_address, fees_decoupled, admin, created_at, updated_at
FROM deployed_pools
WHERE address = $1;

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
SELECT otx.id, otx.tracking_id, otx.otx_type, k.public_key, otx.raw_tx, otx.tx_hash,
    otx.nonce, otx.replaced, otx.created_at, otx.updated_at, d.status
FROM otx
INNER JOIN dispatch d ON otx.id = d.otx_id
INNER JOIN keystore k ON otx.signer_account = k.id
WHERE k.public_key = $1 AND otx.id > $2
ORDER BY otx.id ASC
LIMIT $3;

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
INSERT INTO users (ethereum_address, name, email, role, status, max_pools, max_tokens)
VALUES ($1, $2, $3, $4::user_role, $5::user_status, $6, $7)
RETURNING id, ethereum_address, name, email, role, status, max_pools, max_tokens, created_at, updated_at;

--name: get-user-by-address
SELECT id, ethereum_address, name, email, role, status, max_pools, max_tokens, created_at, updated_at
FROM users
WHERE ethereum_address = $1;

--name: get-user-by-id
SELECT id, ethereum_address, name, email, role, status, max_pools, max_tokens, created_at, updated_at
FROM users
WHERE id = $1::uuid;

--name: update-user-status
UPDATE users
SET status = $2::user_status
WHERE ethereum_address = $1
RETURNING id, ethereum_address, name, email, role, status, max_pools, max_tokens, created_at, updated_at;

--name: update-user-limits
UPDATE users
SET max_pools = $2, max_tokens = $3
WHERE ethereum_address = $1
RETURNING id, ethereum_address, name, email, role, status, max_pools, max_tokens, created_at, updated_at;

--name: list-users
SELECT id, ethereum_address, name, email, role, status, max_pools, max_tokens, created_at, updated_at
FROM users
ORDER BY created_at DESC;

--name: list-users-by-status
SELECT id, ethereum_address, name, email, role, status, max_pools, max_tokens, created_at, updated_at
FROM users
WHERE status = $1::user_status
ORDER BY created_at DESC;

-- User deployments
--name: count-user-tokens
SELECT COUNT(*) FROM user_deployments
WHERE user_id = $1::uuid AND resource_type = 'TOKEN';

--name: count-user-pools
SELECT COUNT(*) FROM user_deployments
WHERE user_id = $1::uuid AND resource_type = 'POOL';

--name: insert-user-deployment
INSERT INTO user_deployments (user_id, tracking_id, resource_type)
VALUES ($1::uuid, $2::uuid, $3)
RETURNING id;

--name: list-user-deployments
SELECT id, user_id, tracking_id, resource_type, created_at
FROM user_deployments
WHERE user_id = $1::uuid
ORDER BY created_at DESC;

--name: get-user-deployment-by-tracking-id
SELECT id, user_id, tracking_id, resource_type, created_at
FROM user_deployments
WHERE tracking_id = $1::uuid AND user_id = $2::uuid;

--name: list-all-deployments
SELECT ud.id, ud.user_id, ud.tracking_id, ud.resource_type, ud.created_at,
    u.ethereum_address, u.name AS user_name
FROM user_deployments ud
JOIN users u ON u.id = ud.user_id
ORDER BY ud.created_at DESC;

--name: list-tokens-by-user
SELECT dt.*
FROM deployed_tokens dt
INNER JOIN otx o ON dt.otx_id = o.id
INNER JOIN user_deployments ud ON ud.tracking_id = o.tracking_id
WHERE ud.user_id = $1::uuid AND ud.resource_type = 'TOKEN';

--name: list-pools-by-user
SELECT dp.*
FROM deployed_pools dp
INNER JOIN otx o ON dp.otx_id = o.id
INNER JOIN user_deployments ud ON ud.tracking_id = o.tracking_id
WHERE ud.user_id = $1::uuid AND ud.resource_type = 'POOL';

--name: list-all-tokens
SELECT id, otx_id, platform_contract_id, address, symbol, name, admin, created_at, updated_at
FROM deployed_tokens
ORDER BY created_at DESC;

--name: list-all-pools
SELECT id, otx_id, platform_contract_id, address, name, symbol,
    token_registry_address, limiter_address, fee_policy_address, quoter_address,
    protocol_fee_controller_address, fees_decoupled, admin, created_at, updated_at
FROM deployed_pools
ORDER BY created_at DESC;
