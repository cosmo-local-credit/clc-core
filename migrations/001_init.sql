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
    ('TOKEN_PROXY_DEPLOY');

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
    ('UNKNOWN_RPC_ERROR');

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
CREATE TYPE contract_type AS ENUM ('ERC20', 'ERC20_DEMURRAGE', 'SWAPPOOL');

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

-- Seed: ERC20 platform contract (Celo mainnet, chain 42220)
INSERT INTO platform_contracts (chain_id, contract_type, factory_address, implementation_address, version) VALUES
    (42220, 'ERC20', '0x3e302C5965954D84Ab3dac664C6152b54D7daa00', '0x1F74298ff3CCF718c50c557d2b9a54040F295012', 'v0.5.5')
ON CONFLICT (chain_id, contract_type) DO UPDATE SET
    factory_address        = EXCLUDED.factory_address,
    implementation_address = EXCLUDED.implementation_address,
    version                = EXCLUDED.version,
    updated_at             = CURRENT_TIMESTAMP;
