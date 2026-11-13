-- Grouped receipts for routed exchanges (ROUTER.md §7.3).
--
-- When the relay accepts a batch that decodes as a swap route, it records the
-- route in the same transaction as the job, from the signed calldata alone.
-- The subscriber later fills each hop from its settlement event, matched by
-- the relayed transaction's tracking id, so a two-hop exchange reads as one
-- receipt rather than two unrelated swaps, on any device.
CREATE TABLE IF NOT EXISTS routed_swaps (
    tracking_id    UUID        PRIMARY KEY,
    chain_id       INT         NOT NULL,
    wallet         TEXT        NOT NULL CHECK (wallet ~ '^0x[0-9a-fA-F]{40}$'),
    recipient      TEXT        NOT NULL CHECK (recipient ~ '^0x[0-9a-fA-F]{40}$'),
    token_in       TEXT        NOT NULL CHECK (token_in ~ '^0x[0-9a-fA-F]{40}$'),
    token_out      TEXT        NOT NULL CHECK (token_out ~ '^0x[0-9a-fA-F]{40}$'),
    amount_in      NUMERIC     NOT NULL CHECK (amount_in > 0),
    min_amount_out NUMERIC     NOT NULL CHECK (min_amount_out >= 0),
    hop_count      SMALLINT    NOT NULL CHECK (hop_count BETWEEN 1 AND 5),
    -- True when every hop used the bounded withdraw overload.
    bounded        BOOLEAN     NOT NULL,
    deadline       BIGINT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS routed_swaps_wallet_created_idx
    ON routed_swaps (lower(wallet), created_at DESC, tracking_id DESC);

CREATE TABLE IF NOT EXISTS routed_swap_hops (
    tracking_id        UUID        NOT NULL REFERENCES routed_swaps(tracking_id) ON DELETE CASCADE,
    hop_index          SMALLINT    NOT NULL CHECK (hop_index >= 0),
    pool_address       TEXT        NOT NULL CHECK (pool_address ~ '^0x[0-9a-fA-F]{40}$'),
    token_in           TEXT        NOT NULL CHECK (token_in ~ '^0x[0-9a-fA-F]{40}$'),
    token_out          TEXT        NOT NULL CHECK (token_out ~ '^0x[0-9a-fA-F]{40}$'),
    amount_in          NUMERIC     NOT NULL CHECK (amount_in > 0),
    min_amount_out     NUMERIC     NOT NULL CHECK (min_amount_out >= 0),
    recipient          TEXT        NOT NULL CHECK (recipient ~ '^0x[0-9a-fA-F]{40}$'),
    -- Settlement, from the pool's SwapSettlement (or legacy Swap) event.
    tx_hash            TEXT,
    block_number       BIGINT,
    log_index          INT,
    settled_amount_in  NUMERIC,
    quoted_amount_out  NUMERIC,
    nominal_amount_out NUMERIC,
    amount_out         NUMERIC,
    pool_fee           NUMERIC,
    protocol_fee       NUMERIC,
    settled_at         TIMESTAMPTZ,
    PRIMARY KEY (tracking_id, hop_index)
);

---- create above / drop below ----

DROP TABLE IF EXISTS routed_swap_hops;
DROP TABLE IF EXISTS routed_swaps;
