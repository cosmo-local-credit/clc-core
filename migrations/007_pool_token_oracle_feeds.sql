-- Pool pricing is oracle-backed. Fixed exchange rates belonged to the legacy
-- RelativeQuoter model and are no longer part of the pool composition cache.
ALTER TABLE pool_tokens
    DROP CONSTRAINT IF EXISTS pool_tokens_rate_nonneg,
    DROP COLUMN IF EXISTS exchange_rate;

ALTER TABLE pool_tokens
    ADD COLUMN IF NOT EXISTS oracle_feed_id INT;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'pool_tokens'::regclass
          AND conname = 'pool_tokens_oracle_feed_id_fkey'
    ) THEN
        ALTER TABLE pool_tokens
            ADD CONSTRAINT pool_tokens_oracle_feed_id_fkey
            FOREIGN KEY (oracle_feed_id) REFERENCES oracle_feeds(id);
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS pool_tokens_oracle_feed_id_idx
    ON pool_tokens (oracle_feed_id);
