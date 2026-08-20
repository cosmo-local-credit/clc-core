-- Somewhere to keep the image URLs an upload produces.
--
-- Bytes stay out of this database: storage-server holds the object and answers
-- with an immutable, content-addressed URL, and these columns hold that URL. All
-- additive and all nullable — an image is optional everywhere it appears, and a
-- reader that does not select these columns is unaffected.
--
-- Three gaps are closed here:
--
--   * users has no avatar column at all, so a profile picture has nowhere to go;
--   * a token deploy carries name, description, geo and tags through
--     deployed_tokens -> vouchers, but not the icon or banner the creator chose,
--     so those were dropped at confirmation while the catalog columns that
--     should receive them already existed;
--   * pools and vouchers already have their image columns, but none of the image
--     columns in this schema have a scheme constraint, so nothing stops a base64
--     data URI being written where a URL belongs — migration 010 found 6 570 of
--     them in report bodies.

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS avatar_url TEXT;

ALTER TABLE users
    DROP CONSTRAINT IF EXISTS users_avatar_url_http;

-- Validated immediately: the column is new, so there is nothing to audit.
ALTER TABLE users
    ADD CONSTRAINT users_avatar_url_http
        CHECK (avatar_url IS NULL OR avatar_url ~* '^https?://');

-- The deploy pipeline. deployed_tokens is the pending/confirmed row that records
-- what was requested, and upsert-voucher-from-deploy promotes it into the catalog
-- once an address exists. There is no separate request table to extend:
-- token_deploy_meta was folded into deployed_tokens and dropped by migration 002.
ALTER TABLE deployed_tokens
    ADD COLUMN IF NOT EXISTS icon_url TEXT;

ALTER TABLE deployed_tokens
    ADD COLUMN IF NOT EXISTS banner_url TEXT;

ALTER TABLE deployed_tokens
    DROP CONSTRAINT IF EXISTS deployed_tokens_image_urls_http;

ALTER TABLE deployed_tokens
    ADD CONSTRAINT deployed_tokens_image_urls_http
        CHECK ((icon_url IS NULL OR icon_url ~* '^https?://')
            AND (banner_url IS NULL OR banner_url ~* '^https?://'));

-- Existing catalog columns, added NOT VALID.
--
-- Imported rows are not audited here, and one of these columns is known to hold
-- data URIs: six approved reports still carry a base64 cover of up to 876 kB,
-- which the feed queries null at read time on purpose (report_search.go, and the
-- test named for it). A validated constraint would fail on those rows, and
-- rewriting them belongs to the image re-host, not to this migration.
--
-- NOT VALID still enforces the check on every insert and update, which is what
-- this needs: no new write can put a data URI in a URL column. Leaving it
-- permanently unvalidated is an acceptable end state; VALIDATE CONSTRAINT can be
-- run later, per table, once the legacy rows are dealt with.
ALTER TABLE vouchers
    DROP CONSTRAINT IF EXISTS vouchers_image_urls_http;

ALTER TABLE vouchers
    ADD CONSTRAINT vouchers_image_urls_http
        CHECK ((icon_url IS NULL OR icon_url ~* '^https?://')
            AND (banner_url IS NULL OR banner_url ~* '^https?://'))
        NOT VALID;

ALTER TABLE pools
    DROP CONSTRAINT IF EXISTS pools_image_urls_http;

ALTER TABLE pools
    ADD CONSTRAINT pools_image_urls_http
        CHECK ((image_url IS NULL OR image_url ~* '^https?://')
            AND (banner_url IS NULL OR banner_url ~* '^https?://'))
        NOT VALID;

ALTER TABLE reports
    DROP CONSTRAINT IF EXISTS reports_image_url_http;

ALTER TABLE reports
    ADD CONSTRAINT reports_image_url_http
        CHECK (image_url IS NULL OR image_url ~* '^https?://')
        NOT VALID;
