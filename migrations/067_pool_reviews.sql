CREATE TABLE pool_reviews (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    pool_id INT NOT NULL REFERENCES pools(id),
    reviewer_address TEXT NOT NULL CHECK (reviewer_address ~ '^0x[0-9a-f]{40}$'),
    stars SMALLINT NOT NULL CHECK (stars BETWEEN 1 AND 5),
    comment TEXT NOT NULL DEFAULT '' CHECK (char_length(comment) <= 2000),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    hidden BOOLEAN NOT NULL DEFAULT false,
    reply TEXT CHECK (char_length(reply) BETWEEN 1 AND 2000),
    reply_author_address TEXT CHECK (reply_author_address ~ '^0x[0-9a-f]{40}$'),
    reply_updated_at TIMESTAMPTZ,
    UNIQUE (pool_id, reviewer_address),
    CHECK ((reply IS NULL) = (reply_author_address IS NULL))
);

CREATE INDEX pool_reviews_public_idx ON pool_reviews (pool_id, id DESC) WHERE NOT hidden;

-- Keep one durable moderation inbox and one globally unique notification ID.
-- Existing voucher rows continue using review_id; pool rows use pool_review_id.
ALTER TABLE voucher_review_flags
    ADD COLUMN pool_review_id BIGINT REFERENCES pool_reviews(id),
    ALTER COLUMN review_id DROP NOT NULL;

ALTER TABLE voucher_review_flags
    DROP CONSTRAINT voucher_review_flags_review_id_reporter_address_key,
    ADD CONSTRAINT voucher_review_flags_one_review
        CHECK ((review_id IS NOT NULL)::INT + (pool_review_id IS NOT NULL)::INT = 1);

CREATE UNIQUE INDEX voucher_review_flags_voucher_reporter_idx
    ON voucher_review_flags (review_id, reporter_address)
    WHERE review_id IS NOT NULL;

CREATE UNIQUE INDEX voucher_review_flags_pool_reporter_idx
    ON voucher_review_flags (pool_review_id, reporter_address)
    WHERE pool_review_id IS NOT NULL;
