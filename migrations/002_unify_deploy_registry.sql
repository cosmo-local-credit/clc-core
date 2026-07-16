-- Collapse token/pool deploy meta into deployed_* (live rows kept as CONFIRMED).

CREATE TABLE IF NOT EXISTS deploy_status_type (
    value TEXT PRIMARY KEY
);
INSERT INTO deploy_status_type (value) VALUES
    ('PENDING'),
    ('CONFIRMED'),
    ('FAILED')
ON CONFLICT (value) DO NOTHING;

ALTER TABLE deployed_tokens
    ADD COLUMN IF NOT EXISTS status TEXT REFERENCES deploy_status_type(value);

UPDATE deployed_tokens
SET status = 'CONFIRMED'
WHERE status IS NULL;

ALTER TABLE deployed_tokens
    ALTER COLUMN status SET DEFAULT 'PENDING',
    ALTER COLUMN status SET NOT NULL;

-- Token decimals were never stored; staging data uses 6.
ALTER TABLE deployed_tokens
    ADD COLUMN IF NOT EXISTS decimals INT;

UPDATE deployed_tokens
SET decimals = 6
WHERE decimals IS NULL;

ALTER TABLE deployed_tokens
    ALTER COLUMN decimals SET NOT NULL;

ALTER TABLE deployed_tokens
    ALTER COLUMN address DROP NOT NULL;

INSERT INTO deployed_tokens (
    otx_id, platform_contract_id, status, address,
    symbol, name, decimals, admin,
    description, website, email, phone,
    voucher_uoa, voucher_value, location_name, geo_lat, geo_lng,
    created_at, updated_at
)
SELECT
    m.otx_id,
    m.platform_contract_id,
    'PENDING',
    NULL,
    m.token_symbol,
    m.token_name,
    6,
    m.admin,
    m.description,
    m.website,
    m.email,
    m.phone,
    m.voucher_uoa,
    m.voucher_value,
    m.location_name,
    m.geo_lat,
    m.geo_lng,
    m.created_at,
    m.updated_at
FROM token_deploy_meta m
WHERE NOT EXISTS (
    SELECT 1 FROM deployed_tokens dt WHERE dt.otx_id = m.otx_id
)
ON CONFLICT (otx_id) DO NOTHING;

INSERT INTO deployed_token_tags (deployed_token_id, tag_value)
SELECT dt.id, t.tag_value
FROM token_deploy_meta_tags t
INNER JOIN deployed_tokens dt ON dt.otx_id = t.otx_id
ON CONFLICT (deployed_token_id, tag_value) DO NOTHING;

DROP TABLE IF EXISTS token_deploy_meta_tags;
DROP TABLE IF EXISTS token_deploy_meta;

ALTER TABLE deployed_pools
    ADD COLUMN IF NOT EXISTS status TEXT REFERENCES deploy_status_type(value);

UPDATE deployed_pools
SET status = 'CONFIRMED'
WHERE status IS NULL;

ALTER TABLE deployed_pools
    ALTER COLUMN status SET DEFAULT 'PENDING',
    ALTER COLUMN status SET NOT NULL;

ALTER TABLE deployed_pools
    ADD COLUMN IF NOT EXISTS decimals INT;

UPDATE deployed_pools dp
SET decimals = m.pool_decimals
FROM pool_deploy_meta m
WHERE m.otx_id = dp.otx_id
  AND dp.decimals IS NULL;

UPDATE deployed_pools
SET decimals = 6
WHERE decimals IS NULL;

ALTER TABLE deployed_pools
    ALTER COLUMN decimals SET NOT NULL;

ALTER TABLE deployed_pools
    ALTER COLUMN address DROP NOT NULL;

INSERT INTO deployed_pools (
    otx_id, platform_contract_id, status, address,
    name, symbol, decimals,
    token_registry_address, limiter_address, fee_policy_address, quoter_address,
    protocol_fee_controller_address, fees_decoupled, admin,
    description, website, email, phone,
    location_name, geo_lat, geo_lng, image_url, banner_url,
    created_at, updated_at
)
SELECT
    m.otx_id,
    m.platform_contract_id,
    'PENDING',
    NULL,
    m.pool_name,
    m.pool_symbol,
    m.pool_decimals,
    m.token_registry_address,
    m.limiter_address,
    m.fee_policy_address,
    m.quoter_address,
    m.protocol_fee_controller_address,
    m.fees_decoupled,
    m.admin,
    m.description,
    m.website,
    m.email,
    m.phone,
    m.location_name,
    m.geo_lat,
    m.geo_lng,
    m.image_url,
    m.banner_url,
    m.created_at,
    m.updated_at
FROM pool_deploy_meta m
WHERE NOT EXISTS (
    SELECT 1 FROM deployed_pools dp WHERE dp.otx_id = m.otx_id
);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'deployed_pools'::regclass
          AND contype = 'u'
          AND pg_get_constraintdef(oid) LIKE '%(otx_id)%'
    ) THEN
        ALTER TABLE deployed_pools ADD CONSTRAINT deployed_pools_otx_id_key UNIQUE (otx_id);
    END IF;
END $$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'deployed_pools'::regclass
          AND contype = 'u'
          AND pg_get_constraintdef(oid) LIKE '%(address)%'
    ) THEN
        ALTER TABLE deployed_pools ADD CONSTRAINT deployed_pools_address_key UNIQUE (address);
    END IF;
END $$;

INSERT INTO deployed_pool_tags (deployed_pool_id, tag_value)
SELECT dp.id, t.tag_value
FROM pool_deploy_meta_tags t
INNER JOIN deployed_pools dp ON dp.otx_id = t.otx_id
ON CONFLICT (deployed_pool_id, tag_value) DO NOTHING;

DROP TABLE IF EXISTS pool_deploy_meta_tags;
DROP TABLE IF EXISTS pool_deploy_meta;

---- create above / drop below ----
