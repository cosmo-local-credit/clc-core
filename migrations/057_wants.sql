-- Wants are off-chain requests published by a user.  They deliberately keep
-- contact permissions rather than copies of profile PII, so an owner can revoke
-- or change a contact value in one place.
ALTER TABLE users
    ADD COLUMN max_wants INTEGER NOT NULL DEFAULT 3
    CONSTRAINT users_max_wants_nonnegative CHECK (max_wants >= 0);

CREATE TABLE want_marker_icon (
    value TEXT PRIMARY KEY,
    CONSTRAINT want_marker_icon_value_valid CHECK (value ~ '^[a-z][a-z-]{0,31}$')
);

INSERT INTO want_marker_icon (value) VALUES
    ('question'),
    ('message'),
    ('request');

CREATE TABLE wants (
    id                 BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    owner_user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name               TEXT NOT NULL,
    description        TEXT NOT NULL,
    location_name      TEXT NOT NULL,
    geo_lat            DOUBLE PRECISION NOT NULL,
    geo_lng            DOUBLE PRECISION NOT NULL,
    starts_at          TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at         TIMESTAMPTZ,
    status             TEXT NOT NULL DEFAULT 'OPEN',
    image_url          TEXT,
    contact_permission INTEGER NOT NULL DEFAULT 0,
    contact_text       TEXT,
    marker_icon        TEXT NOT NULL DEFAULT 'question' REFERENCES want_marker_icon(value),
    marker_color       TEXT NOT NULL DEFAULT 'orange' REFERENCES voucher_marker_color(value),
    created_at         TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT wants_name_length CHECK (CHAR_LENGTH(BTRIM(name)) BETWEEN 1 AND 120),
    CONSTRAINT wants_description_length CHECK (CHAR_LENGTH(BTRIM(description)) BETWEEN 1 AND 2000),
    CONSTRAINT wants_location_name_present CHECK (NULLIF(BTRIM(location_name), '') IS NOT NULL),
    CONSTRAINT wants_geo_lat_valid CHECK (geo_lat BETWEEN -90 AND 90),
    CONSTRAINT wants_geo_lng_valid CHECK (geo_lng BETWEEN -180 AND 180),
    CONSTRAINT wants_expiry_after_start CHECK (expires_at IS NULL OR expires_at > starts_at),
    CONSTRAINT wants_status_valid CHECK (status IN ('OPEN', 'CLOSED')),
    CONSTRAINT wants_image_url_http CHECK (image_url IS NULL OR image_url ~ '^https?://'),
    CONSTRAINT wants_contact_permissions_known CHECK (
        contact_permission >= 0 AND (contact_permission & 3) = contact_permission
    ),
    CONSTRAINT wants_contact_text_length CHECK (
        contact_text IS NULL OR CHAR_LENGTH(BTRIM(contact_text)) BETWEEN 1 AND 500
    ),
    CONSTRAINT wants_has_contact CHECK (
        contact_permission <> 0 OR NULLIF(BTRIM(contact_text), '') IS NOT NULL
    )
);

CREATE INDEX wants_owner_page_idx ON wants(owner_user_id, id DESC);
CREATE INDEX wants_public_page_idx ON wants(id DESC) WHERE status = 'OPEN';
CREATE INDEX wants_location_idx ON wants(geo_lat, geo_lng);

CREATE TABLE want_tags (
    want_id BIGINT NOT NULL REFERENCES wants(id) ON DELETE CASCADE,
    tag_value TEXT NOT NULL REFERENCES token_tag(value) ON UPDATE CASCADE,
    PRIMARY KEY (want_id, tag_value)
);

CREATE INDEX want_tags_tag_want_idx ON want_tags(tag_value, want_id);

CREATE TRIGGER update_wants_timestamp
    BEFORE UPDATE ON wants
    FOR EACH ROW EXECUTE PROCEDURE update_timestamp();

---- create above / drop below ----

DROP TABLE IF EXISTS want_tags;
DROP TABLE IF EXISTS wants;
DROP TABLE IF EXISTS want_marker_icon;
ALTER TABLE users DROP COLUMN IF EXISTS max_wants;
