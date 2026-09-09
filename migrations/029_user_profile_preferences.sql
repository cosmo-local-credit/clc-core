-- Profile details collected during onboarding. These remain nullable so existing
-- accounts and clients released before this migration continue to work.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS preferred_currency CHAR(3),
    ADD COLUMN IF NOT EXISTS bio TEXT,
    ADD COLUMN IF NOT EXISTS website TEXT;

ALTER TABLE users
    DROP CONSTRAINT IF EXISTS users_preferred_currency_format,
    DROP CONSTRAINT IF EXISTS users_bio_length,
    DROP CONSTRAINT IF EXISTS users_website_http;

ALTER TABLE users
    ADD CONSTRAINT users_preferred_currency_format
        CHECK (preferred_currency IS NULL OR preferred_currency ~ '^[A-Z]{3}$'),
    ADD CONSTRAINT users_bio_length
        CHECK (bio IS NULL OR char_length(bio) <= 500),
    ADD CONSTRAINT users_website_http
        CHECK (website IS NULL OR website ~* '^https?://');
