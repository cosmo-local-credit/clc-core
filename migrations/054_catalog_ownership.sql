-- Ordered chain ownership facts, retained even when deployment promotion has
-- not created the catalog row yet. No profile/provenance data lives here.
CREATE TABLE catalog_ownership (
    chain_id BIGINT NOT NULL,
    address TEXT NOT NULL CHECK (address = lower(address)),
    owner_address TEXT NOT NULL,
    block_number BIGINT NOT NULL CHECK (block_number >= 0),
    log_index BIGINT NOT NULL CHECK (log_index >= 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (chain_id, address)
);
