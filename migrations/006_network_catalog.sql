INSERT INTO token_tag (value) VALUES
    ('Art'),
    ('Biodiversity'),
    ('Celebration'),
    ('Collaboration'),
    ('Common Spaces'),
    ('Culture'),
    ('Dhome'),
    ('Ecovillage'),
    ('Festivals'),
    ('Food Processing'),
    ('Food production'),
    ('House building'),
    ('Mobility'),
    ('Natural building'),
    ('Permaculture'),
    ('Plant Nursery'),
    ('Regenerative Agriculture'),
    ('Rehabilitation'),
    ('Retreat'),
    ('Sociocracy'),
    ('Soil Health'),
    ('Solar Energy'),
    ('Storytelling')
ON CONFLICT (value) DO NOTHING;

CREATE TABLE IF NOT EXISTS voucher_type (
    value TEXT PRIMARY KEY
);
INSERT INTO voucher_type (value) VALUES
    ('DEMURRAGE'),
    ('GIFTABLE'),
    ('GIFTABLE_EXPIRING'),
    ('ERC20'),
    ('UNKNOWN')
ON CONFLICT (value) DO NOTHING;

CREATE TABLE IF NOT EXISTS vouchers (
    id                    INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    chain_id              INT NOT NULL,
    address               TEXT NOT NULL,
    name                  TEXT,
    symbol                TEXT,
    decimals              INT,
    owner_address         TEXT,
    voucher_type          TEXT REFERENCES voucher_type(value),
    sink_address          TEXT,
    redemption_address    TEXT,
    description           TEXT,
    voucher_uoa           TEXT,
    voucher_value         NUMERIC,
    location_name         TEXT,
    geo_lat               FLOAT8,
    geo_lng               FLOAT8,
    email                 TEXT,
    website               TEXT,
    phone                 TEXT,
    icon_url              TEXT,
    banner_url            TEXT,
    terms                 TEXT,
    contract_version      TEXT,
    active                BOOLEAN NOT NULL DEFAULT true,
    internal              BOOLEAN NOT NULL DEFAULT false,
    platform_contract_id  INT REFERENCES platform_contracts(id),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT vouchers_address_format
        CHECK (address ~ '^0x[0-9a-fA-F]{40}$'),
    CONSTRAINT vouchers_owner_format
        CHECK (owner_address IS NULL
            OR owner_address ~ '^0x[0-9a-fA-F]{40}$'),
    CONSTRAINT vouchers_sink_format
        CHECK (sink_address IS NULL
            OR sink_address ~ '^0x[0-9a-fA-F]{40}$'),
    CONSTRAINT vouchers_redemption_format
        CHECK (redemption_address IS NULL
            OR redemption_address ~ '^0x[0-9a-fA-F]{40}$'),
    CONSTRAINT vouchers_decimals_range
        CHECK (decimals IS NULL OR decimals BETWEEN 0 AND 255),
    CONSTRAINT vouchers_value_nonneg
        CHECK (voucher_value IS NULL OR voucher_value >= 0),
    CONSTRAINT vouchers_geo_valid CHECK (
        ((geo_lat IS NULL) = (geo_lng IS NULL))
        AND (geo_lat IS NULL OR geo_lat BETWEEN -90 AND 90)
        AND (geo_lng IS NULL OR geo_lng BETWEEN -180 AND 180)
    ),
    UNIQUE (chain_id, address)
);

CREATE UNIQUE INDEX IF NOT EXISTS vouchers_chain_address_lower_key
    ON vouchers (chain_id, lower(address));

DO $$
BEGIN
    CREATE TRIGGER update_vouchers_timestamp
        BEFORE UPDATE ON vouchers
        FOR EACH ROW EXECUTE PROCEDURE update_timestamp();
EXCEPTION
    WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS voucher_tags (
    voucher_id INT  NOT NULL REFERENCES vouchers(id) ON DELETE CASCADE,
    tag_value  TEXT NOT NULL REFERENCES token_tag(value),
    PRIMARY KEY (voucher_id, tag_value)
);

ALTER TABLE deployed_tokens
    ADD COLUMN IF NOT EXISTS voucher_id INT UNIQUE REFERENCES vouchers(id);

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS default_voucher_id INT REFERENCES vouchers(id);

CREATE TABLE IF NOT EXISTS pools (
    id                              INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    chain_id                        INT NOT NULL,
    address                         TEXT NOT NULL,
    name                            TEXT,
    symbol                          TEXT,
    owner_address                   TEXT,
    fee_ppm                         NUMERIC,
    fee_address                     TEXT,
    token_registry_address          TEXT,
    limiter_address                 TEXT,
    quoter_address                  TEXT,
    fee_policy_address              TEXT,
    protocol_fee_controller_address TEXT,
    fees_decoupled                  BOOLEAN,
    decimals                        INT,
    description                     TEXT,
    unit_of_account                 TEXT,
    location_name                   TEXT,
    geo_lat                         FLOAT8,
    geo_lng                         FLOAT8,
    email                           TEXT,
    website                         TEXT,
    phone                           TEXT,
    image_url                       TEXT,
    banner_url                      TEXT,
    terms                           TEXT,
    default_voucher_id              INT REFERENCES vouchers(id),
    platform_contract_id            INT REFERENCES platform_contracts(id),
    quoter_type                     TEXT,
    created_at                      TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at                      TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT pools_quoter_type_valid
        CHECK (quoter_type IS NULL
            OR quoter_type IN ('decimal', 'oracle', 'relative')),
    CONSTRAINT pools_address_format
        CHECK (address ~ '^0x[0-9a-fA-F]{40}$'),
    CONSTRAINT pools_owner_format
        CHECK (owner_address IS NULL
            OR owner_address ~ '^0x[0-9a-fA-F]{40}$'),
    CONSTRAINT pools_fee_address_format
        CHECK (fee_address IS NULL
            OR fee_address ~ '^0x[0-9a-fA-F]{40}$'),
    CONSTRAINT pools_token_registry_format
        CHECK (token_registry_address IS NULL
            OR token_registry_address ~ '^0x[0-9a-fA-F]{40}$'),
    CONSTRAINT pools_limiter_format
        CHECK (limiter_address IS NULL
            OR limiter_address ~ '^0x[0-9a-fA-F]{40}$'),
    CONSTRAINT pools_quoter_format
        CHECK (quoter_address IS NULL
            OR quoter_address ~ '^0x[0-9a-fA-F]{40}$'),
    CONSTRAINT pools_fee_policy_format
        CHECK (fee_policy_address IS NULL
            OR fee_policy_address ~ '^0x[0-9a-fA-F]{40}$'),
    CONSTRAINT pools_protocol_fee_controller_format
        CHECK (protocol_fee_controller_address IS NULL
            OR protocol_fee_controller_address ~ '^0x[0-9a-fA-F]{40}$'),
    CONSTRAINT pools_fee_ppm_range
        CHECK (fee_ppm IS NULL OR fee_ppm BETWEEN 0 AND 1000000),
    CONSTRAINT pools_decimals_range
        CHECK (decimals IS NULL OR decimals BETWEEN 0 AND 255),
    CONSTRAINT pools_geo_valid CHECK (
        ((geo_lat IS NULL) = (geo_lng IS NULL))
        AND (geo_lat IS NULL OR geo_lat BETWEEN -90 AND 90)
        AND (geo_lng IS NULL OR geo_lng BETWEEN -180 AND 180)
    ),
    UNIQUE (chain_id, address)
);

CREATE UNIQUE INDEX IF NOT EXISTS pools_chain_address_lower_key
    ON pools (chain_id, lower(address));

DO $$
BEGIN
    CREATE TRIGGER update_pools_timestamp
        BEFORE UPDATE ON pools
        FOR EACH ROW EXECUTE PROCEDURE update_timestamp();
EXCEPTION
    WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS pool_tags (
    pool_id   INT  NOT NULL REFERENCES pools(id) ON DELETE CASCADE,
    tag_value TEXT NOT NULL REFERENCES token_tag(value),
    PRIMARY KEY (pool_id, tag_value)
);

CREATE TABLE IF NOT EXISTS pool_tokens (
    pool_id       INT NOT NULL REFERENCES pools(id) ON DELETE CASCADE,
    voucher_id    INT NOT NULL REFERENCES vouchers(id),
    is_allowed    BOOLEAN NOT NULL DEFAULT true,
    exchange_rate NUMERIC,
    token_limit   NUMERIC,
    synced_at     TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (pool_id, voucher_id),
    CONSTRAINT pool_tokens_rate_nonneg
        CHECK (exchange_rate IS NULL OR exchange_rate >= 0),
    CONSTRAINT pool_tokens_limit_nonneg
        CHECK (token_limit IS NULL OR token_limit >= 0)
);

CREATE INDEX IF NOT EXISTS pool_tokens_voucher_id_idx ON pool_tokens (voucher_id);

ALTER TABLE deployed_pools
    ADD COLUMN IF NOT EXISTS pool_id INT UNIQUE REFERENCES pools(id);

CREATE TABLE IF NOT EXISTS report_status_type (
    value TEXT PRIMARY KEY
);
INSERT INTO report_status_type (value) VALUES
    ('DRAFT'),
    ('SUBMITTED'),
    ('APPROVED'),
    ('REJECTED')
ON CONFLICT (value) DO NOTHING;

CREATE TABLE IF NOT EXISTS reports (
    id               INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source_id        INT UNIQUE,
    title            TEXT NOT NULL,
    body             JSONB NOT NULL,
    description      TEXT NOT NULL DEFAULT '',
    image_url        TEXT,
    location_name    TEXT,
    geo_lat          FLOAT8,
    geo_lng          FLOAT8,
    period_from      TIMESTAMPTZ,
    period_to        TIMESTAMPTZ,
    author_address   TEXT NOT NULL,
    modifier_address TEXT,
    status           TEXT NOT NULL
                         REFERENCES report_status_type(value)
                         DEFAULT 'DRAFT',
    rejection_reason TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT reports_author_format
        CHECK (author_address ~ '^0x[0-9a-fA-F]{40}$'),
    CONSTRAINT reports_modifier_format
        CHECK (modifier_address IS NULL
            OR modifier_address ~ '^0x[0-9a-fA-F]{40}$'),
    CONSTRAINT reports_geo_valid CHECK (
        ((geo_lat IS NULL) = (geo_lng IS NULL))
        AND (geo_lat IS NULL OR geo_lat BETWEEN -90 AND 90)
        AND (geo_lng IS NULL OR geo_lng BETWEEN -180 AND 180)
    ),
    CONSTRAINT reports_period_valid
        CHECK (
            period_from IS NULL
            OR period_to IS NULL
            OR period_from <= period_to
        ),
    CONSTRAINT reports_rejection_when_rejected
        CHECK (
            status <> 'REJECTED'
            OR (rejection_reason IS NOT NULL AND btrim(rejection_reason) <> '')
        ),
    CONSTRAINT reports_rejection_only_if_rejected
        CHECK (status = 'REJECTED' OR rejection_reason IS NULL)
);

DO $$
BEGIN
    CREATE TRIGGER update_reports_timestamp
        BEFORE UPDATE ON reports
        FOR EACH ROW EXECUTE PROCEDURE update_timestamp();
EXCEPTION
    WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS report_vouchers (
    report_id  INT NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
    voucher_id INT NOT NULL REFERENCES vouchers(id),
    PRIMARY KEY (report_id, voucher_id)
);

CREATE TABLE IF NOT EXISTS report_tags (
    report_id INT  NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
    tag_value TEXT NOT NULL REFERENCES token_tag(value),
    PRIMARY KEY (report_id, tag_value)
);

CREATE TABLE IF NOT EXISTS report_verifiers (
    report_id        INT  NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
    verifier_address TEXT NOT NULL,
    PRIMARY KEY (report_id, verifier_address),
    CONSTRAINT report_verifiers_address_format
        CHECK (verifier_address ~ '^0x[0-9a-fA-F]{40}$')
);

CREATE INDEX IF NOT EXISTS report_vouchers_voucher_id_idx
    ON report_vouchers (voucher_id);
CREATE INDEX IF NOT EXISTS reports_status_created_idx
    ON reports (status, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS reports_author_created_idx
    ON reports (author_address, created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS commodity_type (
    value TEXT PRIMARY KEY
);
INSERT INTO commodity_type (value) VALUES
    ('GOOD'),
    ('SERVICE')
ON CONFLICT (value) DO NOTHING;

CREATE TABLE IF NOT EXISTS offering_frequency (
    value TEXT PRIMARY KEY
);
INSERT INTO offering_frequency (value) VALUES
    ('day'),
    ('week'),
    ('month'),
    ('year')
ON CONFLICT (value) DO NOTHING;

CREATE TABLE IF NOT EXISTS offerings (
    id               INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source_id        INT UNIQUE,
    voucher_id       INT NOT NULL REFERENCES vouchers(id),
    seller_address   TEXT NOT NULL,
    commodity_name   TEXT NOT NULL,
    commodity_type   TEXT NOT NULL REFERENCES commodity_type(value),
    description      TEXT NOT NULL DEFAULT '',
    quantity         NUMERIC NOT NULL,
    unit             TEXT,
    frequency        TEXT REFERENCES offering_frequency(value),
    price            NUMERIC,
    available        BOOLEAN NOT NULL DEFAULT true,
    location_name    TEXT,
    geo_lat          FLOAT8,
    geo_lng          FLOAT8,
    image_url        TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT offerings_seller_format
        CHECK (seller_address ~ '^0x[0-9a-fA-F]{40}$'),
    CONSTRAINT offerings_price_nonneg
        CHECK (price IS NULL OR price >= 0),
    CONSTRAINT offerings_quantity_nonneg
        CHECK (quantity >= 0),
    CONSTRAINT offerings_geo_valid CHECK (
        ((geo_lat IS NULL) = (geo_lng IS NULL))
        AND (geo_lat IS NULL OR geo_lat BETWEEN -90 AND 90)
        AND (geo_lng IS NULL OR geo_lng BETWEEN -180 AND 180)
    )
);

DO $$
BEGIN
    CREATE TRIGGER update_offerings_timestamp
        BEFORE UPDATE ON offerings
        FOR EACH ROW EXECUTE PROCEDURE update_timestamp();
EXCEPTION
    WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS offering_tags (
    offering_id INT  NOT NULL REFERENCES offerings(id) ON DELETE CASCADE,
    tag_value   TEXT NOT NULL REFERENCES token_tag(value),
    PRIMARY KEY (offering_id, tag_value)
);

CREATE INDEX IF NOT EXISTS offerings_voucher_id_idx ON offerings (voucher_id);
CREATE INDEX IF NOT EXISTS offerings_seller_created_idx
    ON offerings (seller_address, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS offerings_available_created_idx
    ON offerings (available, created_at DESC, id DESC)
    WHERE available;
CREATE INDEX IF NOT EXISTS offerings_type_available_idx
    ON offerings (commodity_type, available, created_at DESC, id DESC);
