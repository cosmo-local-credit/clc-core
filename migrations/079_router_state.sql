-- Router state (ROUTER.md §4.1).
--
-- price_index is the RelativeQuoter price index, projected like token_limit.
-- NULL means never heard; 0 is a real value and means parity on chain.
--
-- Every projected field now records the position of its last write, (block,
-- log index), and a projection applies only when it is newer. A NATS message
-- that failed is redelivered after newer ones, so without this an older
-- LIMIT_SET could overwrite a newer one. A chain read at block N writes
-- (N, 2147483647): it already includes every event in N.
ALTER TABLE pool_tokens
    ADD COLUMN IF NOT EXISTS price_index NUMERIC,
    ADD COLUMN IF NOT EXISTS allowed_block BIGINT,
    ADD COLUMN IF NOT EXISTS allowed_log_index INT,
    ADD COLUMN IF NOT EXISTS limit_block BIGINT,
    ADD COLUMN IF NOT EXISTS limit_log_index INT,
    ADD COLUMN IF NOT EXISTS price_block BIGINT,
    ADD COLUMN IF NOT EXISTS price_log_index INT,
    ADD COLUMN IF NOT EXISTS oracle_block BIGINT,
    ADD COLUMN IF NOT EXISTS oracle_log_index INT;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'pool_tokens'::regclass
          AND conname = 'pool_tokens_price_index_nonneg'
    ) THEN
        ALTER TABLE pool_tokens
            ADD CONSTRAINT pool_tokens_price_index_nonneg
            CHECK (price_index IS NULL OR price_index >= 0);
    END IF;
END $$;

-- Chain-read balances. A separate table because the writer (the reconciler),
-- the churn (every swap) and the guard (block number) all differ from the
-- event-projected columns of pool_tokens.
CREATE TABLE IF NOT EXISTS pool_token_liquidity (
    pool_id      INT         NOT NULL REFERENCES pools(id) ON DELETE CASCADE,
    voucher_id   INT         NOT NULL REFERENCES vouchers(id),
    balance      NUMERIC     NOT NULL CHECK (balance >= 0),
    fees_held    NUMERIC     NOT NULL DEFAULT 0 CHECK (fees_held >= 0),
    block_number BIGINT      NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (pool_id, voucher_id)
);

-- Pool parameters that emit no tracked event: FeePolicy's default, the
-- protocol fee controller's fee and recipient, the seal state, and whether
-- the pool's quoter matched the local arithmetic at the last refresh.
CREATE TABLE IF NOT EXISTS pool_chain_state (
    pool_id                INT         PRIMARY KEY REFERENCES pools(id) ON DELETE CASCADE,
    default_fee_ppm        NUMERIC     NOT NULL CHECK (default_fee_ppm BETWEEN 0 AND 1000000),
    -- 0 when the controller is unset, inactive, set to zero or has no recipient.
    protocol_fee_ppm       NUMERIC     NOT NULL CHECK (protocol_fee_ppm BETWEEN 0 AND 1000000),
    protocol_fee_recipient TEXT        CHECK (protocol_fee_recipient IS NULL
                                           OR protocol_fee_recipient ~ '^0x[0-9a-fA-F]{40}$'),
    seal_state             SMALLINT    NOT NULL,
    quoter_verified        BOOLEAN     NOT NULL DEFAULT FALSE,
    block_number           BIGINT      NOT NULL,
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

---- create above / drop below ----

DROP TABLE IF EXISTS pool_chain_state;
DROP TABLE IF EXISTS pool_token_liquidity;

ALTER TABLE pool_tokens
    DROP CONSTRAINT IF EXISTS pool_tokens_price_index_nonneg,
    DROP COLUMN IF EXISTS oracle_log_index,
    DROP COLUMN IF EXISTS oracle_block,
    DROP COLUMN IF EXISTS price_log_index,
    DROP COLUMN IF EXISTS price_block,
    DROP COLUMN IF EXISTS limit_log_index,
    DROP COLUMN IF EXISTS limit_block,
    DROP COLUMN IF EXISTS allowed_log_index,
    DROP COLUMN IF EXISTS allowed_block,
    DROP COLUMN IF EXISTS price_index;
