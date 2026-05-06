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
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS public_key_idx ON keystore(public_key);

CREATE TRIGGER insert_starting_nonce
    AFTER INSERT ON keystore
    FOR EACH ROW EXECUTE PROCEDURE insert_starting_nonce();

CREATE TRIGGER update_keystore_timestamp
    BEFORE UPDATE ON keystore
    FOR EACH ROW EXECUTE PROCEDURE update_timestamp();

-- Master key
CREATE TABLE IF NOT EXISTS master_key (
    id         INT PRIMARY KEY,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TRIGGER update_master_key_timestamp
    BEFORE UPDATE ON master_key
    FOR EACH ROW EXECUTE PROCEDURE update_timestamp();

-- Noncestore
CREATE TABLE IF NOT EXISTS noncestore (
    keystore_id INT PRIMARY KEY REFERENCES keystore(id),
    next_nonce  INT DEFAULT 0,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
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
    ('PROXY_UPGRADE')
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
    nonce          INT NOT NULL,
    replaced       BOOLEAN DEFAULT false,
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Dispatch
CREATE TABLE IF NOT EXISTS dispatch (
    id         INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    otx_id     INT REFERENCES otx(id),
    status     TEXT REFERENCES dispatch_status_type(value) NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
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

-- Platform contracts
CREATE TABLE IF NOT EXISTS platform_contracts (
    id                      INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    chain_id                INT NOT NULL,
    contract_type           contract_type NOT NULL,
    factory_address         TEXT NOT NULL,
    implementation_address  TEXT NOT NULL,
    version                 TEXT NOT NULL DEFAULT '',
    created_at              TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at              TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (chain_id, contract_type)
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
    created_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (otx_id),
    UNIQUE (address)
);

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
    created_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TRIGGER update_token_deploy_meta_timestamp
    BEFORE UPDATE ON token_deploy_meta
    FOR EACH ROW EXECUTE PROCEDURE update_timestamp();

-- Pool addresses: shared singleton addresses for protocol-level contracts
CREATE TABLE IF NOT EXISTS pool_addresses (
    id         INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    key        TEXT NOT NULL UNIQUE,
    address    TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TRIGGER update_pool_addresses_timestamp
    BEFORE UPDATE ON pool_addresses
    FOR EACH ROW EXECUTE PROCEDURE update_timestamp();

INSERT INTO pool_addresses (key, address) VALUES
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
    created_at                      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at                      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TRIGGER update_deployed_pools_timestamp
    BEFORE UPDATE ON deployed_pools
    FOR EACH ROW EXECUTE PROCEDURE update_timestamp();

-- Chainlink oracle feed registry: stores known oracle feed addresses for price feeds
CREATE TABLE IF NOT EXISTS oracle_feeds (
    id         INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    address    TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
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
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ethereum_address TEXT UNIQUE NOT NULL,
    name             TEXT NOT NULL,
    email            TEXT UNIQUE NOT NULL,
    role             user_role NOT NULL DEFAULT 'USER',
    status           user_status NOT NULL DEFAULT 'PENDING',
    max_pools        INT NOT NULL DEFAULT 10,
    max_tokens       INT NOT NULL DEFAULT 20,
    created_at       TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at       TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS users_ethereum_address_idx ON users(ethereum_address);
CREATE INDEX IF NOT EXISTS users_status_idx ON users(status);

CREATE TRIGGER update_users_timestamp
    BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE PROCEDURE update_timestamp();

-- User deployments: links a user to a tracked on-chain resource (token or pool)
CREATE TABLE IF NOT EXISTS user_deployments (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tracking_id   UUID NOT NULL UNIQUE,
    resource_type TEXT NOT NULL CHECK (resource_type IN ('TOKEN', 'POOL')),
    created_at    TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS user_deployments_user_id_idx ON user_deployments(user_id);
CREATE INDEX IF NOT EXISTS user_deployments_tracking_id_idx ON user_deployments(tracking_id);

-- Seed superadmin accounts
INSERT INTO users (ethereum_address, name, email, role, status) VALUES
    ('0x33a573149db22e759fb9a38bcc461c12855c2645', 'Superadmin', 'devops@grassecon.org', 'SUPERADMIN', 'APPROVED')
ON CONFLICT (ethereum_address) DO NOTHING;
