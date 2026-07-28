-- Functions
CREATE OR REPLACE FUNCTION insert_starting_nonce()
    RETURNS trigger AS $$
BEGIN
    INSERT INTO noncestore (keystore_id) VALUES (new.id);
    RETURN new;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION update_timestamp()
    RETURNS trigger AS $$
BEGIN
    new.updated_at = current_timestamp;
    RETURN new;
END;
$$ LANGUAGE plpgsql;

-- Keystore
CREATE TABLE IF NOT EXISTS keystore (
    id          INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_key  TEXT NOT NULL,
    private_key TEXT NOT NULL,
    active      BOOLEAN DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT keystore_public_key_key UNIQUE (public_key),
    CONSTRAINT keystore_public_key_format CHECK (public_key ~ '^0x[0-9a-fA-F]{40}$')
);
CREATE UNIQUE INDEX IF NOT EXISTS keystore_public_key_lower_key ON keystore (lower(public_key));

CREATE TRIGGER insert_starting_nonce
    AFTER INSERT ON keystore
    FOR EACH ROW EXECUTE PROCEDURE insert_starting_nonce();

CREATE TRIGGER update_keystore_timestamp
    BEFORE UPDATE ON keystore
    FOR EACH ROW EXECUTE PROCEDURE update_timestamp();

-- Master key
CREATE TABLE IF NOT EXISTS master_key (
    id         INT PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT master_key_id_fkey FOREIGN KEY (id) REFERENCES keystore(id)
);
CREATE UNIQUE INDEX IF NOT EXISTS master_key_singleton_idx ON master_key ((true));

CREATE TRIGGER update_master_key_timestamp
    BEFORE UPDATE ON master_key
    FOR EACH ROW EXECUTE PROCEDURE update_timestamp();

-- Noncestore
CREATE TABLE IF NOT EXISTS noncestore (
    keystore_id INT PRIMARY KEY REFERENCES keystore(id),
    next_nonce  BIGINT NOT NULL DEFAULT 0,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT noncestore_next_nonce_nonneg CHECK (next_nonce >= 0)
);

CREATE TRIGGER update_nonce_timestamp
    BEFORE UPDATE ON noncestore
    FOR EACH ROW EXECUTE PROCEDURE update_timestamp();

-- OTX transaction type lookup
CREATE TABLE IF NOT EXISTS otx_tx_type (
    value TEXT PRIMARY KEY
);
INSERT INTO otx_tx_type (value) VALUES
    ('TOKEN_PROXY_DEPLOY'),
    ('POOL_DEPLOY_STEP'),
    ('POOL_PROXY_DEPLOY'),
    ('POOL_TOKEN_INDEX_DEPLOY'),
    ('POOL_LIMITER_DEPLOY'),
    ('POOL_FEE_POLICY_DEPLOY'),
    ('POOL_ORACLE_QUOTER_DEPLOY'),
    ('POOL_RELATIVE_QUOTER_DEPLOY'),
    ('PROXY_UPGRADE'),
    ('RELAY_DELEGATE'),
    ('RELAY_EXEC'),
    ('RELAY_DELEGATE_EXEC'),
    ('ACCOUNT_REGISTER')
ON CONFLICT (value) DO NOTHING;

-- Dispatch status type lookup
CREATE TABLE IF NOT EXISTS dispatch_status_type (
    value TEXT PRIMARY KEY
);
INSERT INTO dispatch_status_type (value) VALUES
    ('PENDING'),
    ('IN_NETWORK'),
    ('SUCCESS'),
    ('REVERTED'),
    ('LOW_NONCE'),
    ('NO_GAS'),
    ('LOW_GAS_PRICE'),
    ('NETWORK_ERROR'),
    ('REPLACEMENT_UNDERPRICED'),
    ('UNKNOWN_RPC_ERROR')
ON CONFLICT (value) DO NOTHING;

-- OTX
CREATE TABLE IF NOT EXISTS otx (
    id             INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tracking_id    UUID NOT NULL,
    otx_type       TEXT REFERENCES otx_tx_type(value) NOT NULL,
    signer_account INT REFERENCES keystore(id) NOT NULL,
    raw_tx         TEXT NOT NULL,
    tx_hash        TEXT NOT NULL,
    nonce          BIGINT NOT NULL,
    replaced       BOOLEAN DEFAULT false,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT otx_nonce_nonneg CHECK (nonce >= 0)
);

-- Dispatch
CREATE TABLE IF NOT EXISTS dispatch (
    id         INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    otx_id     INT NOT NULL REFERENCES otx(id),
    status     TEXT REFERENCES dispatch_status_type(value) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT dispatch_otx_id_unique UNIQUE (otx_id)
);

CREATE TRIGGER update_dispatch_timestamp
    BEFORE UPDATE ON dispatch
    FOR EACH ROW EXECUTE PROCEDURE update_timestamp();

-- Contract type
CREATE TYPE contract_type AS ENUM (
    'ERC20',
    'ERC20_DEMURRAGE',
    'SWAPPOOL',
    'TOKEN_UNIQUE_SYMBOL_INDEX',
    'LIMITER',
    'FEE_POLICY',
    'ORACLE_QUOTER',
    'RELATIVE_QUOTER'
);

-- Upgrade meta: staging table to carry upgrade context from worker
-- through to the subscriber confirmation path.
CREATE TABLE IF NOT EXISTS upgrade_meta (
    id                 INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    otx_id             INT REFERENCES otx(id) NOT NULL UNIQUE,
    proxy_address      TEXT NOT NULL,
    contract_type      contract_type NOT NULL,
    new_implementation TEXT NOT NULL,
    version            TEXT NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT upgrade_meta_address_format
        CHECK (proxy_address      ~ '^0x[0-9a-fA-F]{40}$'
           AND new_implementation ~ '^0x[0-9a-fA-F]{40}$')
);

-- Platform contracts
CREATE TABLE IF NOT EXISTS platform_contracts (
    id                      INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    chain_id                INT NOT NULL,
    contract_type           contract_type NOT NULL,
    factory_address         TEXT NOT NULL,
    implementation_address  TEXT NOT NULL,
    version                 TEXT NOT NULL DEFAULT '',
    created_at              TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (chain_id, contract_type),
    CONSTRAINT platform_contracts_address_format
        CHECK (factory_address        ~ '^0x[0-9a-fA-F]{40}$'
           AND implementation_address ~ '^0x[0-9a-fA-F]{40}$')
);

-- Seed platform contracts (Celo mainnet, chain 42220)
INSERT INTO platform_contracts (chain_id, contract_type, factory_address, implementation_address, version) VALUES
    (42220, 'ERC20', '0x3e302C5965954D84Ab3dac664C6152b54D7daa00', '0x1F74298ff3CCF718c50c557d2b9a54040F295012', 'v0.5.5'),
    (42220, 'SWAPPOOL', '0x3e302C5965954D84Ab3dac664C6152b54D7daa00', '0xCF879ADd8c34083b48c8a638D3C166eFcF35D454', 'v0.5.0'),
    (42220, 'TOKEN_UNIQUE_SYMBOL_INDEX', '0x3e302C5965954D84Ab3dac664C6152b54D7daa00', '0xA2d04499e68B0B295bf0331D516DcA8A30Fc51c6', 'v0.5.0'),
    (42220, 'LIMITER', '0x3e302C5965954D84Ab3dac664C6152b54D7daa00', '0x392d269E5AB4d6024AccD3b2F7dE0b79E0f7602f', 'v0.5.0'),
    (42220, 'FEE_POLICY', '0x3e302C5965954D84Ab3dac664C6152b54D7daa00', '0xa4dF0D9713D42dbEBE139B3F020E2f14AF8fb37e', 'v0.5.0'),
    (42220, 'ORACLE_QUOTER', '0x3e302C5965954D84Ab3dac664C6152b54D7daa00', '0x0F753b191d01538d24B89968A90b5A1aE3753E0d', 'v0.5.0'),
    (42220, 'RELATIVE_QUOTER', '0x3e302C5965954D84Ab3dac664C6152b54D7daa00', '0x9107e667aB5F1F05dB5285B2E93c50C3Af47B710', 'v0.5.0')
ON CONFLICT (chain_id, contract_type) DO UPDATE SET
    factory_address        = EXCLUDED.factory_address,
    implementation_address = EXCLUDED.implementation_address,
    version                = EXCLUDED.version,
    updated_at             = CURRENT_TIMESTAMP;

-- Deployed token registry: tracks successfully deployed ERC20 tokens
CREATE TABLE IF NOT EXISTS deployed_tokens (
    id                    INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    otx_id                INT REFERENCES otx(id) NOT NULL,
    platform_contract_id  INT REFERENCES platform_contracts(id) NOT NULL,
    address               TEXT NOT NULL,
    symbol                TEXT NOT NULL,
    name                  TEXT NOT NULL,
    admin                 TEXT NOT NULL,
    description           TEXT,
    website               TEXT,
    email                 TEXT,
    phone                 TEXT,
    voucher_uoa           TEXT,
    voucher_value         NUMERIC,
    location_name         TEXT,
    geo_lat               FLOAT8,
    geo_lng               FLOAT8,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (otx_id),
    UNIQUE (address),
    CONSTRAINT deployed_tokens_address_format
        CHECK (address ~ '^0x[0-9a-fA-F]{40}$'
           AND admin   ~ '^0x[0-9a-fA-F]{40}$'),
    CONSTRAINT deployed_tokens_geo_valid
        CHECK (
            ((geo_lat IS NULL) = (geo_lng IS NULL))
            AND (geo_lat IS NULL OR geo_lat BETWEEN -90 AND 90)
            AND (geo_lng IS NULL OR geo_lng BETWEEN -180 AND 180)
        )
);
CREATE UNIQUE INDEX IF NOT EXISTS deployed_tokens_address_lower_key ON deployed_tokens (lower(address));

CREATE TRIGGER update_deployed_tokens_timestamp
    BEFORE UPDATE ON deployed_tokens
    FOR EACH ROW EXECUTE PROCEDURE update_timestamp();

-- Token deploy metadata: staging table to carry token metadata from deploy
-- time through to the subscriber confirmation path.
CREATE TABLE IF NOT EXISTS token_deploy_meta (
    id                    INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    otx_id                INT REFERENCES otx(id) NOT NULL UNIQUE,
    platform_contract_id  INT REFERENCES platform_contracts(id) NOT NULL,
    token_name            TEXT NOT NULL,
    token_symbol          TEXT NOT NULL,
    admin                 TEXT NOT NULL,
    description           TEXT,
    website               TEXT,
    email                 TEXT,
    phone                 TEXT,
    voucher_uoa           TEXT,
    voucher_value         NUMERIC,
    location_name         TEXT,
    geo_lat               FLOAT8,
    geo_lng               FLOAT8,
    created_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TRIGGER update_token_deploy_meta_timestamp
    BEFORE UPDATE ON token_deploy_meta
    FOR EACH ROW EXECUTE PROCEDURE update_timestamp();

-- Shared tag lookup for token and pool metadata.
CREATE TABLE IF NOT EXISTS token_tag (
    value TEXT PRIMARY KEY
);
INSERT INTO token_tag (value) VALUES
    ('Agriculture'),
    ('Care'),
    ('Construction'),
    ('Cleaning'),
    ('Education'),
    ('Elders'),
    ('Endowments'),
    ('Fellowship'),
    ('Gathering'),
    ('Handcrafts'),
    ('Housekeeping'),
    ('Lending'),
    ('Market'),
    ('Outreach'),
    ('Software'),
    ('Tree Planting'),
    ('Training'),
    ('Volunteer'),
    ('Water Catchment'),
    ('Youth'),
    ('Other')
ON CONFLICT (value) DO NOTHING;

CREATE TABLE IF NOT EXISTS token_deploy_meta_tags (
    otx_id    INT  REFERENCES token_deploy_meta(otx_id) ON DELETE CASCADE NOT NULL,
    tag_value TEXT REFERENCES token_tag(value) NOT NULL,
    PRIMARY KEY (otx_id, tag_value)
);

CREATE TABLE IF NOT EXISTS deployed_token_tags (
    deployed_token_id INT  REFERENCES deployed_tokens(id) ON DELETE CASCADE NOT NULL,
    tag_value         TEXT REFERENCES token_tag(value) NOT NULL,
    PRIMARY KEY (deployed_token_id, tag_value)
);

CREATE TABLE IF NOT EXISTS protocol_addresses (
    id         INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    key        TEXT NOT NULL UNIQUE,
    address    TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT protocol_addresses_address_format CHECK (address ~ '^0x[0-9a-fA-F]{40}$')
);

CREATE TRIGGER update_protocol_addresses_timestamp
    BEFORE UPDATE ON protocol_addresses
    FOR EACH ROW EXECUTE PROCEDURE update_timestamp();

INSERT INTO protocol_addresses (key, address) VALUES
    ('protocol_fee_controller', '0x410e7Ff8410B0f8D15CA762a51F8b8A6Cd6eE9BE'),
    ('decimal_quoter', '0x7ff73c1833FdA7C0f458c079496015F9D22f64f4')
ON CONFLICT (key) DO UPDATE SET
    address    = EXCLUDED.address,
    updated_at = CURRENT_TIMESTAMP;

-- Pool deploy metadata: staging table to carry pool deployment context from
-- deploy time through to the subscriber confirmation path.
CREATE TABLE IF NOT EXISTS pool_deploy_meta (
    id                              INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    otx_id                          INT REFERENCES otx(id) NOT NULL UNIQUE,
    platform_contract_id            INT REFERENCES platform_contracts(id) NOT NULL,
    pool_name                       TEXT NOT NULL,
    pool_symbol                     TEXT NOT NULL,
    pool_decimals                   INT NOT NULL,
    token_registry_address          TEXT NOT NULL,
    limiter_address                 TEXT NOT NULL,
    fee_policy_address              TEXT NOT NULL,
    quoter_address                  TEXT NOT NULL,
    protocol_fee_controller_address TEXT NOT NULL,
    fees_decoupled                  BOOLEAN NOT NULL DEFAULT false,
    admin                           TEXT NOT NULL,
    description                     TEXT,
    website                         TEXT,
    email                           TEXT,
    phone                           TEXT,
    location_name                   TEXT,
    geo_lat                         FLOAT8,
    geo_lng                         FLOAT8,
    image_url                       TEXT,
    banner_url                      TEXT,
    created_at                      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at                      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TRIGGER update_pool_deploy_meta_timestamp
    BEFORE UPDATE ON pool_deploy_meta
    FOR EACH ROW EXECUTE PROCEDURE update_timestamp();

-- Deployed pool registry: tracks successfully deployed SwapPool instances
CREATE TABLE IF NOT EXISTS deployed_pools (
    id                              INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    otx_id                          INT REFERENCES otx(id) NOT NULL,
    platform_contract_id            INT REFERENCES platform_contracts(id) NOT NULL,
    address                         TEXT NOT NULL,
    name                            TEXT NOT NULL,
    symbol                          TEXT NOT NULL,
    token_registry_address          TEXT NOT NULL,
    limiter_address                 TEXT NOT NULL,
    fee_policy_address              TEXT NOT NULL,
    quoter_address                  TEXT NOT NULL,
    protocol_fee_controller_address TEXT NOT NULL,
    fees_decoupled                  BOOLEAN NOT NULL DEFAULT false,
    admin                           TEXT NOT NULL,
    description                     TEXT,
    website                         TEXT,
    email                           TEXT,
    phone                           TEXT,
    location_name                   TEXT,
    geo_lat                         FLOAT8,
    geo_lng                         FLOAT8,
    created_at                      TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at                      TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    image_url                       TEXT,
    banner_url                      TEXT,
    CONSTRAINT deployed_pools_address_format
        CHECK (address                         ~ '^0x[0-9a-fA-F]{40}$'
           AND admin                           ~ '^0x[0-9a-fA-F]{40}$'
           AND token_registry_address          ~ '^0x[0-9a-fA-F]{40}$'
           AND limiter_address                 ~ '^0x[0-9a-fA-F]{40}$'
           AND fee_policy_address              ~ '^0x[0-9a-fA-F]{40}$'
           AND quoter_address                  ~ '^0x[0-9a-fA-F]{40}$'
           AND protocol_fee_controller_address ~ '^0x[0-9a-fA-F]{40}$'),
    CONSTRAINT deployed_pools_geo_valid
        CHECK (
            ((geo_lat IS NULL) = (geo_lng IS NULL))
            AND (geo_lat IS NULL OR geo_lat BETWEEN -90 AND 90)
            AND (geo_lng IS NULL OR geo_lng BETWEEN -180 AND 180)
        )
);
CREATE UNIQUE INDEX IF NOT EXISTS deployed_pools_address_lower_key ON deployed_pools (lower(address));

CREATE TRIGGER update_deployed_pools_timestamp
    BEFORE UPDATE ON deployed_pools
    FOR EACH ROW EXECUTE PROCEDURE update_timestamp();

-- Keep the squashed baseline safe for databases initialized before pool image
-- metadata was introduced.
ALTER TABLE pool_deploy_meta ADD COLUMN IF NOT EXISTS image_url TEXT;
ALTER TABLE pool_deploy_meta ADD COLUMN IF NOT EXISTS banner_url TEXT;
ALTER TABLE deployed_pools ADD COLUMN IF NOT EXISTS image_url TEXT;
ALTER TABLE deployed_pools ADD COLUMN IF NOT EXISTS banner_url TEXT;

CREATE TABLE IF NOT EXISTS pool_deploy_meta_tags (
    otx_id    INT  REFERENCES pool_deploy_meta(otx_id) ON DELETE CASCADE NOT NULL,
    tag_value TEXT REFERENCES token_tag(value) NOT NULL,
    PRIMARY KEY (otx_id, tag_value)
);

CREATE TABLE IF NOT EXISTS deployed_pool_tags (
    deployed_pool_id INT  REFERENCES deployed_pools(id) ON DELETE CASCADE NOT NULL,
    tag_value        TEXT REFERENCES token_tag(value) NOT NULL,
    PRIMARY KEY (deployed_pool_id, tag_value)
);

-- Chainlink oracle feed registry: stores known oracle feed addresses for price feeds
CREATE TABLE IF NOT EXISTS oracle_feeds (
    id         INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    address    TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT oracle_feeds_address_format CHECK (address ~ '^0x[0-9a-fA-F]{40}$')
);

CREATE TRIGGER update_oracle_feeds_timestamp
    BEFORE UPDATE ON oracle_feeds
    FOR EACH ROW EXECUTE PROCEDURE update_timestamp();

-- Seed Chainlink oracle feeds (Celo mainnet, all relative to USD)
INSERT INTO oracle_feeds (name, address) VALUES
    ('USDT', '0x5e37AF40A7A344ec9b03CCD34a250F3dA9a20B02'),
    ('USDC', '0xc7A353BaE210aed958a1A2928b654938EC59DaB2'),
    ('EURC', '0xc7A353BaE210aed958a1A2928b654938EC59DaB2'),
    ('CELO', '0x0568fD19986748cEfF3301e55c0eb1E729E0Ab7e'),
    ('CUSD', '0xe38A27BE4E7d866327e09736F3C570F256FFd048'),
    ('BRL',  '0xe8EcaF727080968Ed5F6DBB595B91e50eEb9F8B3'),
    ('GBP',  '0xe76FE54dfeD2ce8B4d1AC63c982DfF7CFc92bf82'),
    ('NGN',  '0xc17cBE2dB40e53F4984C46F608DA6DA1fF074c11'),
    ('ZAR',  '0x11b7221a0DD025778A95e9E0B87b477522C32E02'),
    ('INR',  '0x85d4Ec34339478F73c153710B19f2D5C402dce6F'),
    ('KES',  '0x85d4Ec34339478F73c153710B19f2D5C402dce6F')
ON CONFLICT (name) DO UPDATE SET
    address    = EXCLUDED.address,
    updated_at = CURRENT_TIMESTAMP;

-- User role and status enumerations
CREATE TYPE user_role AS ENUM ('USER', 'SUPERADMIN');

CREATE TYPE user_status AS ENUM ('PENDING', 'APPROVED', 'REJECTED');

-- Users: stores registered accounts authenticated via SIWE
CREATE TABLE IF NOT EXISTS users (
    id                  INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    blockchain_address  TEXT NOT NULL,
    name                TEXT NOT NULL,
    email               TEXT,
    phone               TEXT,
    country             TEXT NOT NULL DEFAULT '',
    role                user_role NOT NULL DEFAULT 'USER',
    status              user_status NOT NULL DEFAULT 'PENDING',
    max_pools           INT NOT NULL DEFAULT 10,
    max_tokens          INT NOT NULL DEFAULT 20,
    created_at          TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT users_blockchain_address_unique UNIQUE (blockchain_address),
    CONSTRAINT users_email_unique UNIQUE (email),
    CONSTRAINT users_phone_unique UNIQUE (phone),
    CONSTRAINT users_at_least_one_contact CHECK (email IS NOT NULL OR phone IS NOT NULL),
    CONSTRAINT users_blockchain_address_format CHECK (blockchain_address ~ '^0x[0-9a-fA-F]{40}$'),
    CONSTRAINT users_quotas_nonneg CHECK (max_pools >= 0 AND max_tokens >= 0)
);

CREATE UNIQUE INDEX IF NOT EXISTS users_blockchain_address_lower_key ON users (lower(blockchain_address));
CREATE INDEX IF NOT EXISTS users_status_idx ON users(status);

CREATE TRIGGER update_users_timestamp
    BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE PROCEDURE update_timestamp();

-- User deployments: links a user to a tracked on-chain resource (token or pool)
CREATE TABLE IF NOT EXISTS user_deployments (
    id            INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id       INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tracking_id   UUID NOT NULL UNIQUE,
    resource_type TEXT NOT NULL CHECK (resource_type IN ('TOKEN', 'POOL')),
    created_at    TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS user_deployments_user_id_idx ON user_deployments(user_id);

-- WebAuthn credentials registered through the relayer during initial
-- ephemeral-key registration. Looked up by credential_id during recovery
-- to map a passkey back to its Calibur-delegated wallet.
CREATE TABLE IF NOT EXISTS webauthn_credentials (
    credential_id TEXT        PRIMARY KEY,
    address       TEXT        NOT NULL,
    key_hash      TEXT        NOT NULL,
    public_key_x  NUMERIC     NOT NULL,
    public_key_y  NUMERIC     NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT webauthn_address_format CHECK (address ~ '^0x[0-9a-fA-F]{40}$')
);
CREATE INDEX IF NOT EXISTS webauthn_credentials_address_idx
    ON webauthn_credentials (address);

-- Seed superadmin accounts (blockchain_address is EIP-55 checksummed — the
-- canonical stored form for all addresses)
INSERT INTO users (blockchain_address, name, email, role, status) VALUES
    ('0x33a573149db22e759fB9A38Bcc461C12855c2645', 'Superadmin', 'devops@grassecon.org', 'SUPERADMIN', 'APPROVED')
ON CONFLICT (blockchain_address) DO NOTHING;

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

CREATE TRIGGER update_vouchers_timestamp
    BEFORE UPDATE ON vouchers
    FOR EACH ROW EXECUTE PROCEDURE update_timestamp();

CREATE TABLE IF NOT EXISTS voucher_tags (
    voucher_id INT  NOT NULL REFERENCES vouchers(id) ON DELETE CASCADE,
    tag_value  TEXT NOT NULL REFERENCES token_tag(value),
    PRIMARY KEY (voucher_id, tag_value)
);

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
    created_at                      TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at                      TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
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

CREATE TRIGGER update_pools_timestamp
    BEFORE UPDATE ON pools
    FOR EACH ROW EXECUTE PROCEDURE update_timestamp();

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

CREATE TABLE IF NOT EXISTS report_status_type (
    value TEXT PRIMARY KEY
);
INSERT INTO report_status_type (value) VALUES
    ('DRAFT'),
    ('SUBMITTED'),
    ('APPROVED'),
    ('REJECTED')
ON CONFLICT (value) DO NOTHING;

-- `body` is markdown TEXT, not the editor's JSONB document: images,
-- attachments and the field-report form are separated out at write time by
-- migration 010, so no request re-parses a document to serve a report.
-- Column order matches what 010 leaves behind on an upgraded database.
CREATE TABLE IF NOT EXISTS reports (
    id               INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source_id        INT UNIQUE,
    title            TEXT NOT NULL,
    body             TEXT NOT NULL DEFAULT '',
    description      TEXT NOT NULL DEFAULT '',
    image_url        TEXT,
    geo_lat          FLOAT8,
    geo_lng          FLOAT8,
    period_from      TIMESTAMPTZ,
    period_to        TIMESTAMPTZ,
    author_address   TEXT NOT NULL,
    modifier_address TEXT,
    status           TEXT NOT NULL
                         REFERENCES report_status_type(value)
                         DEFAULT 'DRAFT',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    -- The field-report form: single-valued facts about the report, so columns
    -- rather than a second table (§2.1 V6). Issuer free text, never normalized.
    host_name        TEXT,
    participants     TEXT,
    time_taken       TEXT,
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
    CONSTRAINT reports_form_fields_not_blank CHECK (
        (host_name    IS NULL OR btrim(host_name,    E' \t\r\n\f\v') <> '')
        AND (participants IS NULL OR btrim(participants, E' \t\r\n\f\v') <> '')
        AND (time_taken   IS NULL OR btrim(time_taken,   E' \t\r\n\f\v') <> '')
    )
);

CREATE TRIGGER update_reports_timestamp
    BEFORE UPDATE ON reports
    FOR EACH ROW EXECUTE PROCEDURE update_timestamp();

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

CREATE TABLE IF NOT EXISTS report_media_kind (
    value TEXT PRIMARY KEY
);
INSERT INTO report_media_kind (value) VALUES
    ('FILE'),
    ('EMBED')
ON CONFLICT (value) DO NOTHING;

-- One row per image occurrence, extracted from the editor document at write
-- time. Two candidate keys hold: (report_id, ordinal) is document order, and
-- (report_id, url) is unique on the imported catalog — no report references the
-- same image twice. Both are declared, so the relation is in BCNF under either.
CREATE TABLE IF NOT EXISTS report_images (
    report_id  INT  NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
    ordinal    INT  NOT NULL,
    url        TEXT NOT NULL,
    -- The editor's display box. NOT the intrinsic size: 267x1000 display
    -- against 1600x747 intrinsic occurs, so these are not a scaled pair.
    width      INT,
    height     INT,
    max_width  INT,
    max_height INT,
    byte_size  BIGINT,
    caption    TEXT,
    PRIMARY KEY (report_id, ordinal),
    CONSTRAINT report_images_url_unique UNIQUE (report_id, url),
    CONSTRAINT report_images_ordinal_positive CHECK (ordinal > 0),
    -- Storing a base64 data URI here would defeat the point of the table.
    CONSTRAINT report_images_url_http CHECK (url ~* '^https?://'),
    CONSTRAINT report_images_dims_positive CHECK (
        (width      IS NULL OR width      > 0)
        AND (height     IS NULL OR height     > 0)
        AND (max_width  IS NULL OR max_width  > 0)
        AND (max_height IS NULL OR max_height > 0)
    ),
    CONSTRAINT report_images_byte_size_positive
        CHECK (byte_size IS NULL OR byte_size > 0),
    CONSTRAINT report_images_caption_not_blank
        CHECK (caption IS NULL OR btrim(caption, E' \t\r\n\f\v') <> '')
);

-- Uploaded files (PDF, video) and external embeds (YouTube). Separate from
-- report_images because the attributes genuinely differ: attachments have a
-- filename and no dimensions, images the reverse.
CREATE TABLE IF NOT EXISTS report_attachments (
    report_id INT  NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
    ordinal   INT  NOT NULL,
    kind      TEXT NOT NULL REFERENCES report_media_kind(value),
    url       TEXT NOT NULL,
    filename  TEXT,
    byte_size BIGINT,
    PRIMARY KEY (report_id, ordinal),
    CONSTRAINT report_attachments_ordinal_positive CHECK (ordinal > 0),
    CONSTRAINT report_attachments_url_http CHECK (url ~* '^https?://'),
    CONSTRAINT report_attachments_byte_size_positive
        CHECK (byte_size IS NULL OR byte_size > 0),
    CONSTRAINT report_attachments_filename_not_blank
        CHECK (filename IS NULL OR btrim(filename, E' \t\r\n\f\v') <> '')
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

CREATE TRIGGER update_offerings_timestamp
    BEFORE UPDATE ON offerings
    FOR EACH ROW EXECUTE PROCEDURE update_timestamp();

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
