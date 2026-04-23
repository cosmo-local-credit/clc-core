-- Extend contract_type enum for pool-related contracts
ALTER TYPE contract_type ADD VALUE IF NOT EXISTS 'TOKEN_UNIQUE_SYMBOL_INDEX';
ALTER TYPE contract_type ADD VALUE IF NOT EXISTS 'LIMITER';
ALTER TYPE contract_type ADD VALUE IF NOT EXISTS 'FEE_POLICY';
ALTER TYPE contract_type ADD VALUE IF NOT EXISTS 'ORACLE_QUOTER';
ALTER TYPE contract_type ADD VALUE IF NOT EXISTS 'RELATIVE_QUOTER';

-- New OTX types for pool deployments
INSERT INTO otx_tx_type (value) VALUES
    ('POOL_DEPLOY_STEP'),
    ('POOL_PROXY_DEPLOY');

-- Seed platform contracts for pool dependencies (Celo mainnet, chain 42220)
INSERT INTO platform_contracts (chain_id, contract_type, factory_address, implementation_address, version) VALUES
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
