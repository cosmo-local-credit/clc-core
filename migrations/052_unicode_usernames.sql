-- 1. Accept display-name text instead of ASCII handles. NULL means no username.
ALTER TABLE users DROP CONSTRAINT users_username_valid;
ALTER TABLE users ADD CONSTRAINT users_username_valid CHECK (
    username IS NULL OR (char_length(username) BETWEEN 1 AND 80 AND username !~ '^\s*$')
);

-- 2. Comparison keys: equivalent Unicode accents share one identity.
-- lower() keeps the existing database-locale case rules. This shared function
-- also serves pool names and symbol claims; display values are not rewritten.
CREATE OR REPLACE FUNCTION normalize_identity_name(value TEXT) RETURNS TEXT
LANGUAGE sql IMMUTABLE STRICT AS $$
    SELECT normalize(lower(regexp_replace(btrim(normalize(value, NFC)), '\s+', ' ', 'g')), NFC);
$$;
-- A canonical collision fails the migration transaction; never choose an owner
-- or silently discard a claim. Existing display spelling remains unchanged.
UPDATE identity_claims SET name = normalize_identity_name(name);
-- 3. Search indexes must use the same expression as recipient searches.
DROP INDEX users_recipient_name_trgm_idx;
DROP INDEX pools_recipient_name_trgm_idx;
CREATE INDEX users_recipient_name_trgm_idx ON users USING gin
    (normalize_identity_name(username) gin_trgm_ops)
    WHERE username IS NOT NULL AND status = 'APPROVED';
CREATE INDEX pools_recipient_name_trgm_idx ON pools USING gin
    (normalize_identity_name(COALESCE(display_name, name)) gin_trgm_ops);

---- create above / drop below ----
-- Rollback refuses incompatible names rather than changing user identities.
ALTER TABLE users DROP CONSTRAINT users_username_valid;
ALTER TABLE users ADD CONSTRAINT users_username_valid CHECK (username IS NULL OR (
    username = lower(btrim(username)) AND char_length(username) BETWEEN 3 AND 24
    AND username ~ '^[a-z0-9][a-z0-9-]*[a-z0-9]$' AND username ~ '[a-z]'));
DROP INDEX users_recipient_name_trgm_idx;
DROP INDEX pools_recipient_name_trgm_idx;
CREATE OR REPLACE FUNCTION normalize_identity_name(value TEXT) RETURNS TEXT
LANGUAGE sql IMMUTABLE STRICT AS $$
    SELECT lower(regexp_replace(btrim(value), '\s+', ' ', 'g'));
$$;
DELETE FROM identity_claims WHERE NOT starts_with(owner_key, 'deploy:');
UPDATE users SET username = username;
UPDATE pools SET display_name = display_name;
UPDATE vouchers SET display_name = display_name;
CREATE INDEX users_recipient_name_trgm_idx ON users USING gin
    (lower(regexp_replace(btrim(username), '\s+', ' ', 'g')) gin_trgm_ops)
    WHERE username IS NOT NULL AND status = 'APPROVED';
CREATE INDEX pools_recipient_name_trgm_idx ON pools USING gin
    (lower(regexp_replace(btrim(COALESCE(display_name, name)), '\s+', ' ', 'g')) gin_trgm_ops);
