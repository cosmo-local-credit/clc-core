-- Persist optional user location pins and one private attachment per relayed send.

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS location_name TEXT,
    ADD COLUMN IF NOT EXISTS geo_lat DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS geo_lng DOUBLE PRECISION;

ALTER TABLE users
    ADD CONSTRAINT users_location_complete CHECK (
        (location_name IS NULL AND geo_lat IS NULL AND geo_lng IS NULL)
        OR (
            NULLIF(BTRIM(location_name), '') IS NOT NULL
            AND geo_lat IS NOT NULL
            AND geo_lng IS NOT NULL
        )
    ),
    ADD CONSTRAINT users_geo_lat_range CHECK (
        geo_lat IS NULL OR geo_lat BETWEEN -90 AND 90
    ),
    ADD CONSTRAINT users_geo_lng_range CHECK (
        geo_lng IS NULL OR geo_lng BETWEEN -180 AND 180
    );

CREATE TABLE IF NOT EXISTS user_shares (
    id                 BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tracking_id        UUID NOT NULL UNIQUE,
    owner_user_id      INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    authorized_user_id INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    share_permission   INT NOT NULL DEFAULT 0,
    extra_note         TEXT,
    created_at         TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at         TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
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

ALTER TABLE users
    DROP CONSTRAINT IF EXISTS users_geo_lng_range,
    DROP CONSTRAINT IF EXISTS users_geo_lat_range,
    DROP CONSTRAINT IF EXISTS users_location_complete,
    DROP COLUMN IF EXISTS geo_lng,
    DROP COLUMN IF EXISTS geo_lat,
    DROP COLUMN IF EXISTS location_name;
