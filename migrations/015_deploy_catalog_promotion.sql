-- Backfill the confirm-path promotion that MIGRATION.md §2.4 / §3.5 locked but
-- which landed as schema only: 006 added deployed_tokens.voucher_id and
-- deployed_pools.pool_id nullable, and nothing ever linked or created the
-- catalog rows. From this migration forward the promotion runs in the
-- confirming transaction (upsert-voucher-from-deploy / upsert-pool-from-deploy);
-- this file catches up every deploy confirmed before that existed.
--
-- Backfill merge policy is deliberately the *opposite* of the confirm path. A
-- live confirm carries a fresh request and wins field by field; a backfill is
-- reconstructing history, so an existing catalog row — which may hold curated
-- or imported values the deploy snapshot never knew — wins, and the deploy only
-- fills columns that are still NULL. This closes the STAGING_TRANSFER.md gap
-- ("staging's 5 deployed tokens are not in the voucher catalog") by adding those
-- rows, without regressing the 2 pools that already match catalog entries.
--
-- Only rows with a NULL link are touched, so the migration is idempotent and
-- never disturbs a deploy the confirm path has already promoted.
--
-- No 001 baseline fold, unlike the usual convention: this migration adds no
-- schema, only data. 006 already carries both link columns into the baseline,
-- and a fresh install has no confirmed deploys to promote, so the fresh and
-- upgraded paths agree without folding anything.

-- Vouchers: create the missing catalog rows, gap-fill the ones already there.
INSERT INTO vouchers (
    chain_id, address, name, symbol, decimals, owner_address, voucher_type,
    description, voucher_uoa, voucher_value, location_name, geo_lat, geo_lng,
    email, website, phone, contract_version, platform_contract_id
)
SELECT
    pc.chain_id, dt.address, dt.name, dt.symbol, dt.decimals, dt.admin,
    'GIFTABLE',
    dt.description, dt.voucher_uoa, dt.voucher_value, dt.location_name,
    dt.geo_lat, dt.geo_lng, dt.email, dt.website, dt.phone,
    NULLIF(pc.version, ''), dt.platform_contract_id
FROM deployed_tokens dt
INNER JOIN platform_contracts pc ON pc.id = dt.platform_contract_id
WHERE dt.status = 'CONFIRMED'
  AND dt.address IS NOT NULL
  AND dt.voucher_id IS NULL
ON CONFLICT (chain_id, lower(address)) DO UPDATE SET
    name                 = COALESCE(vouchers.name,             EXCLUDED.name),
    symbol               = COALESCE(vouchers.symbol,           EXCLUDED.symbol),
    decimals             = COALESCE(vouchers.decimals,         EXCLUDED.decimals),
    owner_address        = COALESCE(vouchers.owner_address,    EXCLUDED.owner_address),
    voucher_type         = COALESCE(vouchers.voucher_type,     EXCLUDED.voucher_type),
    description          = COALESCE(vouchers.description,      EXCLUDED.description),
    voucher_uoa          = COALESCE(vouchers.voucher_uoa,      EXCLUDED.voucher_uoa),
    voucher_value        = COALESCE(vouchers.voucher_value,    EXCLUDED.voucher_value),
    location_name        = COALESCE(vouchers.location_name,    EXCLUDED.location_name),
    email                = COALESCE(vouchers.email,            EXCLUDED.email),
    website              = COALESCE(vouchers.website,          EXCLUDED.website),
    phone                = COALESCE(vouchers.phone,            EXCLUDED.phone),
    contract_version     = COALESCE(vouchers.contract_version, EXCLUDED.contract_version),
    platform_contract_id = COALESCE(vouchers.platform_contract_id, EXCLUDED.platform_contract_id),
    -- Geo moves as a pair or not at all: vouchers_geo_valid requires lat and
    -- lng to be NULL together, so an independent COALESCE per column could
    -- assemble a half-populated point out of two sources.
    geo_lat = CASE WHEN vouchers.geo_lat IS NULL THEN EXCLUDED.geo_lat ELSE vouchers.geo_lat END,
    geo_lng = CASE WHEN vouchers.geo_lat IS NULL THEN EXCLUDED.geo_lng ELSE vouchers.geo_lng END;

UPDATE deployed_tokens dt
SET voucher_id = v.id
FROM platform_contracts pc, vouchers v
WHERE pc.id = dt.platform_contract_id
  AND v.chain_id = pc.chain_id
  AND lower(v.address) = lower(dt.address)
  AND dt.status = 'CONFIRMED'
  AND dt.address IS NOT NULL
  AND dt.voucher_id IS NULL;

INSERT INTO voucher_tags (voucher_id, tag_value)
SELECT dt.voucher_id, dtt.tag_value
FROM deployed_tokens dt
INNER JOIN deployed_token_tags dtt ON dtt.deployed_token_id = dt.id
WHERE dt.voucher_id IS NOT NULL
ON CONFLICT (voucher_id, tag_value) DO NOTHING;

-- Pools: same three steps.
INSERT INTO pools (
    chain_id, address, name, symbol, decimals, owner_address,
    token_registry_address, limiter_address, quoter_address, fee_policy_address,
    protocol_fee_controller_address, fees_decoupled, description,
    location_name, geo_lat, geo_lng, email, website, phone,
    image_url, banner_url, platform_contract_id
)
SELECT
    pc.chain_id, dp.address, dp.name, dp.symbol, dp.decimals, dp.admin,
    dp.token_registry_address, dp.limiter_address, dp.quoter_address,
    dp.fee_policy_address, dp.protocol_fee_controller_address, dp.fees_decoupled,
    dp.description, dp.location_name, dp.geo_lat, dp.geo_lng,
    dp.email, dp.website, dp.phone, dp.image_url, dp.banner_url,
    dp.platform_contract_id
FROM deployed_pools dp
INNER JOIN platform_contracts pc ON pc.id = dp.platform_contract_id
WHERE dp.status = 'CONFIRMED'
  AND dp.address IS NOT NULL
  AND dp.pool_id IS NULL
ON CONFLICT (chain_id, lower(address)) DO UPDATE SET
    name                            = COALESCE(pools.name,     EXCLUDED.name),
    symbol                          = COALESCE(pools.symbol,   EXCLUDED.symbol),
    decimals                        = COALESCE(pools.decimals, EXCLUDED.decimals),
    owner_address                   = COALESCE(pools.owner_address, EXCLUDED.owner_address),
    token_registry_address          = COALESCE(pools.token_registry_address, EXCLUDED.token_registry_address),
    limiter_address                 = COALESCE(pools.limiter_address, EXCLUDED.limiter_address),
    quoter_address                  = COALESCE(pools.quoter_address, EXCLUDED.quoter_address),
    fee_policy_address              = COALESCE(pools.fee_policy_address, EXCLUDED.fee_policy_address),
    protocol_fee_controller_address = COALESCE(pools.protocol_fee_controller_address, EXCLUDED.protocol_fee_controller_address),
    fees_decoupled                  = COALESCE(pools.fees_decoupled, EXCLUDED.fees_decoupled),
    description                     = COALESCE(pools.description, EXCLUDED.description),
    location_name                   = COALESCE(pools.location_name, EXCLUDED.location_name),
    email                           = COALESCE(pools.email,   EXCLUDED.email),
    website                         = COALESCE(pools.website, EXCLUDED.website),
    phone                           = COALESCE(pools.phone,   EXCLUDED.phone),
    image_url                       = COALESCE(pools.image_url, EXCLUDED.image_url),
    banner_url                      = COALESCE(pools.banner_url, EXCLUDED.banner_url),
    platform_contract_id            = COALESCE(pools.platform_contract_id, EXCLUDED.platform_contract_id),
    geo_lat = CASE WHEN pools.geo_lat IS NULL THEN EXCLUDED.geo_lat ELSE pools.geo_lat END,
    geo_lng = CASE WHEN pools.geo_lat IS NULL THEN EXCLUDED.geo_lng ELSE pools.geo_lng END;

UPDATE deployed_pools dp
SET pool_id = p.id
FROM platform_contracts pc, pools p
WHERE pc.id = dp.platform_contract_id
  AND p.chain_id = pc.chain_id
  AND lower(p.address) = lower(dp.address)
  AND dp.status = 'CONFIRMED'
  AND dp.address IS NOT NULL
  AND dp.pool_id IS NULL;

INSERT INTO pool_tags (pool_id, tag_value)
SELECT dp.pool_id, dpt.tag_value
FROM deployed_pools dp
INNER JOIN deployed_pool_tags dpt ON dpt.deployed_pool_id = dp.id
WHERE dp.pool_id IS NOT NULL
ON CONFLICT (pool_id, tag_value) DO NOTHING;

ANALYZE vouchers;
ANALYZE pools;
