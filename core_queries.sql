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
    description, website, email, phone, terms, voucher_uoa, voucher_value,
    location_name, geo_lat, geo_lng, icon_url, banner_url, marker_icon, marker_color
) VALUES ($1, $2, 'PENDING', $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)
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

-- Confirm-path promotion (MIGRATION.md §2.4): a confirmed deploy becomes a row
-- in the network voucher catalog, and the pipeline row links to it. Runs in the
-- confirming transaction so a deploy is never CONFIRMED without its catalog row.
--
-- The projection lives here rather than in Go so the deploy row stays the single
-- source for the copy and chain_id is derived from the real factory that was
-- used, not from app config.
--
-- Conflict target is the lower(address) index, not UNIQUE (chain_id, address):
-- an imported catalog row may carry different address casing, which would slip
-- past an exact-match inference and then trip the lower() index on insert.
-- Matching on lower() covers both, since an address that differs in case still
-- collides and an address that differs in bytes cannot collide on either.
--
-- Merge policy: the deploy is the fresh, authoritative request, so its values
-- win where present; a pre-existing catalog row keeps whatever the deploy has
-- no opinion about (sink, and active/internal state). The icon and banner are
-- part of the request now, so they merge on the same COALESCE terms: a deploy
-- that carried an image sets it, one that did not leaves an imported image alone.
-- The map marker merges the same way.
--name: upsert-voucher-from-deploy
WITH deploy AS (
    SELECT
        pc.chain_id,
        pc.version AS contract_version,
        dt.id      AS deployed_token_id,
        dt.address,
        dt.name,
        dt.symbol,
        dt.decimals,
        dt.admin,
        dt.description,
        dt.website,
        dt.email,
        dt.phone,
        dt.terms,
        dt.voucher_uoa,
        dt.voucher_value,
        dt.location_name,
        dt.geo_lat,
        dt.geo_lng,
        dt.icon_url,
        dt.banner_url,
        dt.marker_icon,
        dt.marker_color,
        dt.platform_contract_id
    FROM deployed_tokens dt
    INNER JOIN platform_contracts pc ON pc.id = dt.platform_contract_id
    WHERE dt.id = $1 AND dt.address IS NOT NULL
),
upserted AS (
    INSERT INTO vouchers (
        chain_id, address, name, symbol, decimals, owner_address, voucher_type,
        description, voucher_uoa, voucher_value, location_name, geo_lat, geo_lng,
        email, website, phone, terms, icon_url, banner_url, marker_icon,
        marker_color, contract_version, platform_contract_id
    )
    SELECT
        d.chain_id, d.address, d.name, d.symbol, d.decimals, d.admin,
        -- Every clc-core token deploy goes through the giftable token
        -- implementation. The expiring variant is indistinguishable here
        -- because the deploy row does not retain the expiry timestamp.
        'GIFTABLE',
        d.description, d.voucher_uoa, d.voucher_value, d.location_name,
        d.geo_lat, d.geo_lng, d.email, d.website, d.phone, d.terms,
        d.icon_url, d.banner_url, d.marker_icon, d.marker_color,
        NULLIF(d.contract_version, ''), d.platform_contract_id
    FROM deploy d
    ON CONFLICT (chain_id, lower(address)) DO UPDATE SET
        name                 = COALESCE(EXCLUDED.name,          vouchers.name),
        symbol               = COALESCE(EXCLUDED.symbol,        vouchers.symbol),
        decimals             = COALESCE(EXCLUDED.decimals,      vouchers.decimals),
        owner_address        = COALESCE(EXCLUDED.owner_address,  vouchers.owner_address),
        voucher_type         = COALESCE(EXCLUDED.voucher_type,   vouchers.voucher_type),
        description          = COALESCE(EXCLUDED.description,    vouchers.description),
        voucher_uoa          = COALESCE(EXCLUDED.voucher_uoa,    vouchers.voucher_uoa),
        voucher_value        = COALESCE(EXCLUDED.voucher_value,  vouchers.voucher_value),
        location_name        = COALESCE(EXCLUDED.location_name,  vouchers.location_name),
        geo_lat              = COALESCE(EXCLUDED.geo_lat,        vouchers.geo_lat),
        geo_lng              = COALESCE(EXCLUDED.geo_lng,        vouchers.geo_lng),
        email                = COALESCE(EXCLUDED.email,          vouchers.email),
        website              = COALESCE(EXCLUDED.website,        vouchers.website),
        phone                = COALESCE(EXCLUDED.phone,          vouchers.phone),
        terms                = COALESCE(EXCLUDED.terms,          vouchers.terms),
        icon_url             = COALESCE(EXCLUDED.icon_url,       vouchers.icon_url),
        banner_url           = COALESCE(EXCLUDED.banner_url,     vouchers.banner_url),
        marker_icon          = COALESCE(EXCLUDED.marker_icon,    vouchers.marker_icon),
        marker_color         = COALESCE(EXCLUDED.marker_color,   vouchers.marker_color),
        contract_version     = COALESCE(EXCLUDED.contract_version, vouchers.contract_version),
        platform_contract_id = EXCLUDED.platform_contract_id
    RETURNING id
),
linked AS (
    UPDATE deployed_tokens
    SET voucher_id = (SELECT id FROM upserted)
    WHERE id = $1
    RETURNING voucher_id
),
copied_tags AS (
    INSERT INTO voucher_tags (voucher_id, tag_value)
    SELECT (SELECT id FROM upserted), dtt.tag_value
    FROM deployed_token_tags dtt
    WHERE dtt.deployed_token_id = $1
      AND EXISTS (SELECT 1 FROM upserted)
    ON CONFLICT (voucher_id, tag_value) DO NOTHING
    RETURNING voucher_id
)
SELECT id FROM upserted;

--name: insert-deployed-token-tag
INSERT INTO deployed_token_tags (deployed_token_id, tag_value) VALUES ($1, $2);

--name: get-deployed-token-by-address
SELECT dt.id, dt.otx_id, dt.platform_contract_id, dt.status, dt.address, dt.symbol,
       COALESCE(v.display_name, dt.name) AS name, dt.decimals, dt.admin,
       dt.description, dt.website, dt.email, dt.phone, dt.terms, dt.voucher_uoa, dt.voucher_value,
       dt.location_name, dt.geo_lat, dt.geo_lng, dt.icon_url, dt.banner_url,
       dt.marker_icon, dt.marker_color, dt.created_at, dt.updated_at, o.tracking_id,
       COALESCE(array_agg(dtt.tag_value ORDER BY dtt.tag_value) FILTER (WHERE dtt.tag_value IS NOT NULL), ARRAY[]::TEXT[]) AS tags
FROM deployed_tokens dt
INNER JOIN otx o ON dt.otx_id = o.id
LEFT JOIN vouchers v ON v.id = dt.voucher_id
LEFT JOIN deployed_token_tags dtt ON dtt.deployed_token_id = dt.id
WHERE dt.address = $1 AND dt.status = 'CONFIRMED'
GROUP BY dt.id, v.id, o.tracking_id;

--name: get-deployed-token-by-tracking-id
SELECT dt.id, dt.otx_id, dt.platform_contract_id, dt.status, dt.address, dt.symbol,
       COALESCE(v.display_name, dt.name) AS name, dt.decimals, dt.admin,
       dt.description, dt.website, dt.email, dt.phone, dt.terms, dt.voucher_uoa, dt.voucher_value,
       dt.location_name, dt.geo_lat, dt.geo_lng, dt.icon_url, dt.banner_url,
       dt.marker_icon, dt.marker_color, dt.created_at, dt.updated_at, o.tracking_id,
       COALESCE(array_agg(dtt.tag_value ORDER BY dtt.tag_value) FILTER (WHERE dtt.tag_value IS NOT NULL), ARRAY[]::TEXT[]) AS tags
FROM deployed_tokens dt
INNER JOIN otx o ON dt.otx_id = o.id
LEFT JOIN vouchers v ON v.id = dt.voucher_id
LEFT JOIN deployed_token_tags dtt ON dtt.deployed_token_id = dt.id
WHERE o.tracking_id = $1::uuid AND dt.status = 'CONFIRMED'
GROUP BY dt.id, v.id, o.tracking_id
LIMIT 1;

--name: list-token-tags
SELECT value FROM token_tag ORDER BY value;

--name: find-token-tag
SELECT value FROM token_tag WHERE lower(value) = lower($1) FOR KEY SHARE;

--name: insert-custom-token-tag
INSERT INTO token_tag (value, is_custom) VALUES ($1, true)
ON CONFLICT DO NOTHING;

--name: list-offering-units
SELECT value FROM offering_unit ORDER BY value;

--name: find-offering-unit
SELECT value FROM offering_unit WHERE lower(value) = lower($1) FOR KEY SHARE;

--name: insert-custom-offering-unit
INSERT INTO offering_unit (value, is_custom) VALUES ($1, true)
ON CONFLICT DO NOTHING;

--name: list-token-tag-vocabulary
SELECT
    tt.value,
    tt.is_custom,
    (SELECT count(*) FROM deployed_token_tags dtt WHERE dtt.tag_value = tt.value) AS deployed_tokens,
    (SELECT count(*) FROM deployed_pool_tags dpt WHERE dpt.tag_value = tt.value) AS deployed_pools,
    (SELECT count(*) FROM voucher_tags vt WHERE vt.tag_value = tt.value) AS vouchers,
    (SELECT count(*) FROM pool_tags pt WHERE pt.tag_value = tt.value) AS pools,
    (SELECT count(*) FROM report_tags rt WHERE rt.tag_value = tt.value) AS posts,
    (SELECT count(*) FROM offering_tags ot WHERE ot.tag_value = tt.value) AS offerings,
    (SELECT count(*) FROM catalog_tags ct WHERE ct.tag_value = tt.value) AS catalogs
FROM token_tag tt
ORDER BY tt.value;

--name: list-offering-unit-vocabulary
SELECT
    ou.value,
    ou.is_custom,
    (SELECT count(*) FROM offerings o WHERE o.unit = ou.value) AS offerings
FROM offering_unit ou
ORDER BY ou.value;

--name: lock-token-tag
SELECT value, is_custom FROM token_tag
WHERE lower(value) = lower($1)
FOR UPDATE;

--name: lock-offering-unit
SELECT value, is_custom FROM offering_unit
WHERE lower(value) = lower($1)
FOR UPDATE;

--name: rename-token-tag
UPDATE token_tag SET value = $2 WHERE value = $1 RETURNING value;

--name: rename-custom-offering-unit
UPDATE offering_unit SET value = $2 WHERE value = $1 AND is_custom RETURNING value;

--name: has-unfinished-tag-deployment
SELECT EXISTS (
    SELECT 1 FROM river_job
    WHERE kind IN ('TOKEN_DEPLOY', 'POOL_DEPLOY')
      AND state NOT IN ('completed', 'cancelled', 'discarded')
      AND args->'tags' ? $1
);

--name: delete-custom-token-tag
WITH
deleted_deployed_tokens AS (
    DELETE FROM deployed_token_tags WHERE tag_value = $1 RETURNING 1
),
deleted_deployed_pools AS (
    DELETE FROM deployed_pool_tags WHERE tag_value = $1 RETURNING 1
),
deleted_vouchers AS (
    DELETE FROM voucher_tags WHERE tag_value = $1 RETURNING 1
),
deleted_pools AS (
    DELETE FROM pool_tags WHERE tag_value = $1 RETURNING 1
),
deleted_posts AS (
    DELETE FROM report_tags WHERE tag_value = $1 RETURNING 1
),
deleted_offerings AS (
    DELETE FROM offering_tags WHERE tag_value = $1 RETURNING 1
),
deleted_catalogs AS (
    DELETE FROM catalog_tags WHERE tag_value = $1 RETURNING 1
),
deleted_value AS (
    DELETE FROM token_tag
    WHERE value = $1
      AND is_custom
      AND (
        (SELECT count(*) FROM deleted_deployed_tokens)
        + (SELECT count(*) FROM deleted_deployed_pools)
        + (SELECT count(*) FROM deleted_vouchers)
        + (SELECT count(*) FROM deleted_pools)
        + (SELECT count(*) FROM deleted_posts)
        + (SELECT count(*) FROM deleted_offerings)
        + (SELECT count(*) FROM deleted_catalogs)
      ) >= 0
    RETURNING value
)
SELECT
    (SELECT count(*) FROM deleted_deployed_tokens) AS deployed_tokens,
    (SELECT count(*) FROM deleted_deployed_pools) AS deployed_pools,
    (SELECT count(*) FROM deleted_vouchers) AS vouchers,
    (SELECT count(*) FROM deleted_pools) AS pools,
    (SELECT count(*) FROM deleted_posts) AS posts,
    (SELECT count(*) FROM deleted_offerings) AS offerings,
    (SELECT count(*) FROM deleted_catalogs) AS catalogs,
    EXISTS (SELECT 1 FROM deleted_value) AS deleted;

--name: delete-custom-offering-unit
WITH
cleared_offerings AS (
    UPDATE offerings SET unit = NULL WHERE unit = $1 RETURNING 1
),
deleted_value AS (
    DELETE FROM offering_unit
    WHERE value = $1
      AND is_custom
      AND (SELECT count(*) FROM cleared_offerings) >= 0
    RETURNING value
)
SELECT
    (SELECT count(*) FROM cleared_offerings) AS offerings,
    EXISTS (SELECT 1 FROM deleted_value) AS deleted;

-- A symbol is occupied by a catalog row or by a deployment which may still
-- land on-chain. FAILED deployments are deliberately reusable. The suffix
-- predicate mirrors the client's bare-stem plus numeric-suffix candidates;
-- unrelated symbols which merely share the prefix do not occupy it.
--name: list-taken-voucher-symbols
SELECT upper(name) AS symbol FROM identity_claims
WHERE namespace = 'symbol' AND starts_with(name, lower($1))
  AND (name = lower($1) OR substring(name FROM char_length($1) + 1) ~ '^[0-9]{1,2}$')
ORDER BY name LIMIT 128;

--name: list-taken-pool-symbols
SELECT upper(name) AS symbol FROM identity_claims
WHERE namespace = 'symbol' AND starts_with(name, lower($1))
  AND (name = lower($1) OR substring(name FROM char_length($1) + 1) ~ '^[0-9]{1,2}$')
ORDER BY name LIMIT 128;

-- A display name is occupied by a catalog row or by a deploy which may still
-- land on-chain. FAILED deploys are deliberately reusable, as with symbols, and
-- a retired voucher keeps its label: the address stays resolvable and reusing
-- the name would make old references ambiguous.
--
-- $1 is the caller-normalized name (trimmed, inner whitespace collapsed,
-- lowercased) -- normalizeDeployName in Go produces exactly what the indexed
-- expression here produces. $2 excludes the row being renamed so an owner can
-- re-save its own name; it is NULL on deploy.
--name: voucher-display-name-taken
SELECT EXISTS (
    SELECT 1
    FROM vouchers
    WHERE lower(regexp_replace(btrim(COALESCE(display_name, name)), '\s+', ' ', 'g')) = $1
      AND ($2::TEXT IS NULL OR lower(address) <> lower($2))
    UNION ALL
    SELECT 1
    FROM deployed_tokens
    WHERE status IN ('PENDING', 'CONFIRMED')
      AND lower(regexp_replace(btrim(name), '\s+', ' ', 'g')) = $1
      AND ($2::TEXT IS NULL OR address IS NULL OR lower(address) <> lower($2))
);

--name: pool-display-name-taken
SELECT EXISTS (SELECT 1 FROM identity_claims
WHERE namespace = 'recipient' AND name = $1
  AND ($2::TEXT IS NULL OR owner_kind <> 'pool' OR owner_key NOT LIKE '%:' || lower($2)));

--name: get-protocol-address
SELECT address FROM protocol_addresses WHERE key = $1;

--name: set-protocol-address
INSERT INTO protocol_addresses (key, address) VALUES ($1, $2)
ON CONFLICT (key) DO UPDATE SET address = EXCLUDED.address, updated_at = CURRENT_TIMESTAMP;

-- Deployed pools
--name: insert-deployed-pool
INSERT INTO deployed_pools (
    otx_id, platform_contract_id, status, name, symbol, decimals,
    token_registry_address, limiter_address, fee_policy_address, quoter_address, quoter_type,
    protocol_fee_controller_address, fees_decoupled, fee_ppm, fee_address, admin,
    description, unit_of_account, website, email, phone, terms, location_name, geo_lat, geo_lng, image_url, banner_url
) VALUES ($1, $2, 'PENDING', $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26)
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

-- Confirm-path promotion for pools (MIGRATION.md §3.5). Same contract as
-- upsert-voucher-from-deploy: derive chain_id from the real factory, match the
-- catalog on lower(address), let the fresh deploy win field by field, and link
-- the pipeline row — all inside the confirming transaction.
--
-- default_voucher_id has no counterpart on the deploy row and is left to the
-- importer and pool sync. fee_ppm and fee_address do: they are what the deploy
-- initialized FeePolicy and SwapPool with, and a later on-chain setDefaultFee or
-- setFeeAddress is the indexer's to project — COALESCE keeps a projected value
-- rather than resetting it to the deploy's.
--name: upsert-pool-from-deploy
WITH deploy AS (
    SELECT
        pc.chain_id,
        dp.id AS deployed_pool_id,
        dp.address,
        dp.name,
        dp.symbol,
        dp.decimals,
        dp.admin,
        dp.token_registry_address,
        dp.limiter_address,
        dp.quoter_address, dp.quoter_type,
        dp.fee_policy_address,
        dp.protocol_fee_controller_address,
        dp.fees_decoupled,
        dp.fee_ppm,
        dp.fee_address,
        dp.description,
        dp.unit_of_account,
        dp.website,
        dp.email,
        dp.phone,
        dp.terms,
        dp.location_name,
        dp.geo_lat,
        dp.geo_lng,
        dp.image_url,
        dp.banner_url,
        dp.platform_contract_id
    FROM deployed_pools dp
    INNER JOIN platform_contracts pc ON pc.id = dp.platform_contract_id
    WHERE dp.id = $1 AND dp.address IS NOT NULL
),
upserted AS (
    INSERT INTO pools (
        chain_id, address, name, symbol, decimals, owner_address,
        token_registry_address, limiter_address, quoter_address, quoter_type, fee_policy_address,
        protocol_fee_controller_address, fees_decoupled, fee_ppm, fee_address,
        description, unit_of_account,
        location_name, geo_lat, geo_lng, email, website, phone, terms,
        image_url, banner_url, platform_contract_id
    )
    SELECT
        d.chain_id, d.address, d.name, d.symbol, d.decimals, d.admin,
        d.token_registry_address, d.limiter_address, d.quoter_address, d.quoter_type,
        d.fee_policy_address, d.protocol_fee_controller_address, d.fees_decoupled,
        d.fee_ppm, d.fee_address,
        d.description, d.unit_of_account, d.location_name, d.geo_lat, d.geo_lng,
        d.email, d.website, d.phone, d.terms, d.image_url, d.banner_url,
        d.platform_contract_id
    FROM deploy d
    ON CONFLICT (chain_id, lower(address)) DO UPDATE SET
        name                            = COALESCE(EXCLUDED.name,     pools.name),
        symbol                          = COALESCE(EXCLUDED.symbol,   pools.symbol),
        decimals                        = COALESCE(EXCLUDED.decimals, pools.decimals),
        owner_address                   = COALESCE(EXCLUDED.owner_address, pools.owner_address),
        token_registry_address          = COALESCE(EXCLUDED.token_registry_address, pools.token_registry_address),
        limiter_address                 = COALESCE(EXCLUDED.limiter_address, pools.limiter_address),
        quoter_address                  = COALESCE(EXCLUDED.quoter_address, pools.quoter_address),
        quoter_type                     = COALESCE(EXCLUDED.quoter_type, pools.quoter_type),
        fee_policy_address              = COALESCE(EXCLUDED.fee_policy_address, pools.fee_policy_address),
        protocol_fee_controller_address = COALESCE(EXCLUDED.protocol_fee_controller_address, pools.protocol_fee_controller_address),
        fees_decoupled                  = COALESCE(EXCLUDED.fees_decoupled, pools.fees_decoupled),
        fee_ppm                         = COALESCE(pools.fee_ppm, EXCLUDED.fee_ppm),
        fee_address                     = COALESCE(pools.fee_address, EXCLUDED.fee_address),
        description                     = COALESCE(EXCLUDED.description, pools.description),
        unit_of_account                 = COALESCE(EXCLUDED.unit_of_account, pools.unit_of_account),
        location_name                   = COALESCE(EXCLUDED.location_name, pools.location_name),
        geo_lat                         = COALESCE(EXCLUDED.geo_lat, pools.geo_lat),
        geo_lng                         = COALESCE(EXCLUDED.geo_lng, pools.geo_lng),
        email                           = COALESCE(EXCLUDED.email,   pools.email),
        website                         = COALESCE(EXCLUDED.website, pools.website),
        phone                           = COALESCE(EXCLUDED.phone,   pools.phone),
        terms                           = COALESCE(EXCLUDED.terms,   pools.terms),
        image_url                       = COALESCE(EXCLUDED.image_url, pools.image_url),
        banner_url                      = COALESCE(EXCLUDED.banner_url, pools.banner_url),
        platform_contract_id            = EXCLUDED.platform_contract_id
    RETURNING id
),
linked AS (
    UPDATE deployed_pools
    SET pool_id = (SELECT id FROM upserted)
    WHERE id = $1
    RETURNING pool_id
),
copied_tags AS (
    INSERT INTO pool_tags (pool_id, tag_value)
    SELECT (SELECT id FROM upserted), dpt.tag_value
    FROM deployed_pool_tags dpt
    WHERE dpt.deployed_pool_id = $1
      AND EXISTS (SELECT 1 FROM upserted)
    ON CONFLICT (pool_id, tag_value) DO NOTHING
    RETURNING pool_id
)
SELECT id FROM upserted;

--name: insert-deployed-pool-tag
INSERT INTO deployed_pool_tags (deployed_pool_id, tag_value) VALUES ($1, $2);

--name: get-deployed-pool-by-address
SELECT dp.id, dp.otx_id, dp.platform_contract_id, dp.status, dp.address, dp.name, dp.symbol, dp.decimals,
    dp.token_registry_address, dp.limiter_address, dp.fee_policy_address, dp.quoter_address, dp.quoter_type,
    dp.protocol_fee_controller_address, dp.fees_decoupled, dp.fee_ppm, dp.fee_address, dp.admin,
    dp.description, dp.unit_of_account, dp.website, dp.email, dp.phone, dp.terms, dp.location_name, dp.geo_lat, dp.geo_lng,
    dp.image_url, dp.banner_url, dp.created_at, dp.updated_at, o.tracking_id,
    COALESCE(array_agg(dpt.tag_value ORDER BY dpt.tag_value) FILTER (WHERE dpt.tag_value IS NOT NULL), ARRAY[]::TEXT[]) AS tags
FROM deployed_pools dp
INNER JOIN otx o ON dp.otx_id = o.id
LEFT JOIN deployed_pool_tags dpt ON dpt.deployed_pool_id = dp.id
WHERE lower(dp.address) = lower($1) AND dp.status = 'CONFIRMED'
GROUP BY dp.id, o.tracking_id;

--name: get-deployed-pool-by-tracking-id
SELECT dp.id, dp.otx_id, dp.platform_contract_id, dp.status, dp.address, dp.name, dp.symbol, dp.decimals,
    dp.token_registry_address, dp.limiter_address, dp.fee_policy_address, dp.quoter_address, dp.quoter_type,
    dp.protocol_fee_controller_address, dp.fees_decoupled, dp.fee_ppm, dp.fee_address, dp.admin,
    dp.description, dp.unit_of_account, dp.website, dp.email, dp.phone, dp.terms, dp.location_name, dp.geo_lat, dp.geo_lng,
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
SELECT id, chain_id, name, address, created_at, updated_at
FROM oracle_feeds
WHERE chain_id = $1 AND name = $2;

--name: list-oracle-feeds
SELECT id, chain_id, name, address, created_at, updated_at
FROM oracle_feeds
WHERE chain_id = $1
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

-- The operator console's OTX page: every origin transaction the service signed,
-- newest first, narrowed by an optional type and dispatch status. Unlike
-- get-otx-by-account this is not scoped to one signer, and it omits raw_tx —
-- a page of 100 raw transactions is a payload nothing on that screen reads.
--
-- The two filters are written as `$n = '' OR col = $n` so one statement serves
-- the filtered and unfiltered reads. Both columns are indexed (migration 019);
-- the ordering is the primary key, so an unfiltered page is a backward index
-- scan.
--name: list-otx
SELECT otx.id, otx.tracking_id, otx.otx_type, k.public_key, otx.tx_hash,
    otx.nonce, otx.replaced, otx.created_at, otx.updated_at, d.status
FROM otx
INNER JOIN dispatch d ON otx.id = d.otx_id
INNER JOIN keystore k ON otx.signer_account = k.id
WHERE ($1 = '' OR otx.otx_type = $1) AND ($2 = '' OR d.status = $2)
ORDER BY otx.id DESC
LIMIT $3;

--name: list-otx-next
SELECT otx.id, otx.tracking_id, otx.otx_type, k.public_key, otx.tx_hash,
    otx.nonce, otx.replaced, otx.created_at, otx.updated_at, d.status
FROM otx
INNER JOIN dispatch d ON otx.id = d.otx_id
INNER JOIN keystore k ON otx.signer_account = k.id
WHERE ($1 = '' OR otx.otx_type = $1) AND ($2 = '' OR d.status = $2) AND otx.id < $3
ORDER BY otx.id DESC
LIMIT $4;

-- Keyset window fetched ascending (the rows immediately above the cursor) then
-- re-sorted descending in SQL, so the API never reorders rows in memory.
--name: list-otx-prev
SELECT * FROM (
    SELECT otx.id, otx.tracking_id, otx.otx_type, k.public_key, otx.tx_hash,
        otx.nonce, otx.replaced, otx.created_at, otx.updated_at, d.status
    FROM otx
    INNER JOIN dispatch d ON otx.id = d.otx_id
    INNER JOIN keystore k ON otx.signer_account = k.id
    WHERE ($1 = '' OR otx.otx_type = $1) AND ($2 = '' OR d.status = $2) AND otx.id > $3
    ORDER BY otx.id ASC
    LIMIT $4
) page
ORDER BY page.id DESC;

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
RETURNING id, blockchain_address AS ethereum_address, username, name, COALESCE(email, '') AS email, COALESCE(phone, '') AS phone, COALESCE(country, '') AS country, preferred_currency, bio, website, location_name, geo_lat, geo_lng, avatar_url, role, status, max_pools, max_tokens, created_at, updated_at;

--name: get-user-by-address
SELECT id, blockchain_address AS ethereum_address, username, name, COALESCE(email, '') AS email, COALESCE(phone, '') AS phone, COALESCE(country, '') AS country, preferred_currency, bio, website, location_name, geo_lat, geo_lng, avatar_url, role, status, max_pools, max_tokens, created_at, updated_at
FROM users
WHERE LOWER(blockchain_address) = LOWER($1)
LIMIT 1;

--name: get-user-by-id
SELECT id, blockchain_address AS ethereum_address, username, name, COALESCE(email, '') AS email, COALESCE(phone, '') AS phone, COALESCE(country, '') AS country, preferred_currency, bio, website, location_name, geo_lat, geo_lng, avatar_url, role, status, max_pools, max_tokens, created_at, updated_at
FROM users
WHERE id = $1;

--name: get-user-by-email
SELECT id, blockchain_address AS ethereum_address, username, name, COALESCE(email, '') AS email, COALESCE(phone, '') AS phone, COALESCE(country, '') AS country, preferred_currency, bio, website, location_name, geo_lat, geo_lng, avatar_url, role, status, max_pools, max_tokens, created_at, updated_at
FROM users
WHERE email = $1;

--name: update-user-status
UPDATE users
SET status = $2::user_status
WHERE blockchain_address = $1
RETURNING id, blockchain_address AS ethereum_address, username, name, COALESCE(email, '') AS email, COALESCE(phone, '') AS phone, COALESCE(country, '') AS country, preferred_currency, bio, website, location_name, geo_lat, geo_lng, avatar_url, role, status, max_pools, max_tokens, created_at, updated_at;

--name: approve-pending-user-by-address
UPDATE users
SET status = 'APPROVED'::user_status
WHERE blockchain_address = $1 AND status = 'PENDING'::user_status
RETURNING id, blockchain_address AS ethereum_address, username, name, COALESCE(email, '') AS email, COALESCE(phone, '') AS phone, COALESCE(country, '') AS country, preferred_currency, bio, website, location_name, geo_lat, geo_lng, avatar_url, role, status, max_pools, max_tokens, created_at, updated_at;

--name: update-user-limits
UPDATE users
SET max_pools = $2, max_tokens = $3
WHERE blockchain_address = $1
RETURNING id, blockchain_address AS ethereum_address, username, name, COALESCE(email, '') AS email, COALESCE(phone, '') AS phone, COALESCE(country, '') AS country, preferred_currency, bio, website, location_name, geo_lat, geo_lng, avatar_url, role, status, max_pools, max_tokens, created_at, updated_at;

--name: update-user-profile
-- Every onboarding preference preserves on omission so older clients cannot
-- erase a profile field they do not know about. An explicitly sent empty bio or
-- website clears it. Location and avatar retain their existing explicit-clear
-- controls because each is a multi-column/value pair respectively.
UPDATE users
SET name = $2,
    email = NULLIF($3, ''),
    phone = NULLIF($4, ''),
    country = $5,
    preferred_currency = COALESCE($6, preferred_currency),
    bio = CASE WHEN $7::TEXT IS NULL THEN bio ELSE NULLIF($7, '') END,
    website = CASE WHEN $8::TEXT IS NULL THEN website ELSE NULLIF($8, '') END,
    location_name = CASE WHEN $9 THEN NULL ELSE COALESCE($10, location_name) END,
    geo_lat = CASE WHEN $9 THEN NULL ELSE COALESCE($11::DOUBLE PRECISION, geo_lat) END,
    geo_lng = CASE WHEN $9 THEN NULL ELSE COALESCE($12::DOUBLE PRECISION, geo_lng) END,
    avatar_url = CASE WHEN $13 THEN NULL ELSE COALESCE($14, avatar_url) END,
    username = COALESCE($15, username)
WHERE blockchain_address = $1
RETURNING id, blockchain_address AS ethereum_address, username, name, COALESCE(email, '') AS email, COALESCE(phone, '') AS phone, COALESCE(country, '') AS country, preferred_currency, bio, website, location_name, geo_lat, geo_lng, avatar_url, role, status, max_pools, max_tokens, created_at, updated_at;

-- User shares
--name: insert-user-share
INSERT INTO user_shares (
    tx_hash, owner_user_id, authorized_user_id, share_permission, extra_note
) VALUES (LOWER($1), $2, $3, $4, NULLIF($5, ''))
ON CONFLICT (LOWER(tx_hash)) DO UPDATE
SET tx_hash = EXCLUDED.tx_hash
WHERE user_shares.owner_user_id = EXCLUDED.owner_user_id
  AND user_shares.authorized_user_id = EXCLUDED.authorized_user_id
  AND user_shares.share_permission = EXCLUDED.share_permission
  AND user_shares.extra_note IS NOT DISTINCT FROM EXCLUDED.extra_note
RETURNING id;

--name: get-user-share-by-tx-hash
SELECT us.id,
    us.owner_user_id,
    us.authorized_user_id,
    us.share_permission,
    COALESCE(us.extra_note, '') AS extra_note,
    us.created_at,
    us.updated_at,
    us.tx_hash,
    owner.name AS owner_name,
    CASE WHEN (us.share_permission & 1) = 1 THEN COALESCE(owner.email, '') ELSE '' END AS email,
    CASE WHEN (us.share_permission & 2) = 2 THEN COALESCE(owner.phone, '') ELSE '' END AS phone,
    CASE WHEN (us.share_permission & 4) = 4 THEN COALESCE(owner.location_name, '') ELSE '' END AS location_name,
    CASE WHEN (us.share_permission & 4) = 4 THEN owner.geo_lat ELSE NULL END AS geo_lat,
    CASE WHEN (us.share_permission & 4) = 4 THEN owner.geo_lng ELSE NULL END AS geo_lng
FROM user_shares us
INNER JOIN users owner ON owner.id = us.owner_user_id
WHERE LOWER(us.tx_hash) = LOWER($1)
  AND $2 IN (us.owner_user_id, us.authorized_user_id)
LIMIT 1;

-- Which of the given transaction hashes carry a private attachment the caller
-- may read. Same visibility rule as get-user-share-by-tx-hash (owner or
-- authorized recipient), but for a whole page of transfers at once, so the
-- activity list can flag them without one request per row.
--name: list-user-share-tx-hashes
SELECT DISTINCT LOWER(us.tx_hash) AS tx_hash
FROM user_shares us
WHERE LOWER(us.tx_hash) = ANY($1::text[])
  AND $2 IN (us.owner_user_id, us.authorized_user_id);

-- Snapshot the claim metadata for every catalog voucher referenced by a
-- supported relayer operation. Missing or incomplete metadata deliberately
-- inserts no row; that transfer remains token-only rather than borrowing a
-- later profile value.
--name: insert-transfer-valuation-snapshots
INSERT INTO transfer_valuation_snapshots (
    tracking_id,
    token_address,
    voucher_value,
    voucher_uoa
)
SELECT $1::uuid,
    v.address,
    v.voucher_value,
    btrim(v.voucher_uoa)
FROM vouchers v
WHERE lower(v.address) = ANY($2::text[])
  AND v.voucher_value IS NOT NULL
  AND v.voucher_value > 0
  AND NULLIF(btrim(v.voucher_uoa), '') IS NOT NULL
  AND btrim(v.voucher_uoa) ~ '^[A-Za-z0-9]{2,16}$'
ON CONFLICT (tracking_id, token_address) DO NOTHING
RETURNING token_address, voucher_value::text, voucher_uoa;

-- Resolve snapshots through every OTX sharing the tracking id. This keeps the
-- original submitted valuation attached when a relayed transaction is replaced.
--name: list-transfer-valuation-snapshots-by-tx-hashes
SELECT DISTINCT ON (lower(o.tx_hash), lower(s.token_address))
    lower(o.tx_hash) AS tx_hash,
    s.token_address,
    s.voucher_value::text AS voucher_value,
    s.voucher_uoa
FROM transfer_valuation_snapshots s
INNER JOIN otx o ON o.tracking_id = s.tracking_id
WHERE lower(o.tx_hash) = ANY($1::text[])
ORDER BY lower(o.tx_hash), lower(s.token_address), o.id DESC;

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
SELECT dt.id, dt.otx_id, dt.platform_contract_id, dt.status, dt.address, dt.symbol,
    COALESCE(v.display_name, dt.name) AS name, dt.decimals, dt.admin, dt.created_at, dt.updated_at, o.tracking_id
FROM deployed_tokens dt
INNER JOIN otx o ON dt.otx_id = o.id
INNER JOIN user_deployments ud ON ud.tracking_id = o.tracking_id
LEFT JOIN vouchers v ON v.id = dt.voucher_id
WHERE ud.user_id = $1 AND ud.resource_type = 'TOKEN' AND dt.status = 'CONFIRMED'
ORDER BY dt.id DESC
LIMIT $2;

--name: list-tokens-by-user-next
-- Keyset window is fetched ascending (the rows immediately after the cursor)
-- then re-sorted descending in SQL so the API never reorders rows in memory.
SELECT * FROM (
    SELECT dt.id, dt.otx_id, dt.platform_contract_id, dt.status, dt.address, dt.symbol,
        COALESCE(v.display_name, dt.name) AS name, dt.decimals, dt.admin, dt.created_at, dt.updated_at, o.tracking_id
    FROM deployed_tokens dt
    INNER JOIN otx o ON dt.otx_id = o.id
    INNER JOIN user_deployments ud ON ud.tracking_id = o.tracking_id
    LEFT JOIN vouchers v ON v.id = dt.voucher_id
    WHERE ud.user_id = $1 AND ud.resource_type = 'TOKEN' AND dt.status = 'CONFIRMED' AND dt.id > $2
    ORDER BY dt.id ASC
    LIMIT $3
) page
ORDER BY page.id DESC;

--name: list-tokens-by-user-prev
SELECT dt.id, dt.otx_id, dt.platform_contract_id, dt.status, dt.address, dt.symbol,
    COALESCE(v.display_name, dt.name) AS name, dt.decimals, dt.admin, dt.created_at, dt.updated_at, o.tracking_id
FROM deployed_tokens dt
INNER JOIN otx o ON dt.otx_id = o.id
INNER JOIN user_deployments ud ON ud.tracking_id = o.tracking_id
LEFT JOIN vouchers v ON v.id = dt.voucher_id
WHERE ud.user_id = $1 AND ud.resource_type = 'TOKEN' AND dt.status = 'CONFIRMED' AND dt.id < $2
ORDER BY dt.id DESC
LIMIT $3;

--name: list-pools-by-user
SELECT dp.id, dp.otx_id, dp.platform_contract_id, dp.status, dp.address, dp.name, dp.symbol, dp.decimals,
    dp.token_registry_address, dp.limiter_address, dp.fee_policy_address, dp.quoter_address, dp.quoter_type,
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
    dp.token_registry_address, dp.limiter_address, dp.fee_policy_address, dp.quoter_address, dp.quoter_type,
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
        dp.token_registry_address, dp.limiter_address, dp.fee_policy_address, dp.quoter_address, dp.quoter_type,
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
SELECT dt.id, dt.otx_id, dt.platform_contract_id, dt.status, dt.address, dt.symbol,
    COALESCE(v.display_name, dt.name) AS name, dt.decimals, dt.admin, dt.created_at, dt.updated_at
FROM deployed_tokens dt
LEFT JOIN vouchers v ON v.id = dt.voucher_id
WHERE dt.status = 'CONFIRMED'
ORDER BY dt.created_at DESC;

--name: list-all-pools
SELECT id, otx_id, platform_contract_id, status, address, name, symbol, decimals,
    token_registry_address, limiter_address, fee_policy_address, quoter_address, quoter_type,
    protocol_fee_controller_address, fees_decoupled, admin, image_url, banner_url, created_at, updated_at
FROM deployed_pools
WHERE status = 'CONFIRMED'
ORDER BY created_at DESC;

-- Global confirmed pools (any user). Full metadata + tags for the app pools tab.
--name: list-confirmed-pools
SELECT dp.id, dp.otx_id, dp.platform_contract_id, dp.status, dp.address, dp.name, dp.symbol, dp.decimals,
    dp.token_registry_address, dp.limiter_address, dp.fee_policy_address, dp.quoter_address, dp.quoter_type,
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
    dp.token_registry_address, dp.limiter_address, dp.fee_policy_address, dp.quoter_address, dp.quoter_type,
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
        dp.token_registry_address, dp.limiter_address, dp.fee_policy_address, dp.quoter_address, dp.quoter_type,
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

-- Record a WebAuthn credential carried by a registration relay. Written in the
-- relay worker's transaction, beside the OTX it waits on, and always 'pending':
-- only a successful receipt for tracking_id makes it usable (see
-- settle-webauthn-registration). Conflicts are decided by the caller under
-- get-webauthn-credential-for-update, never silently here.
--name: insert-webauthn-credential
INSERT INTO webauthn_credentials (credential_id, address, key_hash, public_key_x, public_key_y, tracking_id, state)
VALUES ($1, $2, $3, $4, $5, $6::uuid, 'pending');

-- Re-arm a failed registration of the same credential -> address/key mapping
-- under a new tracking id.
--name: rearm-webauthn-credential
UPDATE webauthn_credentials
SET tracking_id = $2::uuid, state = 'pending'
WHERE credential_id = $1 AND state IN ('failed', 'unverified');

-- Any-state lookup, locked, for deciding whether a registration conflicts.
--name: get-webauthn-credential-for-update
SELECT credential_id, address, key_hash, public_key_x::TEXT AS public_key_x, public_key_y::TEXT AS public_key_y,
    tracking_id::TEXT AS tracking_id, state, created_at
FROM webauthn_credentials
WHERE credential_id = $1
FOR UPDATE;

-- Any-state lookup without a lock, for the relay handler's early rejection.
--name: get-webauthn-credential
SELECT credential_id, address, key_hash, public_key_x::TEXT AS public_key_x, public_key_y::TEXT AS public_key_y,
    tracking_id::TEXT AS tracking_id, state, created_at
FROM webauthn_credentials
WHERE credential_id = $1;

-- The only lookup a sign-in or recovery path may use: a credential whose
-- registration the chain confirmed. Pending, failed and legacy rows are
-- indistinguishable from an unknown credential here.
--name: get-active-webauthn-credential
SELECT credential_id, address, key_hash, public_key_x::TEXT AS public_key_x, public_key_y::TEXT AS public_key_y,
    tracking_id::TEXT AS tracking_id, state, created_at
FROM webauthn_credentials
WHERE credential_id = $1 AND state = 'active';

-- Every credential mapped to one wallet, for the holder's own Settings screen.
-- Reads persisted state only; no chain call sits behind this.
--name: list-webauthn-credentials-by-address
SELECT credential_id, address, key_hash, public_key_x::TEXT AS public_key_x, public_key_y::TEXT AS public_key_y,
    tracking_id::TEXT AS tracking_id, state, created_at
FROM webauthn_credentials
WHERE address = $1
ORDER BY created_at DESC, credential_id;

-- Re-derive a pending registration's state from every transaction carrying its
-- tracking id, after one of them changed status. A fee bump or republish adds a
-- replacement row under the same tracking id, so one row's REVERTED or LOW_NONCE
-- is not the outcome while another row is still in flight:
--   any SUCCESS                      -> active
--   every row terminal, none SUCCESS -> failed
--   otherwise                        -> still pending
-- The terminal set matches store.terminalDispatchStatuses.
--name: settle-webauthn-registration
UPDATE webauthn_credentials c
SET state = CASE
    WHEN EXISTS (
        SELECT 1 FROM otx o INNER JOIN dispatch d ON d.otx_id = o.id
        WHERE o.tracking_id = c.tracking_id AND d.status = 'SUCCESS'
    ) THEN 'active'
    WHEN NOT EXISTS (
        SELECT 1 FROM otx o INNER JOIN dispatch d ON d.otx_id = o.id
        WHERE o.tracking_id = c.tracking_id
          AND d.status NOT IN ('SUCCESS', 'REVERTED', 'LOW_NONCE', 'INVALID_TX')
    ) THEN 'failed'
    ELSE 'pending'
END
WHERE c.tracking_id = (SELECT tracking_id FROM otx WHERE id = $1)
  AND c.state = 'pending';

-- Rows written before migration 038, awaiting the one-time chain check.
--name: list-legacy-webauthn-credentials
SELECT credential_id, address, key_hash, public_key_x::TEXT AS public_key_x, public_key_y::TEXT AS public_key_y,
    tracking_id::TEXT AS tracking_id, state, created_at
FROM webauthn_credentials
WHERE state = 'legacy'
ORDER BY created_at, credential_id;

-- Move one legacy row to its verdict. Guarded on state so a concurrent
-- registration of the same credential is never overwritten.
--name: resolve-legacy-webauthn-credential
UPDATE webauthn_credentials
SET state = $2
WHERE credential_id = $1 AND state = 'legacy';

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
SELECT address, symbol, COALESCE(display_name, name) AS name, decimals, icon_url
FROM vouchers
WHERE lower(address) = ANY($1);

-- Pool catalog directory (MIGRATION.md §3.5) — reads come from the pools
-- catalog, not the deploy pipeline. Cursor-paginated newest-first by id,
-- tags aggregated from pool_tags.
--name: list-pools
SELECT p.id, p.chain_id, p.address, COALESCE(p.display_name, p.name) AS name,
    p.name AS contract_name, p.symbol, p.owner_address, p.fee_ppm,
    p.fee_address, p.token_registry_address, p.limiter_address, p.quoter_address, p.quoter_type,
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
SELECT p.id, p.chain_id, p.address, COALESCE(p.display_name, p.name) AS name,
    p.name AS contract_name, p.symbol, p.owner_address, p.fee_ppm,
    p.fee_address, p.token_registry_address, p.limiter_address, p.quoter_address, p.quoter_type,
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
    SELECT p.id, p.chain_id, p.address, COALESCE(p.display_name, p.name) AS name,
        p.name AS contract_name, p.symbol, p.owner_address, p.fee_ppm,
        p.fee_address, p.token_registry_address, p.limiter_address, p.quoter_address, p.quoter_type,
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
SELECT p.id, p.chain_id, p.address, COALESCE(p.display_name, p.name) AS name,
    p.name AS contract_name, p.symbol, p.owner_address, p.fee_ppm,
    p.fee_address, p.token_registry_address, p.limiter_address, p.quoter_address, p.quoter_type,
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

-- The owner profile route authorizes from this row and writes it in the same
-- transaction. Imported pools may have no owner; only a superadmin may manage
-- those through the admin route.
--name: lock-pool-for-profile-update
SELECT id, owner_address
FROM pools
WHERE lower(address) = lower($1)
FOR UPDATE;

-- Owner or operator edits to a pool's off-chain record.
--
-- Only catalog metadata: nothing here has an on-chain counterpart, so an edit
-- cannot put the row out of step with the contract. Name, symbol, decimals and
-- every dependency address are deliberately absent — those mirror SwapPool, and
-- a catalog that disagrees with the chain about them is worse than one that
-- cannot edit them.
--
-- Assign-all rather than patch: the caller sends the whole record and every
-- column is written, so clearing a field is expressing it as empty rather than
-- omitting it. pools_geo_valid backstops the both-or-neither coordinate rule the
-- handler checks first.
--name: update-pool-profile
-- Assign-all for everything the console has always sent, and preserve-on-omit
-- semantics for display_name and the two image columns.
--
-- The distinction is not stylistic. Until now this statement did not touch
-- image_url or banner_url at all, which is the only reason a pool's images survive
-- an operator editing its description. Making them assign-all would have the
-- already-deployed console — which sends no image fields — blank them on every
-- save. Omitted therefore preserves, and '' clears, matching update-user-profile.
-- Once every client sends the fields this can collapse to a plain assignment.
UPDATE pools
SET display_name    = CASE WHEN $2::TEXT IS NULL THEN display_name ELSE NULLIF(btrim($2), '') END,
    description     = $3,
    unit_of_account = $4,
    email           = $5,
    website         = $6,
    phone           = $7,
    terms           = $8,
    location_name   = $9,
    geo_lat         = $10,
    geo_lng         = $11,
    image_url       = CASE WHEN $12::TEXT IS NULL THEN image_url  ELSE NULLIF($12, '') END,
    banner_url      = CASE WHEN $13::TEXT IS NULL THEN banner_url ELSE NULLIF($13, '') END
WHERE lower(address) = lower($1)
RETURNING id;

-- Tags are a junction, so a replace is a clear and a re-insert. Both run inside
-- the caller's transaction with the profile update, or an operator who mistypes
-- one tag loses the rest.
--name: delete-pool-tags
DELETE FROM pool_tags WHERE pool_id = $1;

--name: insert-pool-tag
INSERT INTO pool_tags (pool_id, tag_value) VALUES ($1, $2)
ON CONFLICT (pool_id, tag_value) DO NOTHING;

-- A superadmin's edits to a voucher's off-chain record.
--
-- display_name is deliberately separate from name: it is a public catalog
-- label, while name continues to mirror the deployed ERC20. Symbol, decimals
-- and owner_address are also chain facts. active, internal, voucher_type
-- and the sink and redemption addresses — those are not catalog prose, and
-- retiring a voucher is not the same operation as correcting its email.
--
-- Assign-all rather than patch, matching update-pool-profile: the caller sends
-- the whole record and every column is written, so clearing a field is
-- expressing it as empty rather than omitting it. vouchers_geo_valid backstops
-- the both-or-neither coordinate rule the handler checks first.
--
-- display_name, the two image columns and the two marker columns are the
-- exceptions, for the reason update-pool-profile gives: NULL preserves and ''
-- clears, so a client that sends no image or marker fields cannot blank them.
-- vouchers_image_urls_http rejects anything that is not http(s), and the marker
-- foreign keys anything unseeded, which is why '' has to become NULL rather
-- than staying an empty string.
--name: update-voucher-profile
UPDATE vouchers
SET display_name  = CASE WHEN $2::TEXT IS NULL THEN display_name ELSE NULLIF(btrim($2), '') END,
    description   = $3,
    voucher_uoa   = $4,
    voucher_value = $5,
    email         = $6,
    website       = $7,
    phone         = $8,
    terms         = $9,
    location_name = $10,
    geo_lat       = $11,
    geo_lng       = $12,
    icon_url      = CASE WHEN $13::TEXT IS NULL THEN icon_url   ELSE NULLIF($13, '') END,
    banner_url    = CASE WHEN $14::TEXT IS NULL THEN banner_url ELSE NULLIF($14, '') END,
    marker_icon   = CASE WHEN $15::TEXT IS NULL THEN marker_icon  ELSE NULLIF($15, '') END,
    marker_color  = CASE WHEN $16::TEXT IS NULL THEN marker_color ELSE NULLIF($16, '') END
WHERE lower(address) = lower($1)
RETURNING id;

-- A voucher owner may correct the public label without receiving access to the
-- rest of the operator profile. Authorization happens against the locked row in
-- the handler; taking the database id here prevents a second address lookup.
--name: update-voucher-display-name
UPDATE vouchers
SET display_name = $2
WHERE id = $1;

-- Retiring and restoring are the same column write; only who may ask differs,
-- and that is decided in the handler against the locked row. `active` is the
-- catalog's visibility gate — every market, search and offering statement
-- filters on it — so flipping it here removes a voucher from the public
-- catalog without touching the report and offering rows that reference it
-- (42.md 3.1: deactivate, do not delete).
--name: set-voucher-active
UPDATE vouchers
SET active = $2,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1;

-- Tags are a junction, so a replace is a clear and a re-insert, both inside the
-- caller's transaction with the profile update — as for pools, an operator who
-- mistypes one tag must not lose the rest.
--name: delete-voucher-tags
DELETE FROM voucher_tags WHERE voucher_id = $1;

--name: insert-voucher-tag
INSERT INTO voucher_tags (voucher_id, tag_value) VALUES ($1, $2)
ON CONFLICT (voucher_id, tag_value) DO NOTHING;

-- Pool composition projected from chain events (TRACKER.* via internal/sub).
--
-- The chain is the source of truth for what a pool holds: nothing else writes
-- pool_tokens, and clc-core is not always the party that signed the change — an
-- operator console, a script, or another relayer can all move the same state.
-- Each statement therefore resolves its own ids from addresses and upserts, so a
-- redelivered NATS message is a no-op rather than a duplicate.
--
-- Ownership of columns is split along the contract that emits them, and no
-- statement writes a column it did not hear about:
--
--   is_allowed     the token registry (INDEX_ADD / INDEX_REMOVE)
--   token_limit    the limiter        (LIMIT_SET)
--   oracle_feed_id the quoter         (ORACLE_UPDATED / ORACLE_REMOVED)
--
-- The two non-registry statements insert is_allowed = FALSE when they are the
-- first to see a pair, because a cap or a price says nothing about membership;
-- their ON CONFLICT clauses leave it alone so an INDEX_ADD that already landed
-- is not undone by a later cap.

--name: set-pool-token-membership
INSERT INTO pool_tokens (pool_id, voucher_id, is_allowed, synced_at)
SELECT p.id, v.id, $3, CURRENT_TIMESTAMP
FROM pools p
JOIN vouchers v ON v.chain_id = p.chain_id
WHERE lower(p.token_registry_address) = lower($1)
  AND lower(v.address) = lower($2)
ON CONFLICT (pool_id, voucher_id) DO UPDATE
SET is_allowed = EXCLUDED.is_allowed,
    synced_at  = EXCLUDED.synced_at;

--name: set-pool-token-limit
INSERT INTO pool_tokens (pool_id, voucher_id, is_allowed, token_limit, synced_at)
SELECT p.id, v.id, FALSE, $3::NUMERIC, CURRENT_TIMESTAMP
FROM pools p
JOIN vouchers v ON v.chain_id = p.chain_id
WHERE lower(p.address) = lower($1)
  AND lower(v.address) = lower($2)
ON CONFLICT (pool_id, voucher_id) DO UPDATE
SET token_limit = EXCLUDED.token_limit,
    synced_at   = EXCLUDED.synced_at;

-- An aggregator with no oracle_feeds row resolves to NULL: the mapping is real
-- but this service cannot name the feed, which is a different fact from having
-- no mapping and is why the join is outer rather than inner.
--
-- The lateral is not decoration. oracle_feeds is unique on name, not address, and
-- some feeds share an aggregator — USDC and EURC both point at 0xc7A353Ba… — so a
-- plain join returns two rows, the insert proposes the same (pool, voucher) pair
-- twice, and Postgres aborts the whole statement with "ON CONFLICT DO UPDATE
-- command cannot affect row a second time". Picking the lowest id keeps one
-- deterministic answer; by address the two feeds are the same contract anyway.
--name: set-pool-token-oracle
INSERT INTO pool_tokens (pool_id, voucher_id, is_allowed, oracle_feed_id, synced_at)
SELECT p.id, v.id, FALSE, f.id, CURRENT_TIMESTAMP
FROM pools p
JOIN vouchers v ON v.chain_id = p.chain_id
LEFT JOIN LATERAL (
    SELECT id FROM oracle_feeds
    WHERE chain_id = p.chain_id AND lower(address) = lower($3)
    ORDER BY id LIMIT 1
) f ON TRUE
WHERE lower(p.quoter_address) = lower($1)
  AND lower(v.address) = lower($2)
ON CONFLICT (pool_id, voucher_id) DO UPDATE
SET oracle_feed_id = EXCLUDED.oracle_feed_id,
    synced_at      = EXCLUDED.synced_at;

-- Removal only ever clears an existing pairing; it must not conjure a row for a
-- pool that never priced the token.
--name: clear-pool-token-oracle
UPDATE pool_tokens pt
SET oracle_feed_id = NULL,
    synced_at      = CURRENT_TIMESTAMP
FROM pools p, vouchers v
WHERE pt.pool_id = p.id
  AND pt.voucher_id = v.id
  AND v.chain_id = p.chain_id
  AND lower(p.quoter_address) = lower($1)
  AND lower(v.address) = lower($2);

-- Pool voucher composition (MIGRATION.md §3.4). Oracle feeds replace legacy
-- fixed exchange rates; offering counts are local catalog facts.
--name: list-pool-vouchers
SELECT v.id, v.chain_id, v.address, COALESCE(v.display_name, v.name) AS name, v.symbol, v.decimals, v.owner_address,
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
SELECT v.id, v.chain_id, v.address, COALESCE(v.display_name, v.name) AS name, v.symbol, v.decimals, v.owner_address,
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
    SELECT v.id, v.chain_id, v.address, COALESCE(v.display_name, v.name) AS name, v.symbol, v.decimals, v.owner_address,
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
    COALESCE(v.display_name, v.name) AS voucher_name, v.symbol AS voucher_symbol,
    v.voucher_uoa AS voucher_uoa,
    CASE WHEN o.price > 0 AND NULLIF(BTRIM(o.unit), '') IS NOT NULL
        AND v.voucher_value > 0 AND NULLIF(BTRIM(v.voucher_uoa), '') IS NOT NULL
        THEN (o.price * v.voucher_value)::TEXT END AS price_in_unit_of_account,
    v.decimals AS voucher_decimals,
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
    COALESCE(v.display_name, v.name) AS voucher_name, v.symbol AS voucher_symbol,
    v.voucher_uoa AS voucher_uoa,
    CASE WHEN o.price > 0 AND NULLIF(BTRIM(o.unit), '') IS NOT NULL
        AND v.voucher_value > 0 AND NULLIF(BTRIM(v.voucher_uoa), '') IS NOT NULL
        THEN (o.price * v.voucher_value)::TEXT END AS price_in_unit_of_account,
    v.decimals AS voucher_decimals,
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
        COALESCE(v.display_name, v.name) AS voucher_name, v.symbol AS voucher_symbol,
        v.voucher_uoa AS voucher_uoa,
        CASE WHEN o.price > 0 AND NULLIF(BTRIM(o.unit), '') IS NOT NULL
            AND v.voucher_value > 0 AND NULLIF(BTRIM(v.voucher_uoa), '') IS NOT NULL
            THEN (o.price * v.voucher_value)::TEXT END AS price_in_unit_of_account,
        v.decimals AS voucher_decimals,
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
    COALESCE(v.display_name, v.name) AS voucher_name, v.symbol AS voucher_symbol,
    v.voucher_uoa AS voucher_uoa,
    CASE WHEN o.price > 0 AND NULLIF(BTRIM(o.unit), '') IS NOT NULL
        AND v.voucher_value > 0 AND NULLIF(BTRIM(v.voucher_uoa), '') IS NOT NULL
        THEN (o.price * v.voucher_value)::TEXT END AS price_in_unit_of_account,
    v.decimals AS voucher_decimals,
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
    COALESCE(v.display_name, v.name) AS voucher_name, v.symbol AS voucher_symbol,
    v.voucher_uoa AS voucher_uoa,
    CASE WHEN o.price > 0 AND NULLIF(BTRIM(o.unit), '') IS NOT NULL
        AND v.voucher_value > 0 AND NULLIF(BTRIM(v.voucher_uoa), '') IS NOT NULL
        THEN (o.price * v.voucher_value)::TEXT END AS price_in_unit_of_account,
    v.decimals AS voucher_decimals,
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
        COALESCE(v.display_name, v.name) AS voucher_name, v.symbol AS voucher_symbol,
        v.voucher_uoa AS voucher_uoa,
        CASE WHEN o.price > 0 AND NULLIF(BTRIM(o.unit), '') IS NOT NULL
            AND v.voucher_value > 0 AND NULLIF(BTRIM(v.voucher_uoa), '') IS NOT NULL
            THEN (o.price * v.voucher_value)::TEXT END AS price_in_unit_of_account,
        v.decimals AS voucher_decimals,
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
    COALESCE(v.display_name, v.name) AS voucher_name, v.symbol AS voucher_symbol,
    v.voucher_uoa AS voucher_uoa,
    CASE WHEN o.price > 0 AND NULLIF(BTRIM(o.unit), '') IS NOT NULL
        AND v.voucher_value > 0 AND NULLIF(BTRIM(v.voucher_uoa), '') IS NOT NULL
        THEN (o.price * v.voucher_value)::TEXT END AS price_in_unit_of_account,
    v.decimals AS voucher_decimals,
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
    COALESCE(v.display_name, v.name) AS voucher_name, v.symbol AS voucher_symbol,
    v.voucher_uoa AS voucher_uoa,
    CASE WHEN o.price > 0 AND NULLIF(BTRIM(o.unit), '') IS NOT NULL
        AND v.voucher_value > 0 AND NULLIF(BTRIM(v.voucher_uoa), '') IS NOT NULL
        THEN (o.price * v.voucher_value)::TEXT END AS price_in_unit_of_account,
    v.decimals AS voucher_decimals,
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
        COALESCE(v.display_name, v.name) AS voucher_name, v.symbol AS voucher_symbol,
        v.voucher_uoa AS voucher_uoa,
        CASE WHEN o.price > 0 AND NULLIF(BTRIM(o.unit), '') IS NOT NULL
            AND v.voucher_value > 0 AND NULLIF(BTRIM(v.voucher_uoa), '') IS NOT NULL
            THEN (o.price * v.voucher_value)::TEXT END AS price_in_unit_of_account,
        v.decimals AS voucher_decimals,
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

-- Offering writes.
--
-- One offering belongs to exactly one voucher, and authority follows that
-- relationship rather than the historical seller_address an import carried. Every
-- write therefore locks the voucher (or the offering joined to it) and reads the
-- owner from the locked row in the same transaction, so a caller cannot be
-- authorized against a stale catalog copy.
--
-- The projection is deliberately the same one the list statements use: item read
-- and mutation read-back return the identical wire shape, so a client never has
-- to reconcile two representations of the row it just saved.
--name: get-voucher-offering
SELECT o.id, o.source_id, o.voucher_id, v.address AS voucher_address,
    COALESCE(v.display_name, v.name) AS voucher_name, v.symbol AS voucher_symbol,
    v.voucher_uoa AS voucher_uoa,
    CASE WHEN o.price > 0 AND NULLIF(BTRIM(o.unit), '') IS NOT NULL
        AND v.voucher_value > 0 AND NULLIF(BTRIM(v.voucher_uoa), '') IS NOT NULL
        THEN (o.price * v.voucher_value)::TEXT END AS price_in_unit_of_account,
    v.decimals AS voucher_decimals,
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
WHERE lower(v.address) = lower($1) AND o.id = $2 AND o.available AND v.active;

-- The same projection ignoring availability and voucher activity. A write reads
-- its own result back through this one: a row is still the caller's immediately
-- after DELETE unlisted it, and the public statement above would return nothing.
--name: get-offering-for-mutation
SELECT o.id, o.source_id, o.voucher_id, v.address AS voucher_address,
    COALESCE(v.display_name, v.name) AS voucher_name, v.symbol AS voucher_symbol,
    v.voucher_uoa AS voucher_uoa,
    CASE WHEN o.price > 0 AND NULLIF(BTRIM(o.unit), '') IS NOT NULL
        AND v.voucher_value > 0 AND NULLIF(BTRIM(v.voucher_uoa), '') IS NOT NULL
        THEN (o.price * v.voucher_value)::TEXT END AS price_in_unit_of_account,
    v.decimals AS voucher_decimals,
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
WHERE o.id = $1;

-- Public offering discovery. Global and nearby shapes are separate so the
-- nearby statement exposes its bounding-box predicate directly to the planner.
-- The boolean term mode is a validated server choice: short terms use an
-- index-friendly prefix, longer terms use substring matching.
--name: list-offerings-filtered-global
WITH candidates AS MATERIALIZED (
    SELECT o.id, NULL::float8 AS distance_meters
    FROM offerings o
    INNER JOIN vouchers v ON v.id = o.voucher_id
    WHERE o.available AND v.active
      AND COALESCE(o.geo_lat, v.geo_lat) IS NOT NULL
      AND COALESCE(o.geo_lng, v.geo_lng) IS NOT NULL
      AND ($1 = '' OR CASE WHEN $2 THEN (
            lower(o.commodity_name) LIKE lower($1) || '%' ESCAPE '\'
         OR lower(o.description) LIKE lower($1) || '%' ESCAPE '\'
         OR lower(COALESCE(v.display_name, v.name, '')) LIKE lower($1) || '%' ESCAPE '\'
         OR lower(COALESCE(v.symbol, '')) LIKE lower($1) || '%' ESCAPE '\'
         OR EXISTS (SELECT 1 FROM offering_tags search_ot
                    WHERE search_ot.offering_id = o.id
                      AND lower(search_ot.tag_value) LIKE lower($1) || '%' ESCAPE '\')
        ) ELSE (
            lower(o.commodity_name) LIKE '%' || lower($1) || '%' ESCAPE '\'
         OR lower(o.description) LIKE '%' || lower($1) || '%' ESCAPE '\'
         OR lower(COALESCE(v.display_name, v.name, '')) LIKE '%' || lower($1) || '%' ESCAPE '\'
         OR lower(COALESCE(v.symbol, '')) LIKE '%' || lower($1) || '%' ESCAPE '\'
         OR EXISTS (SELECT 1 FROM offering_tags search_ot
                    WHERE search_ot.offering_id = o.id
                      AND lower(search_ot.tag_value) LIKE '%' || lower($1) || '%' ESCAPE '\')
        ) END)
      AND (cardinality($3::TEXT[]) = 0 OR CASE WHEN $4 THEN
            (SELECT COUNT(*) FROM offering_tags selected_ot
             WHERE selected_ot.offering_id = o.id
               AND selected_ot.tag_value = ANY($3::TEXT[])) = cardinality($3::TEXT[])
          ELSE EXISTS (SELECT 1 FROM offering_tags selected_ot
                       WHERE selected_ot.offering_id = o.id
                         AND selected_ot.tag_value = ANY($3::TEXT[])) END)
      AND (cardinality($5::BIGINT[]) = 0 OR EXISTS (
            SELECT 1 FROM pool_tokens pt
            WHERE pt.voucher_id = v.id AND pt.is_allowed
              AND pt.pool_id = ANY($5::BIGINT[])))
), page AS MATERIALIZED (
    SELECT id, distance_meters FROM candidates
    WHERE $6::BIGINT = 0 OR id < $6
    ORDER BY id DESC
    LIMIT $7
)
SELECT o.id, o.source_id, o.voucher_id, v.address AS voucher_address,
    COALESCE(v.display_name, v.name) AS voucher_name, v.symbol AS voucher_symbol,
    v.voucher_uoa AS voucher_uoa,
    CASE WHEN o.price > 0 AND NULLIF(BTRIM(o.unit), '') IS NOT NULL
        AND v.voucher_value > 0 AND NULLIF(BTRIM(v.voucher_uoa), '') IS NOT NULL
        THEN (o.price * v.voucher_value)::TEXT END AS price_in_unit_of_account,
    v.decimals AS voucher_decimals, v.icon_url AS voucher_image_url,
    v.location_name AS voucher_location_name, v.geo_lat AS voucher_geo_lat,
    v.geo_lng AS voucher_geo_lng, o.seller_address, o.commodity_name,
    o.commodity_type, o.description, o.quantity::TEXT AS quantity, o.unit,
    o.frequency, o.price::TEXT AS price, o.available, o.location_name,
    o.geo_lat, o.geo_lng, o.image_url,
    COALESCE(o.location_name, v.location_name) AS effective_location_name,
    COALESCE(o.geo_lat, v.geo_lat) AS effective_geo_lat,
    COALESCE(o.geo_lng, v.geo_lng) AS effective_geo_lng,
    page.distance_meters,
    COALESCE((SELECT array_agg(ot.tag_value ORDER BY ot.tag_value)
              FROM offering_tags ot WHERE ot.offering_id = o.id), ARRAY[]::TEXT[]) AS tags,
    o.created_at, o.updated_at
FROM page
INNER JOIN offerings o ON o.id = page.id
INNER JOIN vouchers v ON v.id = o.voucher_id
ORDER BY page.id DESC;

--name: list-offerings-filtered-nearby
WITH candidates AS MATERIALIZED (
    SELECT o.id,
        2 * 6371008.8 * asin(sqrt(least(1.0, greatest(0.0,
            power(sin(radians(COALESCE(o.geo_lat, v.geo_lat) - $6::float8) / 2), 2) +
            cos(radians($6::float8)) * cos(radians(COALESCE(o.geo_lat, v.geo_lat))) *
            power(sin(radians(COALESCE(o.geo_lng, v.geo_lng) - $7::float8) / 2), 2)
        )))) AS distance_meters
    FROM offerings o
    INNER JOIN vouchers v ON v.id = o.voucher_id
    WHERE o.available AND v.active
      AND COALESCE(o.geo_lat, v.geo_lat) IS NOT NULL
      AND COALESCE(o.geo_lng, v.geo_lng) IS NOT NULL
      AND point(COALESCE(o.geo_lng, v.geo_lng), COALESCE(o.geo_lat, v.geo_lat))
          <@ box(point($8, $9), point($10, $11))
      AND CASE WHEN COALESCE(o.geo_lat, v.geo_lat) BETWEEN -90 AND 90
                    AND COALESCE(o.geo_lng, v.geo_lng) BETWEEN -180 AND 180
          THEN 2 * 6371008.8 * asin(sqrt(least(1.0, greatest(0.0,
              power(sin(radians(COALESCE(o.geo_lat, v.geo_lat) - $6::float8) / 2), 2) +
              cos(radians($6::float8)) * cos(radians(COALESCE(o.geo_lat, v.geo_lat))) *
              power(sin(radians(COALESCE(o.geo_lng, v.geo_lng) - $7::float8) / 2), 2)
          )))) <= 1000000 ELSE false END
      AND ($1 = '' OR CASE WHEN $2 THEN (
            lower(o.commodity_name) LIKE lower($1) || '%' ESCAPE '\'
         OR lower(o.description) LIKE lower($1) || '%' ESCAPE '\'
         OR lower(COALESCE(v.display_name, v.name, '')) LIKE lower($1) || '%' ESCAPE '\'
         OR lower(COALESCE(v.symbol, '')) LIKE lower($1) || '%' ESCAPE '\'
         OR EXISTS (SELECT 1 FROM offering_tags search_ot WHERE search_ot.offering_id = o.id
                     AND lower(search_ot.tag_value) LIKE lower($1) || '%' ESCAPE '\')
        ) ELSE (
            lower(o.commodity_name) LIKE '%' || lower($1) || '%' ESCAPE '\'
         OR lower(o.description) LIKE '%' || lower($1) || '%' ESCAPE '\'
         OR lower(COALESCE(v.display_name, v.name, '')) LIKE '%' || lower($1) || '%' ESCAPE '\'
         OR lower(COALESCE(v.symbol, '')) LIKE '%' || lower($1) || '%' ESCAPE '\'
         OR EXISTS (SELECT 1 FROM offering_tags search_ot WHERE search_ot.offering_id = o.id
                     AND lower(search_ot.tag_value) LIKE '%' || lower($1) || '%' ESCAPE '\')
        ) END)
      AND (cardinality($3::TEXT[]) = 0 OR CASE WHEN $4 THEN
            (SELECT COUNT(*) FROM offering_tags selected_ot
             WHERE selected_ot.offering_id = o.id
               AND selected_ot.tag_value = ANY($3::TEXT[])) = cardinality($3::TEXT[])
          ELSE EXISTS (SELECT 1 FROM offering_tags selected_ot
                       WHERE selected_ot.offering_id = o.id
                         AND selected_ot.tag_value = ANY($3::TEXT[])) END)
      AND (cardinality($5::BIGINT[]) = 0 OR EXISTS (
            SELECT 1 FROM pool_tokens pt WHERE pt.voucher_id = v.id AND pt.is_allowed
              AND pt.pool_id = ANY($5::BIGINT[])))
), page AS MATERIALIZED (
    SELECT id, distance_meters FROM candidates
    WHERE $13::BIGINT = 0 OR (distance_meters, id) > ($12::float8, $13)
    ORDER BY distance_meters ASC, id ASC
    LIMIT $14
)
SELECT o.id, o.source_id, o.voucher_id, v.address AS voucher_address,
    COALESCE(v.display_name, v.name) AS voucher_name, v.symbol AS voucher_symbol,
    v.voucher_uoa AS voucher_uoa,
    CASE WHEN o.price > 0 AND NULLIF(BTRIM(o.unit), '') IS NOT NULL
        AND v.voucher_value > 0 AND NULLIF(BTRIM(v.voucher_uoa), '') IS NOT NULL
        THEN (o.price * v.voucher_value)::TEXT END AS price_in_unit_of_account,
    v.decimals AS voucher_decimals, v.icon_url AS voucher_image_url,
    v.location_name AS voucher_location_name, v.geo_lat AS voucher_geo_lat,
    v.geo_lng AS voucher_geo_lng, o.seller_address, o.commodity_name,
    o.commodity_type, o.description, o.quantity::TEXT AS quantity, o.unit,
    o.frequency, o.price::TEXT AS price, o.available, o.location_name,
    o.geo_lat, o.geo_lng, o.image_url,
    COALESCE(o.location_name, v.location_name) AS effective_location_name,
    COALESCE(o.geo_lat, v.geo_lat) AS effective_geo_lat,
    COALESCE(o.geo_lng, v.geo_lng) AS effective_geo_lng,
    page.distance_meters,
    COALESCE((SELECT array_agg(ot.tag_value ORDER BY ot.tag_value)
              FROM offering_tags ot WHERE ot.offering_id = o.id), ARRAY[]::TEXT[]) AS tags,
    o.created_at, o.updated_at
FROM page
INNER JOIN offerings o ON o.id = page.id
INNER JOIN vouchers v ON v.id = o.voucher_id
ORDER BY page.distance_meters ASC, page.id ASC;

-- Offering facets deliberately exclude the selected-tag reducer so users can
-- see the complete tag vocabulary under the current search, pool, and location.
--name: list-offering-tag-counts-global
WITH candidates AS MATERIALIZED (
    SELECT o.id FROM offerings o
    INNER JOIN vouchers v ON v.id = o.voucher_id
    WHERE o.available AND v.active
      AND COALESCE(o.geo_lat, v.geo_lat) IS NOT NULL
      AND COALESCE(o.geo_lng, v.geo_lng) IS NOT NULL
      AND ($1 = '' OR CASE WHEN $2 THEN (
            lower(o.commodity_name) LIKE lower($1) || '%' ESCAPE '\'
         OR lower(o.description) LIKE lower($1) || '%' ESCAPE '\'
         OR lower(COALESCE(v.display_name, v.name, '')) LIKE lower($1) || '%' ESCAPE '\'
         OR lower(COALESCE(v.symbol, '')) LIKE lower($1) || '%' ESCAPE '\'
         OR EXISTS (SELECT 1 FROM offering_tags s WHERE s.offering_id=o.id AND lower(s.tag_value) LIKE lower($1) || '%' ESCAPE '\')
        ) ELSE (
            lower(o.commodity_name) LIKE '%' || lower($1) || '%' ESCAPE '\'
         OR lower(o.description) LIKE '%' || lower($1) || '%' ESCAPE '\'
         OR lower(COALESCE(v.display_name, v.name, '')) LIKE '%' || lower($1) || '%' ESCAPE '\'
         OR lower(COALESCE(v.symbol, '')) LIKE '%' || lower($1) || '%' ESCAPE '\'
         OR EXISTS (SELECT 1 FROM offering_tags s WHERE s.offering_id=o.id AND lower(s.tag_value) LIKE '%' || lower($1) || '%' ESCAPE '\')
        ) END)
      AND (cardinality($3::BIGINT[]) = 0 OR EXISTS (
            SELECT 1 FROM pool_tokens pt WHERE pt.voucher_id=v.id AND pt.is_allowed AND pt.pool_id=ANY($3::BIGINT[])))
)
SELECT ot.tag_value, COUNT(*) AS match_count FROM candidates c
INNER JOIN offering_tags ot ON ot.offering_id=c.id GROUP BY ot.tag_value;

--name: list-offering-tag-counts-nearby
WITH candidates AS MATERIALIZED (
    SELECT o.id FROM offerings o
    INNER JOIN vouchers v ON v.id = o.voucher_id
    WHERE o.available AND v.active
      AND COALESCE(o.geo_lat, v.geo_lat) IS NOT NULL AND COALESCE(o.geo_lng, v.geo_lng) IS NOT NULL
      AND point(COALESCE(o.geo_lng,v.geo_lng),COALESCE(o.geo_lat,v.geo_lat)) <@ box(point($6,$7),point($8,$9))
      AND CASE WHEN COALESCE(o.geo_lat,v.geo_lat) BETWEEN -90 AND 90 AND COALESCE(o.geo_lng,v.geo_lng) BETWEEN -180 AND 180
          THEN 2 * 6371008.8 * asin(sqrt(least(1.0,greatest(0.0,
            power(sin(radians(COALESCE(o.geo_lat,v.geo_lat)-$4::float8)/2),2)+
            cos(radians($4::float8))*cos(radians(COALESCE(o.geo_lat,v.geo_lat)))*
            power(sin(radians(COALESCE(o.geo_lng,v.geo_lng)-$5::float8)/2),2)
          )))) <= 1000000 ELSE false END
      AND ($1 = '' OR CASE WHEN $2 THEN (
            lower(o.commodity_name) LIKE lower($1)||'%' ESCAPE '\' OR lower(o.description) LIKE lower($1)||'%' ESCAPE '\'
         OR lower(COALESCE(v.display_name,v.name,'')) LIKE lower($1)||'%' ESCAPE '\' OR lower(COALESCE(v.symbol,'')) LIKE lower($1)||'%' ESCAPE '\'
         OR EXISTS (SELECT 1 FROM offering_tags s WHERE s.offering_id=o.id AND lower(s.tag_value) LIKE lower($1)||'%' ESCAPE '\')
        ) ELSE (
            lower(o.commodity_name) LIKE '%'||lower($1)||'%' ESCAPE '\' OR lower(o.description) LIKE '%'||lower($1)||'%' ESCAPE '\'
         OR lower(COALESCE(v.display_name,v.name,'')) LIKE '%'||lower($1)||'%' ESCAPE '\' OR lower(COALESCE(v.symbol,'')) LIKE '%'||lower($1)||'%' ESCAPE '\'
         OR EXISTS (SELECT 1 FROM offering_tags s WHERE s.offering_id=o.id AND lower(s.tag_value) LIKE '%'||lower($1)||'%' ESCAPE '\')
        ) END)
      AND (cardinality($3::BIGINT[]) = 0 OR EXISTS (
            SELECT 1 FROM pool_tokens pt WHERE pt.voucher_id=v.id AND pt.is_allowed AND pt.pool_id=ANY($3::BIGINT[])))
)
SELECT ot.tag_value, COUNT(*) AS match_count FROM candidates c
INNER JOIN offering_tags ot ON ot.offering_id=c.id GROUP BY ot.tag_value;

-- Create authorizes against the voucher, so the voucher row is what gets locked.
-- Returning owner_address and active lets one round trip answer 404 (no such
-- voucher), 409 (retired) and 403 (not the owner).
--name: lock-voucher-for-offering-create
SELECT id, owner_address, active
FROM vouchers
WHERE lower(address) = lower($1)
FOR UPDATE;

-- Update and delete authorize against the offering's parent voucher. FOR UPDATE
-- OF o locks the offering without locking the voucher: two sellers editing two
-- offerings on one voucher must not serialize against each other.
--
-- The join to the named address is what makes an id belonging to another voucher
-- a 404 rather than a cross-parent write.
--name: lock-offering-voucher-owner
SELECT o.id, o.voucher_id, o.available, v.owner_address, v.active
FROM offerings o
INNER JOIN vouchers v ON v.id = o.voucher_id
WHERE o.id = $1 AND lower(v.address) = lower($2)
FOR UPDATE OF o;

-- The six product fields a caller owns, plus the compatibility values the legacy
-- import columns still require. quantity is 1 because the column is NOT NULL and
-- this product does not manage stock -- it must never be rendered as "1
-- available". seller_address is server-selected from the locked voucher owner (or
-- the authenticated superadmin for an ownerless voucher) and never read from JSON.
--name: create-offering
INSERT INTO offerings (
    voucher_id, seller_address, commodity_name, commodity_type, description,
    quantity, unit, frequency, price, available,
    location_name, geo_lat, geo_lng, image_url
)
VALUES ($1, $2, $3, $4, $5, 1, $6, NULL, $7::NUMERIC, true, NULL, NULL, NULL, $8)
RETURNING id;

-- Assign-all editing of the product fields only. voucher_id, seller_address,
-- source_id, quantity, frequency, location and geo are absent: they are the
-- immutable relationship, provenance, or legacy import data, not offering
-- content. available is absent too -- unlisting is DELETE, not an edit.
--name: update-offering
UPDATE offerings
SET commodity_name = $2,
    commodity_type = $3,
    description = $4,
    unit = $5,
    price = $6::NUMERIC,
    image_url = $7
WHERE id = $1;

-- An unlist, not a destruction. The row keeps its Sarafu source_id, timestamps
-- and provenance so a later catalog import reconciles it instead of inserting a
-- duplicate, and every public read already filters on available.
--name: delete-offering
UPDATE offerings
SET available = false
WHERE id = $1
RETURNING id;

--name: list-market-vouchers
WITH page AS MATERIALIZED (
    SELECT v.id, v.chain_id, v.address, COALESCE(v.display_name, v.name) AS name, v.symbol, v.icon_url,
        v.location_name, v.geo_lat, v.geo_lng, v.marker_icon, v.marker_color
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
    SELECT v.id, v.chain_id, v.address, COALESCE(v.display_name, v.name) AS name, v.symbol, v.icon_url,
        v.location_name, v.geo_lat, v.geo_lng, v.marker_icon, v.marker_color
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
        SELECT v.id, v.chain_id, v.address, COALESCE(v.display_name, v.name) AS name, v.symbol, v.icon_url,
            v.location_name, v.geo_lat, v.geo_lng, v.marker_icon, v.marker_color
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
    SELECT v.id, v.chain_id, v.address, COALESCE(v.display_name, v.name) AS name, v.symbol, v.icon_url,
        v.location_name, v.geo_lat, v.geo_lng, v.marker_icon, v.marker_color
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
    SELECT v.id, v.chain_id, v.address, COALESCE(v.display_name, v.name) AS name, v.symbol, v.icon_url,
        v.location_name, v.geo_lat, v.geo_lng, v.marker_icon, v.marker_color
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
        SELECT v.id, v.chain_id, v.address, COALESCE(v.display_name, v.name) AS name, v.symbol, v.icon_url,
            v.location_name, v.geo_lat, v.geo_lng, v.marker_icon, v.marker_color
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
-- terms and tags are selected here and not in the list statements. A detail read
-- is one row, so the tag subquery costs one index scan, and both fields are what
-- an operator editing this voucher has to see before they can edit it: an
-- update-voucher-profile that assigns every column needs the current values to
-- send back, or a save silently clears whichever it could not read.
SELECT v.id, v.chain_id, v.address, COALESCE(v.display_name, v.name) AS name,
    v.name AS contract_name, v.symbol, v.decimals, v.owner_address,
    v.description, v.voucher_uoa, v.voucher_value::TEXT AS voucher_value,
    v.location_name, v.geo_lat, v.geo_lng,
    v.email, v.website, v.phone, v.terms, v.icon_url, v.banner_url,
    v.marker_icon, v.marker_color,
    COALESCE((
        SELECT array_agg(vt.tag_value ORDER BY vt.tag_value)
        FROM voucher_tags vt
        WHERE vt.voucher_id = v.id
    ), ARRAY[]::TEXT[]) AS tags,
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

-- The console's read of one voucher. Identical projection to
-- get-market-voucher-by-address plus `active`, and deliberately without the
-- `v.active` predicate: a retired voucher is invisible to every public
-- statement, so without this an operator could retire one and then have no
-- screen left to restore it from. Mounted under /api/v2/admin, where
-- requireSuperAdmin has already decided the caller may see it.
--name: admin-get-voucher-by-address
SELECT v.id, v.chain_id, v.address, COALESCE(v.display_name, v.name) AS name,
    v.name AS contract_name, v.symbol, v.decimals, v.owner_address,
    v.description, v.voucher_uoa, v.voucher_value::TEXT AS voucher_value,
    v.location_name, v.geo_lat, v.geo_lng,
    v.email, v.website, v.phone, v.terms, v.icon_url, v.banner_url, v.active,
    v.marker_icon, v.marker_color,
    COALESCE((
        SELECT array_agg(vt.tag_value ORDER BY vt.tag_value)
        FROM voucher_tags vt
        WHERE vt.voucher_id = v.id
    ), ARRAY[]::TEXT[]) AS tags,
    CASE dominant.commodity_type
        WHEN 'GOOD' THEN 'goods'
        WHEN 'SERVICE' THEN 'services'
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
WHERE lower(v.address) = lower($1);

-- Report feed. Ordered by updated_at because that is the timestamp the feed
-- shows; on the imported catalog it bears almost no relation to id order.
--
-- `body` is still not selected here. Since migration 010 it is markdown TEXT
-- totalling 104 KB across every row, so size is no longer the reason — a feed
-- row is a summary and `description` already carries what it renders. Only 310
-- of 4,632 reports have any body prose at all.
--
-- image_url is gated on http(s): six approved rows store a base64 `data:` URI
-- up to 876 KB, and returning one would put it straight into a feed page. The
-- test reads `left(image_url, 8)` rather than the whole value so a data URI is
-- rejected from a sliced detoast — matching on the full column costs 1.2 ms on
-- those rows against 0.19 ms, and the two agree on every row.
--name: list-reports
SELECT r.id, r.title, r.description,
    CASE WHEN left(r.image_url, 8) ~* '^https?://' THEN r.image_url END AS image_url,
    r.geo_lat, r.geo_lng, r.period_from, r.period_to, r.author_address,
    r.created_at, r.updated_at,
    COALESCE((
        SELECT array_agg(rt.tag_value ORDER BY rt.tag_value)
        FROM report_tags rt
        WHERE rt.report_id = r.id
    ), ARRAY[]::TEXT[]) AS tags,
    COALESCE((
        SELECT jsonb_agg(jsonb_build_object(
            'id', v.id, 'chainId', v.chain_id, 'address', v.address,
            'name', COALESCE(v.display_name, v.name), 'symbol', v.symbol
        ) ORDER BY v.id)
        FROM report_vouchers rv
        INNER JOIN vouchers v ON v.id = rv.voucher_id
        WHERE rv.report_id = r.id
    ), '[]'::JSONB) AS vouchers
FROM reports r
WHERE r.status = 'APPROVED'
ORDER BY r.updated_at DESC, r.id DESC
LIMIT $1;

--name: list-reports-next
SELECT r.id, r.title, r.description,
    CASE WHEN left(r.image_url, 8) ~* '^https?://' THEN r.image_url END AS image_url,
    r.geo_lat, r.geo_lng, r.period_from, r.period_to, r.author_address,
    r.created_at, r.updated_at,
    COALESCE((
        SELECT array_agg(rt.tag_value ORDER BY rt.tag_value)
        FROM report_tags rt
        WHERE rt.report_id = r.id
    ), ARRAY[]::TEXT[]) AS tags,
    COALESCE((
        SELECT jsonb_agg(jsonb_build_object(
            'id', v.id, 'chainId', v.chain_id, 'address', v.address,
            'name', COALESCE(v.display_name, v.name), 'symbol', v.symbol
        ) ORDER BY v.id)
        FROM report_vouchers rv
        INNER JOIN vouchers v ON v.id = rv.voucher_id
        WHERE rv.report_id = r.id
    ), '[]'::JSONB) AS vouchers
FROM reports r
WHERE r.status = 'APPROVED'
    AND (r.updated_at, r.id) < ($1, $2)
ORDER BY r.updated_at DESC, r.id DESC
LIMIT $3;

-- One report, with everything the detail screen renders.
--
-- Since migration 010 nothing is parsed at read time. The images, the
-- attachments and the field-report form were extracted from the editor
-- document once, at write time, into `report_images`, `report_attachments` and
-- three columns on `reports`; `body` is the prose as markdown. The previous
-- version of this query ran two independent `jsonb_array_elements(r.body)`
-- passes per request and detoasted up to 3.95 MB to return a few hundred bytes.
--
-- Only http(s) media reached those tables (a CHECK enforces it), so the
-- base64-data-URI exclusion that used to live here is now a storage invariant
-- rather than a filter every request has to re-apply. image_url is the one
-- exception: six approved rows still hold a data URI inline, and nulling them
-- would leave four reports with no image at all, so they are gated here.
--name: get-report
SELECT r.id, r.title, r.body, r.description,
    CASE WHEN left(r.image_url, 8) ~* '^https?://' THEN r.image_url END AS image_url,
    r.geo_lat, r.geo_lng, r.period_from, r.period_to, r.author_address,
    r.status, r.created_at, r.updated_at,
    r.host_name, r.participants, r.time_taken,
    COALESCE((
        SELECT jsonb_agg(jsonb_build_object(
            'url', ri.url,
            'width', ri.width, 'height', ri.height,
            'maxWidth', ri.max_width, 'maxHeight', ri.max_height,
            'byteSize', ri.byte_size, 'caption', ri.caption
        ) ORDER BY ri.ordinal)
        FROM report_images ri
        WHERE ri.report_id = r.id
    ), '[]'::JSONB) AS images,
    COALESCE((
        SELECT jsonb_agg(jsonb_build_object(
            'kind', ra.kind, 'url', ra.url,
            'filename', ra.filename, 'byteSize', ra.byte_size
        ) ORDER BY ra.ordinal)
        FROM report_attachments ra
        WHERE ra.report_id = r.id
    ), '[]'::JSONB) AS attachments,
    COALESCE((
        SELECT array_agg(rt.tag_value ORDER BY rt.tag_value)
        FROM report_tags rt
        WHERE rt.report_id = r.id
    ), ARRAY[]::TEXT[]) AS tags,
    COALESCE((
        SELECT jsonb_agg(jsonb_build_object(
            'id', v.id, 'chainId', v.chain_id, 'address', v.address,
            'name', COALESCE(v.display_name, v.name), 'symbol', v.symbol
        ) ORDER BY v.id)
        FROM report_vouchers rv
        INNER JOIN vouchers v ON v.id = rv.voucher_id
        WHERE rv.report_id = r.id
    ), '[]'::JSONB) AS vouchers
FROM reports r
WHERE r.id = $1 AND ($2 OR r.status = 'APPROVED');

-- One post in any status, for its own author or for an operator.
--
-- A second statement rather than another boolean on get-report: that route's
-- approved-only representation is cached as the public catalog, and widening it
-- on a caller property would give one cache identity two meanings. This one is
-- served no-store.
--
-- $2 is the author constraint — NULL for a superadmin read, an address for
-- /users/me/reports/:id. A row owned by someone else returns nothing, which the
-- handler answers as 404 rather than 403.
--
-- The projection must stay byte-identical to get-report's: an editor built on
-- this read sends every field back on an assign-all PUT, so a column missing
-- here is a field the editor erases.
-- TestManagedReportDetailProjectionMatchesPublicDetail fails if they drift.
--name: get-managed-report
SELECT r.id, r.title, r.body, r.description,
    CASE WHEN left(r.image_url, 8) ~* '^https?://' THEN r.image_url END AS image_url,
    r.geo_lat, r.geo_lng, r.period_from, r.period_to, r.author_address,
    r.status, r.created_at, r.updated_at,
    r.host_name, r.participants, r.time_taken,
    COALESCE((
        SELECT jsonb_agg(jsonb_build_object(
            'url', ri.url,
            'width', ri.width, 'height', ri.height,
            'maxWidth', ri.max_width, 'maxHeight', ri.max_height,
            'byteSize', ri.byte_size, 'caption', ri.caption
        ) ORDER BY ri.ordinal)
        FROM report_images ri
        WHERE ri.report_id = r.id
    ), '[]'::JSONB) AS images,
    COALESCE((
        SELECT jsonb_agg(jsonb_build_object(
            'kind', ra.kind, 'url', ra.url,
            'filename', ra.filename, 'byteSize', ra.byte_size
        ) ORDER BY ra.ordinal)
        FROM report_attachments ra
        WHERE ra.report_id = r.id
    ), '[]'::JSONB) AS attachments,
    COALESCE((
        SELECT array_agg(rt.tag_value ORDER BY rt.tag_value)
        FROM report_tags rt
        WHERE rt.report_id = r.id
    ), ARRAY[]::TEXT[]) AS tags,
    COALESCE((
        SELECT jsonb_agg(jsonb_build_object(
            'id', v.id, 'chainId', v.chain_id, 'address', v.address,
            'name', COALESCE(v.display_name, v.name), 'symbol', v.symbol
        ) ORDER BY v.id)
        FROM report_vouchers rv
        INNER JOIN vouchers v ON v.id = rv.voucher_id
        WHERE rv.report_id = r.id
    ), '[]'::JSONB) AS vouchers
FROM reports r
WHERE r.id = $1
    AND ($2::TEXT IS NULL OR lower(r.author_address) = lower($2::TEXT));

-- Report writes. author_address is set only by the insert and never appears in
-- the update statement: the wallet that created a post remains its original
-- author even when a superadmin later corrects it. A normal user's post starts
-- SUBMITTED; a superadmin may create it already APPROVED because that caller
-- has the same authority as the explicit approval endpoint.
--name: create-report
INSERT INTO reports (
    title, body, description, image_url, geo_lat, geo_lng,
    period_from, period_to, author_address, status,
    host_name, participants, time_taken
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING id;

-- The ownership decision and the write share one transaction. Locking the row
-- closes the gap in which a concurrent admin delete could otherwise happen
-- between checking the author and applying the edit.
--name: lock-report-author
SELECT author_address
FROM reports
WHERE id = $1
FOR UPDATE;

-- Assign-all editing. source_id, author_address and created_at are deliberately
-- absent: they are provenance, not post content.
--
-- status is a conditional write. $14 is NULL for a superadmin edit, so COALESCE
-- keeps the row's current state; an author's edit passes 'SUBMITTED' so
-- replacement content goes back through review. Assigning it here keeps the
-- authorization decision, the content write and the workflow write inside the
-- transaction already holding lock-report-author's row lock.
--name: update-report
UPDATE reports
SET title = $2,
    body = $3,
    description = $4,
    image_url = $5,
    geo_lat = $6,
    geo_lng = $7,
    period_from = $8,
    period_to = $9,
    modifier_address = $10,
    host_name = $11,
    participants = $12,
    time_taken = $13,
    status = COALESCE($14, status)
WHERE id = $1;

-- Approval is separate from content editing. Neither an author edit nor an
-- admin correction touches status, so an approved post stays approved and a
-- submitted post stays submitted until a superadmin explicitly approves it.
--name: approve-report
UPDATE reports
SET status = 'APPROVED', modifier_address = $2
WHERE id = $1
RETURNING id;

--name: delete-report
DELETE FROM reports
WHERE id = $1
RETURNING id;

-- A post write replaces its small relation sets in the same transaction as
-- the scalar row. Clear-and-insert makes omission mean empty and avoids a read
-- whose only purpose would be to calculate a diff.
--name: delete-report-tags
DELETE FROM report_tags WHERE report_id = $1;

--name: insert-report-tag
INSERT INTO report_tags (report_id, tag_value) VALUES ($1, $2);

--name: delete-report-vouchers
DELETE FROM report_vouchers WHERE report_id = $1;

--name: insert-report-voucher
INSERT INTO report_vouchers (report_id, voucher_id) VALUES ($1, $2);

--name: delete-report-images
DELETE FROM report_images WHERE report_id = $1;

--name: insert-report-image
INSERT INTO report_images (
    report_id, ordinal, url, width, height, max_width, max_height, byte_size, caption
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

--name: delete-report-attachments
DELETE FROM report_attachments WHERE report_id = $1;

--name: insert-report-attachment
INSERT INTO report_attachments (report_id, ordinal, kind, url, filename, byte_size)
VALUES ($1, $2, $3, $4, $5, $6);

-- Returns the requested tag values that are not in the token_tag vocabulary, so
-- a catalog filter can be rejected rather than silently narrowed. unnest keeps
-- the request as one array parameter, so the statement text does not vary with
-- how many tags were asked for.
--name: unknown-token-tags
SELECT requested.value
FROM unnest($1::TEXT[]) AS requested(value)
WHERE NOT EXISTS (
    SELECT 1 FROM token_tag tt WHERE tt.value = requested.value
);

-- Caps one catalog read transaction at the database. set_config with
-- is_local = true is SET LOCAL, which cannot take a bound parameter; this form
-- can, so the timeout stays a Go constant instead of interpolated SQL.
--name: set-catalog-statement-timeout
SELECT set_config('statement_timeout', $1, true);

-- Resolve stable public voucher identities to the numeric keys used by the
-- relationship junctions. The caller compares the result count with the
-- deduplicated request and rejects the whole filter if any reference is absent.
--name: resolve-voucher-refs
WITH requested AS (
    SELECT *
    FROM unnest($1::BIGINT[], $2::TEXT[]) AS ref(chain_id, address)
)
SELECT v.id
FROM requested ref
INNER JOIN vouchers v
    ON v.chain_id = ref.chain_id
    AND lower(v.address) = lower(ref.address)
ORDER BY v.id;

-- Resolve stable public pool identities to the numeric keys used by
-- pool_tokens. Every requested reference must resolve before a relationship
-- filter is applied, otherwise a typo could silently broaden discovery.
--name: resolve-pool-refs
WITH requested AS (
    SELECT *
    FROM unnest($1::BIGINT[], $2::TEXT[]) AS ref(chain_id, address)
)
SELECT p.id
FROM requested ref
INNER JOIN pools p
    ON p.chain_id = ref.chain_id
    AND lower(p.address) = lower(ref.address)
ORDER BY p.id;

-- How many of the given voucher ids belong to one address. Posts link vouchers
-- by id after resolve-voucher-refs, and a non-superadmin author may only link
-- vouchers they own; comparing the count against the number of ids is the
-- whole check. A voucher with no recorded owner is owned by nobody here.
--name: count-vouchers-owned-by
SELECT count(*)
FROM vouchers v
WHERE v.id = ANY($1::BIGINT[])
    AND lower(v.owner_address) = lower($2);

-- The voucher tag facet vocabulary: every tag that appears on an available
-- offering of a visible voucher. Deliberately independent of the caller's
-- search, so the filter list does not collapse as the user types, and of the
-- selected tags, which the handler unions in — that keeps this statement
-- parameterless and therefore a straightforward caching candidate if it ever
-- becomes hot. token_tag's other values are excluded: offering a filter that
-- can never match is worse than omitting it.
--
-- Measured at 5x scale (13,181 active vouchers, 12,975 available offerings,
-- 6,705 tag rows) this is ~6.2 ms of sequential scans over the junctions, which
-- is why it is separate from the count query rather than a CTE inside it:
-- sharing one statement made the planner abandon the trigram index for the
-- search half.
--name: list-voucher-tag-vocabulary
-- Both sources a voucher's tags come from, or the vocabulary would offer fewer
-- filters than the counts beside it report. voucher_tags is curated directly (the
-- deploy pipeline and the admin profile write); offering_tags is derived from
-- available offerings. Same visibility rule on both branches: a tag reachable
-- only through an invisible voucher offers a filter that can never match.
SELECT tag_value
FROM (
    SELECT ot.tag_value
    FROM offering_tags ot
    INNER JOIN offerings o ON o.id = ot.offering_id
    INNER JOIN vouchers v ON v.id = o.voucher_id
    WHERE o.available AND v.active AND v.geo_lat IS NOT NULL
    UNION
    SELECT vt.tag_value
    FROM voucher_tags vt
    INNER JOIN vouchers v ON v.id = vt.voucher_id
    WHERE v.active AND v.geo_lat IS NOT NULL
) vocabulary
ORDER BY tag_value;

-- Tags carried by publicly discoverable offerings. Effective location uses
-- the offering location first and falls back to its active parent voucher.
--name: list-offering-tag-vocabulary
SELECT DISTINCT ot.tag_value
FROM offering_tags ot
INNER JOIN offerings o ON o.id = ot.offering_id
INNER JOIN vouchers v ON v.id = o.voucher_id
WHERE o.available AND v.active
    AND COALESCE(o.geo_lat, v.geo_lat) IS NOT NULL
    AND COALESCE(o.geo_lng, v.geo_lng) IS NOT NULL
ORDER BY ot.tag_value;

-- The pool tag facet vocabulary: the pool's own curated tags, which is what the
-- pool list already returns per row.
--name: list-pool-tag-vocabulary
SELECT DISTINCT pt.tag_value
FROM pool_tags pt
ORDER BY pt.tag_value;

-- The report tag facet vocabulary: tags carried by reports the feed can actually
-- return, so a value reachable only through an unapproved report does not offer a
-- filter that can never match.
--name: list-report-tag-vocabulary
SELECT DISTINCT rt.tag_value
FROM report_tags rt
INNER JOIN reports r ON r.id = rt.report_id
WHERE r.status = 'APPROVED'
ORDER BY rt.tag_value;

-- Dashboard snapshot: lifetime stock plus windowed report creations.
--name: dashboard-catalog-snapshot
SELECT
    (SELECT COUNT(*) FROM pools) AS pools,
    (SELECT COUNT(*) FROM vouchers) AS vouchers,
    (SELECT COUNT(*) FROM users) AS accounts,
    (SELECT COUNT(*) FROM reports
     WHERE status = 'APPROVED' AND created_at >= $1 AND created_at < $2) AS reports;

-- Filled catalog series. generate_series builds the bucket grid so the API
-- does not materialise empty slots in process memory.
-- $1 since, $2 until, $3 bucket seconds.
--name: dashboard-catalog-series
WITH buckets AS (
    SELECT g AS bucket_epoch
    FROM generate_series(
        (floor(extract(epoch from $1::timestamptz) / $3) * $3)::bigint,
        (extract(epoch from $2::timestamptz)::bigint - 1),
        $3
    ) AS g
),
reports AS (
    SELECT (floor(extract(epoch from created_at) / $3) * $3)::bigint AS bucket_epoch,
        COUNT(*)::bigint AS count
    FROM reports
    WHERE status = 'APPROVED' AND created_at >= $1 AND created_at < $2
    GROUP BY 1
),
vouchers AS (
    SELECT (floor(extract(epoch from created_at) / $3) * $3)::bigint AS bucket_epoch,
        COUNT(*)::bigint AS count
    FROM vouchers
    WHERE created_at >= $1 AND created_at < $2
    GROUP BY 1
),
pools AS (
    SELECT (floor(extract(epoch from created_at) / $3) * $3)::bigint AS bucket_epoch,
        COUNT(*)::bigint AS count
    FROM pools
    WHERE created_at >= $1 AND created_at < $2
    GROUP BY 1
)
SELECT to_timestamp(b.bucket_epoch) AS t,
    COALESCE(r.count, 0) AS reports,
    COALESCE(v.count, 0) AS vouchers,
    COALESCE(p.count, 0) AS pools
FROM buckets b
LEFT JOIN reports r ON r.bucket_epoch = b.bucket_epoch
LEFT JOIN vouchers v ON v.bucket_epoch = b.bucket_epoch
LEFT JOIN pools p ON p.bucket_epoch = b.bucket_epoch
ORDER BY b.bucket_epoch;

--name: account-relationship-counts
-- Visibility matches the lists these figures sit beside: approved reports, and
-- vouchers the public catalog can return.
SELECT
    (SELECT COUNT(*) FROM reports r
        WHERE r.status = 'APPROVED' AND lower(r.author_address) = lower($1)) AS reports,
    (SELECT COUNT(*) FROM vouchers v
        WHERE v.active AND v.geo_lat IS NOT NULL
            AND lower(v.owner_address) = lower($1)) AS vouchers,
    (SELECT COUNT(DISTINCT pt.pool_id)
        FROM pool_tokens pt
        INNER JOIN vouchers v ON v.id = pt.voucher_id
        WHERE pt.is_allowed AND lower(v.owner_address) = lower($1)) AS pool_memberships;

-- Unsettled transactions for one signer, oldest nonce first. The reconciler
-- walks this list: receipts first, then republishes the lowest nonce the chain
-- has not consumed. Dispatch statuses excluded here are the settled ones — see
-- store.IsTerminalDispatchStatus.
--name: get-unsettled-otx-by-signer
SELECT otx.id, otx.tracking_id, otx.otx_type, otx.signer_account AS public_key, otx.raw_tx, otx.tx_hash, otx.nonce, otx.replaced, otx.created_at, otx.updated_at, dispatch.status
FROM otx
INNER JOIN dispatch ON otx.id = dispatch.otx_id
WHERE otx.signer_account = (SELECT id FROM keystore WHERE public_key = $1)
  AND otx.replaced = false
  AND dispatch.status NOT IN ('SUCCESS', 'REVERTED', 'LOW_NONCE', 'INVALID_TX')
ORDER BY otx.nonce ASC, otx.id ASC;

-- Mark an OTX superseded by a replacement carrying the same nonce. The column
-- has existed since migration 001 and was never written until the fee bumper
-- landed; nothing enforces uniqueness on (signer_account, nonce) precisely so
-- replacement rows are legal.
--name: set-otx-replaced
UPDATE otx SET replaced = true WHERE id = $1;

-- Serialises everything that touches one signer's nonce sequence: the
-- dispatcher, the fee bumper and the reconciler. Transaction-scoped, so it is
-- released on commit or rollback. Two goroutines signing a pair of
-- transactions in the same millisecond is how they got published out of nonce
-- order in the first place.
--name: lock-signer
SELECT pg_advisory_xact_lock(
    (SELECT id FROM keystore WHERE public_key = $1)::BIGINT
);

-- Highest nonce this signer has an unreplaced OTX for, and whether every
-- lower nonce has left PENDING. Used by the dispatcher to hold a transaction
-- back until its predecessors are in the pool.
--name: count-undispatched-below-nonce
SELECT count(*)
FROM otx
INNER JOIN dispatch ON otx.id = dispatch.otx_id
WHERE otx.signer_account = (SELECT id FROM keystore WHERE public_key = $1)
  AND otx.replaced = false
  AND otx.nonce < $2
  AND dispatch.status = 'PENDING';

--name: create-curated-catalog
INSERT INTO catalogs(owner_user_id, name, description, image_url, location_name, geo_lat, geo_lng)
VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id;

--name: update-curated-catalog
UPDATE catalogs SET name=$2, description=$3, image_url=$4, location_name=$5, geo_lat=$6, geo_lng=$7 WHERE id=$1;

--name: lock-curated-catalog
SELECT owner_user_id FROM catalogs WHERE id=$1 FOR UPDATE;

-- Counts and member reads share public-detail visibility: all pools and active
-- vouchers. Coordinates are not required for explicit curation or detail.
--name: get-curated-catalog
SELECT c.id,c.name,c.description,c.image_url,c.location_name,c.geo_lat,c.geo_lng,
    u.blockchain_address AS owner_address,c.created_at,c.updated_at,
    (SELECT count(*) FROM catalog_pools cp JOIN pools p ON p.id=cp.pool_id WHERE cp.catalog_id=c.id) AS pool_count,
    (SELECT count(*) FROM catalog_vouchers cv JOIN vouchers v ON v.id=cv.voucher_id WHERE cv.catalog_id=c.id AND v.active) AS voucher_count,
    NULL::float8 AS distance_meters,
    COALESCE((SELECT array_agg(ct.tag_value ORDER BY ct.tag_value) FROM catalog_tags ct WHERE ct.catalog_id=c.id), ARRAY[]::TEXT[]) AS tags
FROM catalogs c JOIN users u ON u.id=c.owner_user_id WHERE c.id=$1;

--name: list-curated-catalogs-global
WITH candidates AS MATERIALIZED (
    SELECT c.id, NULL::float8 AS distance_meters
    FROM catalogs c
    WHERE ($1::BIGINT=0 OR c.owner_user_id=$1)
      AND ($2='' OR CASE WHEN $3 THEN (
            lower(c.name) LIKE lower($2)||'%' ESCAPE '\'
         OR lower(c.location_name) LIKE lower($2)||'%' ESCAPE '\'
         OR lower(COALESCE(c.description,'')) LIKE lower($2)||'%' ESCAPE '\'
        ) ELSE (
            lower(c.name) LIKE '%'||lower($2)||'%' ESCAPE '\'
         OR lower(c.location_name) LIKE '%'||lower($2)||'%' ESCAPE '\'
         OR lower(COALESCE(c.description,'')) LIKE '%'||lower($2)||'%' ESCAPE '\'
        ) END)
      AND (cardinality($4::TEXT[])=0 OR EXISTS (
          SELECT 1 FROM catalog_tags ct WHERE ct.catalog_id=c.id AND ct.tag_value=ANY($4::TEXT[])
          GROUP BY ct.catalog_id
          HAVING NOT $5::BOOLEAN OR count(DISTINCT ct.tag_value)=cardinality($4::TEXT[])
      ))
      AND (cardinality($6::BIGINT[])=0 OR EXISTS (
          SELECT 1 FROM catalog_pools cp WHERE cp.catalog_id=c.id AND cp.pool_id=ANY($6::BIGINT[])
          UNION ALL
          SELECT 1 FROM catalog_vouchers cv
          JOIN pool_tokens pt ON pt.voucher_id=cv.voucher_id AND pt.is_allowed
          WHERE cv.catalog_id=c.id AND pt.pool_id=ANY($6::BIGINT[])
      ))
), page AS MATERIALIZED (
    SELECT id,distance_meters FROM candidates
    WHERE $7::BIGINT=0 OR id<$7 ORDER BY id DESC LIMIT $8
)
SELECT c.id,c.name,c.description,c.image_url,c.location_name,c.geo_lat,c.geo_lng,
    u.blockchain_address AS owner_address,c.created_at,c.updated_at,
    (SELECT count(*) FROM catalog_pools cp JOIN pools p ON p.id=cp.pool_id WHERE cp.catalog_id=c.id) AS pool_count,
    (SELECT count(*) FROM catalog_vouchers cv JOIN vouchers v ON v.id=cv.voucher_id WHERE cv.catalog_id=c.id AND v.active) AS voucher_count,
    page.distance_meters,
    COALESCE((SELECT array_agg(ct.tag_value ORDER BY ct.tag_value) FROM catalog_tags ct WHERE ct.catalog_id=c.id), ARRAY[]::TEXT[]) AS tags
FROM page JOIN catalogs c ON c.id=page.id JOIN users u ON u.id=c.owner_user_id
ORDER BY page.id DESC;

--name: list-curated-catalogs-nearby
WITH candidates AS MATERIALIZED (
    SELECT c.id,
        2 * 6371008.8 * asin(sqrt(least(1.0,greatest(0.0,
            power(sin(radians(c.geo_lat-$7::float8)/2),2)+
            cos(radians($7::float8))*cos(radians(c.geo_lat))*
            power(sin(radians(c.geo_lng-$8::float8)/2),2)
        )))) AS distance_meters
    FROM catalogs c
    WHERE ($1::BIGINT=0 OR c.owner_user_id=$1)
      AND point(c.geo_lng,c.geo_lat) <@ box(point($9,$10),point($11,$12))
      AND CASE WHEN c.geo_lat BETWEEN -90 AND 90 AND c.geo_lng BETWEEN -180 AND 180
          THEN 2 * 6371008.8 * asin(sqrt(least(1.0,greatest(0.0,
              power(sin(radians(c.geo_lat-$7::float8)/2),2)+
              cos(radians($7::float8))*cos(radians(c.geo_lat))*
              power(sin(radians(c.geo_lng-$8::float8)/2),2)
          )))) <= 1000000 ELSE false END
      AND ($2='' OR CASE WHEN $3 THEN (
            lower(c.name) LIKE lower($2)||'%' ESCAPE '\'
         OR lower(c.location_name) LIKE lower($2)||'%' ESCAPE '\'
         OR lower(COALESCE(c.description,'')) LIKE lower($2)||'%' ESCAPE '\'
        ) ELSE (
            lower(c.name) LIKE '%'||lower($2)||'%' ESCAPE '\'
         OR lower(c.location_name) LIKE '%'||lower($2)||'%' ESCAPE '\'
         OR lower(COALESCE(c.description,'')) LIKE '%'||lower($2)||'%' ESCAPE '\'
        ) END)
      AND (cardinality($4::TEXT[])=0 OR EXISTS (
          SELECT 1 FROM catalog_tags ct WHERE ct.catalog_id=c.id AND ct.tag_value=ANY($4::TEXT[])
          GROUP BY ct.catalog_id
          HAVING NOT $5::BOOLEAN OR count(DISTINCT ct.tag_value)=cardinality($4::TEXT[])
      ))
      AND (cardinality($6::BIGINT[])=0 OR EXISTS (
          SELECT 1 FROM catalog_pools cp WHERE cp.catalog_id=c.id AND cp.pool_id=ANY($6::BIGINT[])
          UNION ALL
          SELECT 1 FROM catalog_vouchers cv
          JOIN pool_tokens pt ON pt.voucher_id=cv.voucher_id AND pt.is_allowed
          WHERE cv.catalog_id=c.id AND pt.pool_id=ANY($6::BIGINT[])
      ))
), page AS MATERIALIZED (
    SELECT id,distance_meters FROM candidates
    WHERE $14::BIGINT=0 OR (distance_meters,id)>($13::float8,$14)
    ORDER BY distance_meters ASC,id ASC LIMIT $15
)
SELECT c.id,c.name,c.description,c.image_url,c.location_name,c.geo_lat,c.geo_lng,
    u.blockchain_address AS owner_address,c.created_at,c.updated_at,
    (SELECT count(*) FROM catalog_pools cp JOIN pools p ON p.id=cp.pool_id WHERE cp.catalog_id=c.id) AS pool_count,
    (SELECT count(*) FROM catalog_vouchers cv JOIN vouchers v ON v.id=cv.voucher_id WHERE cv.catalog_id=c.id AND v.active) AS voucher_count,
    page.distance_meters,
    COALESCE((SELECT array_agg(ct.tag_value ORDER BY ct.tag_value) FROM catalog_tags ct WHERE ct.catalog_id=c.id), ARRAY[]::TEXT[]) AS tags
FROM page JOIN catalogs c ON c.id=page.id JOIN users u ON u.id=c.owner_user_id
ORDER BY page.distance_meters ASC,page.id ASC;

--name: delete-curated-catalog-tags
DELETE FROM catalog_tags WHERE catalog_id=$1;

--name: insert-curated-catalog-tag
INSERT INTO catalog_tags(catalog_id,tag_value) VALUES($1,$2);

-- Facets deliberately omit the selected-tag reducer while retaining search,
-- ownership, relationship and location predicates.
--name: list-curated-catalog-tag-facets-global
WITH eligible AS (
    SELECT c.id FROM catalogs c
    WHERE ($1::BIGINT=0 OR c.owner_user_id=$1)
      AND ($2='' OR CASE WHEN $3 THEN (
            lower(c.name) LIKE lower($2)||'%' ESCAPE '\'
         OR lower(c.location_name) LIKE lower($2)||'%' ESCAPE '\'
         OR lower(COALESCE(c.description,'')) LIKE lower($2)||'%' ESCAPE '\'
        ) ELSE (
            lower(c.name) LIKE '%'||lower($2)||'%' ESCAPE '\'
         OR lower(c.location_name) LIKE '%'||lower($2)||'%' ESCAPE '\'
         OR lower(COALESCE(c.description,'')) LIKE '%'||lower($2)||'%' ESCAPE '\'
        ) END)
      AND (cardinality($4::BIGINT[])=0 OR EXISTS (
          SELECT 1 FROM catalog_pools cp WHERE cp.catalog_id=c.id AND cp.pool_id=ANY($4::BIGINT[])
          UNION ALL
          SELECT 1 FROM catalog_vouchers cv JOIN pool_tokens pt ON pt.voucher_id=cv.voucher_id AND pt.is_allowed
          WHERE cv.catalog_id=c.id AND pt.pool_id=ANY($4::BIGINT[])
      ))
)
SELECT tt.value AS tag_value,count(ct.catalog_id)::BIGINT AS match_count
FROM token_tag tt LEFT JOIN catalog_tags ct ON ct.tag_value=tt.value AND ct.catalog_id IN (SELECT id FROM eligible)
GROUP BY tt.value ORDER BY tt.value;

--name: list-curated-catalog-tag-facets-nearby
WITH eligible AS (
    SELECT c.id FROM catalogs c
    WHERE ($1::BIGINT=0 OR c.owner_user_id=$1)
      AND ($2='' OR CASE WHEN $3 THEN (
            lower(c.name) LIKE lower($2)||'%' ESCAPE '\'
         OR lower(c.location_name) LIKE lower($2)||'%' ESCAPE '\'
         OR lower(COALESCE(c.description,'')) LIKE lower($2)||'%' ESCAPE '\'
        ) ELSE (
            lower(c.name) LIKE '%'||lower($2)||'%' ESCAPE '\'
         OR lower(c.location_name) LIKE '%'||lower($2)||'%' ESCAPE '\'
         OR lower(COALESCE(c.description,'')) LIKE '%'||lower($2)||'%' ESCAPE '\'
        ) END)
      AND (cardinality($4::BIGINT[])=0 OR EXISTS (
          SELECT 1 FROM catalog_pools cp WHERE cp.catalog_id=c.id AND cp.pool_id=ANY($4::BIGINT[])
          UNION ALL
          SELECT 1 FROM catalog_vouchers cv JOIN pool_tokens pt ON pt.voucher_id=cv.voucher_id AND pt.is_allowed
          WHERE cv.catalog_id=c.id AND pt.pool_id=ANY($4::BIGINT[])
      ))
      AND point(c.geo_lng,c.geo_lat) <@ box(point($7,$8),point($9,$10))
      AND 2 * 6371008.8 * asin(sqrt(least(1.0,greatest(0.0,
          power(sin(radians(c.geo_lat-$5::float8)/2),2)+cos(radians($5::float8))*cos(radians(c.geo_lat))*
          power(sin(radians(c.geo_lng-$6::float8)/2),2)
      )))) <= 1000000
)
SELECT tt.value AS tag_value,count(ct.catalog_id)::BIGINT AS match_count
FROM token_tag tt LEFT JOIN catalog_tags ct ON ct.tag_value=tt.value AND ct.catalog_id IN (SELECT id FROM eligible)
GROUP BY tt.value ORDER BY tt.value;

--name: add-curated-pool
INSERT INTO catalog_pools(catalog_id,pool_id)
SELECT $1,id FROM pools WHERE id=$2 ON CONFLICT DO NOTHING;

--name: add-curated-voucher
INSERT INTO catalog_vouchers(catalog_id,voucher_id)
SELECT $1,id FROM vouchers WHERE id=$2 AND active ON CONFLICT DO NOTHING;

--name: remove-curated-pool
DELETE FROM catalog_pools WHERE catalog_id=$1 AND pool_id=$2;

--name: remove-curated-voucher
DELETE FROM catalog_vouchers WHERE catalog_id=$1 AND voucher_id=$2;

--name: curated-pool-membership
SELECT EXISTS(SELECT 1 FROM catalog_pools WHERE catalog_id=$1 AND pool_id=$2);

--name: curated-voucher-membership
SELECT EXISTS(SELECT 1 FROM catalog_vouchers WHERE catalog_id=$1 AND voucher_id=$2);

--name: visible-curated-pool
SELECT id FROM pools WHERE id=$1 FOR SHARE;

--name: visible-curated-voucher
SELECT id FROM vouchers WHERE id=$1 AND active FOR SHARE;

--name: list-curated-pools
SELECT p.id,p.chain_id,p.address,COALESCE(p.display_name,p.name,p.address) AS name,p.image_url
FROM catalog_pools cp JOIN pools p ON p.id=cp.pool_id WHERE cp.catalog_id=$1 AND p.id>$2 ORDER BY p.id LIMIT $3;

--name: list-curated-vouchers
SELECT v.id,v.chain_id,v.address,COALESCE(v.display_name,v.name,v.address) AS name,v.icon_url AS image_url
FROM catalog_vouchers cv JOIN vouchers v ON v.id=cv.voucher_id WHERE cv.catalog_id=$1 AND v.active AND v.id>$2 ORDER BY v.id LIMIT $3;

-- Unified public marketplace discovery. Request data crosses only as bound
-- values; PostgreSQL owns relationship matching, ranking, distance and keyset
-- pagination so a mixed page is complete rather than four client-side slices.
--name: list-marketplace
WITH raw AS MATERIALIZED (
    SELECT 'offering'::TEXT AS kind,o.id::BIGINT AS id,v.chain_id::BIGINT AS chain_id,
        v.address, o.commodity_name AS name, NULLIF(o.description,'') AS description,
        v.symbol,COALESCE(o.image_url,v.icon_url) AS image_url,
        NULL::TEXT AS marker_icon,NULL::TEXT AS marker_color,NULL::TEXT AS voucher_kind,
        COALESCE(o.location_name,v.location_name) AS location_name,
        COALESCE(o.geo_lat,v.geo_lat)::FLOAT8 AS geo_lat,
        COALESCE(o.geo_lng,v.geo_lng)::FLOAT8 AS geo_lng,
        COALESCE(ot.tags,ARRAY[]::TEXT[]) AS tags,
        v.address AS voucher_address,COALESCE(v.display_name,v.name,v.address) AS voucher_name,
        v.symbol AS voucher_symbol,v.voucher_uoa,
        CASE WHEN o.price>0 AND NULLIF(BTRIM(o.unit),'') IS NOT NULL
                  AND v.voucher_value>0 AND NULLIF(BTRIM(v.voucher_uoa),'') IS NOT NULL
             THEN (o.price*v.voucher_value)::TEXT END AS price_in_unit_of_account,
        o.unit,NULL::BIGINT AS pool_count,NULL::BIGINT AS voucher_count,
        o.created_at,o.updated_at,
        lower(o.commodity_name) AS direct_name,
        lower(concat_ws(' ',o.commodity_name,o.description,array_to_string(ot.tags,' '),
            COALESCE(v.display_name,v.name),v.symbol)) AS search_text
    FROM offerings o JOIN vouchers v ON v.id=o.voucher_id
    LEFT JOIN LATERAL (
        SELECT array_agg(x.tag_value ORDER BY x.tag_value) AS tags
        FROM offering_tags x WHERE x.offering_id=o.id
    ) ot ON true
    WHERE o.available AND v.active
      AND COALESCE(o.geo_lat,v.geo_lat) IS NOT NULL AND COALESCE(o.geo_lng,v.geo_lng) IS NOT NULL
      AND (cardinality($5::BIGINT[])=0 OR EXISTS (
          SELECT 1 FROM pool_tokens pt WHERE pt.voucher_id=v.id AND pt.is_allowed AND pt.pool_id=ANY($5::BIGINT[])
      ))

    UNION ALL
    SELECT 'voucher',v.id::BIGINT,v.chain_id::BIGINT,v.address,
        COALESCE(v.display_name,v.name,v.address),v.description,v.symbol,v.icon_url,
        v.marker_icon,v.marker_color,
        COALESCE((
            SELECT CASE o3.commodity_type WHEN 'GOOD' THEN 'goods' WHEN 'SERVICE' THEN 'services' END
            FROM offerings o3 WHERE o3.voucher_id=v.id AND o3.available
            GROUP BY o3.commodity_type ORDER BY count(*) DESC,o3.commodity_type ASC LIMIT 1
        ),'voucher') AS voucher_kind,
        v.location_name,v.geo_lat::FLOAT8,v.geo_lng::FLOAT8,
        COALESCE(vt.tags,ARRAY[]::TEXT[]),
        NULL,NULL,NULL,NULL,NULL,NULL,NULL::BIGINT,NULL::BIGINT,
        v.created_at,v.updated_at,
        lower(COALESCE(v.display_name,v.name,v.address)),
        lower(concat_ws(' ',COALESCE(v.display_name,v.name),v.symbol,v.description,
            array_to_string(vt.tags,' '),related.text))
    FROM vouchers v
    LEFT JOIN LATERAL (
        SELECT array_agg(tag ORDER BY tag) AS tags FROM (
            SELECT x.tag_value AS tag FROM voucher_tags x WHERE x.voucher_id=v.id
            UNION
            SELECT x.tag_value FROM offerings o2 JOIN offering_tags x ON x.offering_id=o2.id
            WHERE o2.voucher_id=v.id AND o2.available
        ) valueset
    ) vt ON true
    LEFT JOIN LATERAL (
        SELECT string_agg(concat_ws(' ',o2.commodity_name,o2.description),' ') AS text
        FROM offerings o2 WHERE o2.voucher_id=v.id AND o2.available
    ) related ON true
    WHERE v.active AND v.geo_lat IS NOT NULL AND v.geo_lng IS NOT NULL
      AND (cardinality($5::BIGINT[])=0 OR EXISTS (
          SELECT 1 FROM pool_tokens pt WHERE pt.voucher_id=v.id AND pt.is_allowed AND pt.pool_id=ANY($5::BIGINT[])
      ))

    UNION ALL
    SELECT 'pool',p.id::BIGINT,p.chain_id::BIGINT,p.address,
        COALESCE(p.display_name,p.name,p.address),p.description,p.symbol,p.image_url,
        NULL::TEXT,NULL::TEXT,NULL::TEXT,
        p.location_name,p.geo_lat::FLOAT8,p.geo_lng::FLOAT8,
        COALESCE(ptags.tags,ARRAY[]::TEXT[]),
        NULL,NULL,NULL,NULL,NULL,NULL,NULL::BIGINT,
        (SELECT count(*) FROM pool_tokens x JOIN vouchers v ON v.id=x.voucher_id WHERE x.pool_id=p.id AND x.is_allowed AND v.active)::BIGINT,
        p.created_at,p.updated_at,
        lower(COALESCE(p.display_name,p.name,p.address)),
        lower(concat_ws(' ',COALESCE(p.display_name,p.name),p.symbol,p.description,
            array_to_string(ptags.tags,' '),related.text))
    FROM pools p
    LEFT JOIN LATERAL (
        SELECT array_agg(x.tag_value ORDER BY x.tag_value) AS tags FROM pool_tags x WHERE x.pool_id=p.id
    ) ptags ON true
    LEFT JOIN LATERAL (
        SELECT string_agg(concat_ws(' ',COALESCE(v.display_name,v.name),v.symbol,o.commodity_name,o.description),' ') AS text
        FROM pool_tokens x JOIN vouchers v ON v.id=x.voucher_id AND v.active
        LEFT JOIN offerings o ON o.voucher_id=v.id AND o.available
        WHERE x.pool_id=p.id AND x.is_allowed
    ) related ON true
    WHERE p.geo_lat IS NOT NULL AND p.geo_lng IS NOT NULL
      AND (cardinality($5::BIGINT[])=0 OR p.id=ANY($5::BIGINT[]))

    UNION ALL
    SELECT 'catalog',c.id::BIGINT,NULL::BIGINT,NULL::TEXT,c.name,c.description,NULL::TEXT,c.image_url,
        NULL::TEXT,NULL::TEXT,NULL::TEXT,
        c.location_name,c.geo_lat::FLOAT8,c.geo_lng::FLOAT8,
        COALESCE(ctags.tags,ARRAY[]::TEXT[]),
        NULL,NULL,NULL,NULL,NULL,NULL,
        (SELECT count(*) FROM catalog_pools cp WHERE cp.catalog_id=c.id)::BIGINT,
        (SELECT count(*) FROM catalog_vouchers cv JOIN vouchers v ON v.id=cv.voucher_id WHERE cv.catalog_id=c.id AND v.active)::BIGINT,
        c.created_at,c.updated_at,lower(c.name),
        lower(concat_ws(' ',c.name,c.description,c.location_name,array_to_string(ctags.tags,' ')))
    FROM catalogs c
    LEFT JOIN LATERAL (
        SELECT array_agg(x.tag_value ORDER BY x.tag_value) AS tags FROM catalog_tags x WHERE x.catalog_id=c.id
    ) ctags ON true
    WHERE cardinality($5::BIGINT[])=0 OR EXISTS (
        SELECT 1 FROM catalog_pools cp WHERE cp.catalog_id=c.id AND cp.pool_id=ANY($5::BIGINT[])
        UNION ALL
        SELECT 1 FROM catalog_vouchers cv JOIN pool_tokens x ON x.voucher_id=cv.voucher_id AND x.is_allowed
        WHERE cv.catalog_id=c.id AND x.pool_id=ANY($5::BIGINT[])
    )
), scored AS MATERIALIZED (
    SELECT raw.*,
        CASE WHEN $1='' THEN 0
             WHEN direct_name=lower($1) OR lower(COALESCE(symbol,''))=lower($1) THEN 0
             WHEN direct_name LIKE lower($1)||'%' ESCAPE '\' THEN 1
             WHEN EXISTS (SELECT 1 FROM unnest(tags) tag WHERE lower(tag)=lower($1)) THEN 2
             WHEN direct_name LIKE '%'||lower($1)||'%' ESCAPE '\' THEN 3
             ELSE 4 END AS rank,
        CASE WHEN $7::BOOLEAN THEN
            2 * 6371008.8 * asin(sqrt(least(1.0,greatest(0.0,
                power(sin(radians(geo_lat-$8::FLOAT8)/2),2)+
                cos(radians($8::FLOAT8))*cos(radians(geo_lat))*
                power(sin(radians(geo_lng-$9::FLOAT8)/2),2)
            )))) END AS distance_meters
    FROM raw
    WHERE (cardinality($6::TEXT[])=0 OR kind=ANY($6::TEXT[]))
      AND ($1='' OR CASE WHEN $2::BOOLEAN
            THEN search_text LIKE lower($1)||'%' ESCAPE '\'
            ELSE search_text LIKE '%'||lower($1)||'%' ESCAPE '\' END)
      AND (cardinality($3::TEXT[])=0 OR (
          CASE WHEN $4::BOOLEAN THEN
              (SELECT count(DISTINCT tag) FROM unnest(tags) tag WHERE tag=ANY($3::TEXT[]))=cardinality($3::TEXT[])
          ELSE tags && $3::TEXT[] END
      ))
      AND (NOT $7::BOOLEAN OR (
          point(geo_lng,geo_lat) <@ box(point($10,$11),point($12,$13))
          AND 2 * 6371008.8 * asin(sqrt(least(1.0,greatest(0.0,
              power(sin(radians(geo_lat-$8::FLOAT8)/2),2)+
              cos(radians($8::FLOAT8))*cos(radians(geo_lat))*
              power(sin(radians(geo_lng-$9::FLOAT8)/2),2)
          )))) <= 1000000
      ))
), facet_counts AS (
    SELECT tag,count(DISTINCT kind||':'||id::TEXT)::BIGINT AS count
    FROM scored CROSS JOIN LATERAL unnest(tags) tag
    GROUP BY tag
), page AS (
    SELECT * FROM scored
    WHERE $18::BIGINT=0 OR (
        rank>$14::INT OR (rank=$14::INT AND (
            ($7::BOOLEAN AND (distance_meters>$15::FLOAT8 OR (distance_meters=$15::FLOAT8 AND (
                updated_at<$16::TIMESTAMPTZ OR (updated_at=$16::TIMESTAMPTZ AND
                    (kind>$17::TEXT OR (kind=$17::TEXT AND id<$18::BIGINT)))
            )))) OR
            (NOT $7::BOOLEAN AND (updated_at<$16::TIMESTAMPTZ OR (updated_at=$16::TIMESTAMPTZ AND
                (kind>$17::TEXT OR (kind=$17::TEXT AND id<$18::BIGINT)))))
        ))
    )
    ORDER BY rank ASC,
        CASE WHEN $7::BOOLEAN THEN distance_meters END ASC NULLS LAST,
        updated_at DESC,kind ASC,id DESC
    LIMIT $19
)
SELECT kind,id,chain_id,address,name,description,symbol,image_url,marker_icon,marker_color,voucher_kind,location_name,geo_lat,geo_lng,
    distance_meters,tags,voucher_address,voucher_name,voucher_symbol,voucher_uoa,
    price_in_unit_of_account,unit,pool_count,voucher_count,rank,created_at,updated_at,
    CASE WHEN $20::BOOLEAN THEN COALESCE((
        SELECT jsonb_agg(jsonb_build_object('tag',tag,'count',count) ORDER BY tag)
        FROM facet_counts
    ),'[]'::JSONB) ELSE '[]'::JSONB END AS tag_facets
FROM page
ORDER BY rank ASC,CASE WHEN $7::BOOLEAN THEN distance_meters END ASC NULLS LAST,
    updated_at DESC,kind ASC,id DESC;
