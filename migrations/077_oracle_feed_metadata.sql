-- Verified oracle feeds carry what a picker needs to show and use them, and a
-- pool token keeps the aggregator its quoter actually maps.
--
-- description, decimals and max_staleness_seconds are optional: the Celo rows
-- predate them and are left NULL rather than guessed. max_staleness_seconds is
-- the per-feed freshness bound sent with setOracle(token, oracle, maxStaleness);
-- NULL means the quoter's global bound applies.
ALTER TABLE oracle_feeds
    ADD COLUMN IF NOT EXISTS description TEXT,
    ADD COLUMN IF NOT EXISTS decimals SMALLINT,
    ADD COLUMN IF NOT EXISTS max_staleness_seconds INT;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'oracle_feeds'::regclass
          AND conname = 'oracle_feeds_metadata_ranges'
    ) THEN
        ALTER TABLE oracle_feeds
            ADD CONSTRAINT oracle_feeds_metadata_ranges
            CHECK ((decimals IS NULL OR decimals BETWEEN 0 AND 255)
               AND (max_staleness_seconds IS NULL OR max_staleness_seconds > 0));
    END IF;
END $$;

-- Gnosis: Chainlink EUR / USD. The feed updates on a 24 h heartbeat, the same
-- as the quoter's default bound, so it is seeded with a 2 h margin.
INSERT INTO oracle_feeds (chain_id, name, address, description, decimals, max_staleness_seconds) VALUES
    (100, 'EUR', '0xab70BCB260073d036d1660201e9d5405F5829b7a', 'EUR / USD', 8, 93600)
ON CONFLICT (chain_id, name) DO UPDATE SET
    address               = EXCLUDED.address,
    description           = EXCLUDED.description,
    decimals              = EXCLUDED.decimals,
    max_staleness_seconds = EXCLUDED.max_staleness_seconds,
    updated_at            = CURRENT_TIMESTAMP;

-- The aggregator a quoter maps, whether or not oracle_feeds can name it. Before
-- this column an unregistered aggregator was stored as no feed at all.
ALTER TABLE pool_tokens
    ADD COLUMN IF NOT EXISTS oracle_address TEXT;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'pool_tokens'::regclass
          AND conname = 'pool_tokens_oracle_address_format'
    ) THEN
        ALTER TABLE pool_tokens
            ADD CONSTRAINT pool_tokens_oracle_address_format
            CHECK (oracle_address IS NULL OR oracle_address ~ '^0x[0-9a-fA-F]{40}$');
    END IF;
END $$;

UPDATE pool_tokens pt
SET oracle_address = f.address
FROM oracle_feeds f
WHERE f.id = pt.oracle_feed_id
  AND pt.oracle_address IS NULL;

---- create above / drop below ----

ALTER TABLE pool_tokens
    DROP CONSTRAINT IF EXISTS pool_tokens_oracle_address_format,
    DROP COLUMN IF EXISTS oracle_address;

DELETE FROM oracle_feeds
WHERE chain_id = 100 AND name = 'EUR'
  AND NOT EXISTS (SELECT 1 FROM pool_tokens WHERE oracle_feed_id = oracle_feeds.id);

ALTER TABLE oracle_feeds
    DROP CONSTRAINT IF EXISTS oracle_feeds_metadata_ranges,
    DROP COLUMN IF EXISTS max_staleness_seconds,
    DROP COLUMN IF EXISTS decimals,
    DROP COLUMN IF EXISTS description;
