CREATE TABLE report_pools (
    report_id BIGINT NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
    pool_id BIGINT NOT NULL REFERENCES pools(id),
    PRIMARY KEY (report_id, pool_id)
);

CREATE INDEX report_pools_pool_report_idx
    ON report_pools (pool_id, report_id);

DROP INDEX IF EXISTS users_recipient_name_trgm_idx;

CREATE INDEX users_recipient_username_exact_idx
    ON users (normalize_identity_name(username))
    WHERE username IS NOT NULL AND status = 'APPROVED';

CREATE INDEX users_recipient_phone_exact_idx
    ON users ((regexp_replace(phone, '[^0-9]', '', 'g')))
    WHERE username IS NOT NULL AND status = 'APPROVED';

---- create above / drop below ----

DROP INDEX IF EXISTS users_recipient_phone_exact_idx;
DROP INDEX IF EXISTS users_recipient_username_exact_idx;

CREATE INDEX users_recipient_name_trgm_idx ON users USING gin
    (normalize_identity_name(username) gin_trgm_ops)
    WHERE username IS NOT NULL AND status = 'APPROVED';

DROP INDEX report_pools_pool_report_idx;
DROP TABLE report_pools;
