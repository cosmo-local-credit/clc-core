-- Scope curated oracle feeds to the chain where their aggregator is deployed.
--
-- The original registry was Celo-only and made `name` globally unique. Carry
-- those rows forward as Celo mainnet, then key names and address lookups by
-- chain so moving the service to another network is a data change rather than
-- a client release.

ALTER TABLE oracle_feeds
    ADD COLUMN IF NOT EXISTS chain_id BIGINT;

UPDATE oracle_feeds
SET chain_id = 42220
WHERE chain_id IS NULL;

ALTER TABLE oracle_feeds
    ALTER COLUMN chain_id SET NOT NULL;

ALTER TABLE oracle_feeds
    DROP CONSTRAINT IF EXISTS oracle_feeds_name_key;

ALTER TABLE oracle_feeds
    DROP CONSTRAINT IF EXISTS oracle_feeds_chain_name_unique;

ALTER TABLE oracle_feeds
    ADD CONSTRAINT oracle_feeds_chain_name_unique UNIQUE (chain_id, name);

ALTER TABLE oracle_feeds
    DROP CONSTRAINT IF EXISTS oracle_feeds_chain_id_positive;

ALTER TABLE oracle_feeds
    ADD CONSTRAINT oracle_feeds_chain_id_positive CHECK (chain_id > 0);

CREATE INDEX IF NOT EXISTS oracle_feeds_chain_address_idx
    ON oracle_feeds (chain_id, lower(address));

-- Correct the two legacy aliases whose Celo rows pointed at another feed, and
-- add the native EUR/USD feed published by Chainlink. KES/USD follows the Celo
-- setup documented by clc-protocol.
UPDATE oracle_feeds
SET address = CASE name
    WHEN 'EURC' THEN '0x9a48d9b0AF457eF040281A9Af3867bc65522Fecd'
    WHEN 'KES'  THEN '0x0826492a24b1dBd1d8fcB4701b38C557CE685e9D'
    ELSE address
END,
updated_at = CURRENT_TIMESTAMP
WHERE chain_id = 42220 AND name IN ('EURC', 'KES');

INSERT INTO oracle_feeds (chain_id, name, address) VALUES
    (42220, 'EUR', '0x3D207061Dbe8E2473527611BFecB87Ff12b28dDa')
ON CONFLICT (chain_id, name) DO UPDATE SET
    address    = EXCLUDED.address,
    updated_at = CURRENT_TIMESTAMP;
