-- Persist one private attachment per relayed send. User location columns are
-- supplied by 014_user_geo.sql.

CREATE TABLE IF NOT EXISTS user_shares (
    id                 BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tracking_id        UUID NOT NULL UNIQUE,
    owner_user_id      INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    authorized_user_id INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    share_permission   INT NOT NULL DEFAULT 0,
    extra_note         TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT user_shares_distinct_users CHECK (owner_user_id <> authorized_user_id),
    CONSTRAINT user_shares_known_permissions CHECK (
        share_permission >= 0 AND (share_permission & 7) = share_permission
    ),
    CONSTRAINT user_shares_has_content CHECK (
        share_permission <> 0 OR NULLIF(BTRIM(extra_note), '') IS NOT NULL
    ),
    CONSTRAINT user_shares_note_length CHECK (
        extra_note IS NULL OR CHAR_LENGTH(extra_note) <= 500
    )
);

CREATE INDEX IF NOT EXISTS user_shares_owner_user_id_idx
    ON user_shares(owner_user_id);
CREATE INDEX IF NOT EXISTS user_shares_authorized_user_id_idx
    ON user_shares(authorized_user_id);

CREATE TRIGGER update_user_shares_timestamp
    BEFORE UPDATE ON user_shares
    FOR EACH ROW EXECUTE PROCEDURE update_timestamp();

---- create above / drop below ----

DROP TABLE IF EXISTS user_shares;
