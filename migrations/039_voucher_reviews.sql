CREATE TABLE voucher_reviews (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    voucher_id INT NOT NULL REFERENCES vouchers(id),
    reviewer_address TEXT NOT NULL CHECK (reviewer_address ~ '^0x[0-9a-f]{40}$'),
    stars SMALLINT NOT NULL CHECK (stars BETWEEN 1 AND 5),
    comment TEXT NOT NULL DEFAULT '' CHECK (char_length(comment) <= 2000),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    hidden BOOLEAN NOT NULL DEFAULT false,
    reply TEXT CHECK (char_length(reply) BETWEEN 1 AND 2000),
    reply_author_address TEXT CHECK (reply_author_address ~ '^0x[0-9a-f]{40}$'),
    reply_updated_at TIMESTAMPTZ,
    UNIQUE (voucher_id, reviewer_address),
    CHECK ((reply IS NULL) = (reply_author_address IS NULL))
);
CREATE INDEX voucher_reviews_public_idx ON voucher_reviews (voucher_id, id DESC) WHERE NOT hidden;

-- The durable admin inbox. Creating a flag and its notification is one write;
-- resolving it preserves the original report and content for moderation.
CREATE TABLE voucher_review_flags (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    review_id BIGINT NOT NULL REFERENCES voucher_reviews(id),
    reporter_address TEXT NOT NULL CHECK (reporter_address ~ '^0x[0-9a-f]{40}$'),
    reason TEXT NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 500),
    reported_stars SMALLINT NOT NULL,
    reported_comment TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resolved_at TIMESTAMPTZ,
    resolved_by TEXT,
    UNIQUE (review_id, reporter_address)
);
CREATE INDEX voucher_review_flags_pending_idx ON voucher_review_flags (id DESC) WHERE resolved_at IS NULL;
