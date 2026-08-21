-- Make an offering writable.
--
-- Until now offerings were import-only: the Sarafu catalog wrote them and the API
-- read them. Adding create/update/delete means the two columns a caller now
-- chooses -- `unit` and `image_url` -- need to be constrained values rather than
-- whatever the importer happened to carry.
--
-- `commodity_type` deliberately does not change. GOOD and SERVICE stay the only
-- classifications, and the write form asks for one explicitly, so the eight
-- market kind derivations (seven statements in core_queries.sql, one in
-- internal/store/market_voucher_search.go) keep their meaning: the CASE's ELSE
-- branch still means "this voucher has no available offerings" and nothing else.

-- The units a write may choose. A lookup table rather than an enum, matching
-- commodity_type and offering_frequency beside it, so adding a unit is an INSERT
-- and not an ALTER TYPE that cannot run in a transaction with other DDL.
CREATE TABLE IF NOT EXISTS offering_unit (
    value TEXT PRIMARY KEY
);
INSERT INTO offering_unit (value) VALUES
    ('kg'),
    ('g'),
    ('litre'),
    ('item'),
    ('hour'),
    ('day'),
    ('bag'),
    ('unit'),
    ('bundle'),
    ('piece'),
    ('dozen'),
    ('serving'),
    ('trip'),
    ('session'),
    ('metre')
ON CONFLICT (value) DO NOTHING;

-- NOT VALID, because imported rows carry free text: "50kg sack", "sack", "per
-- bunch". Coercing those to a canonical value would invent data the importer
-- never had, and dropping them would lose what the seller actually wrote. They
-- stay readable; the constraint applies to every new and changed unit.
--
-- Safe for the soft delete this feature adds. A foreign key's UPDATE trigger is
-- registered against its referencing columns and short-circuits when the key is
-- unchanged, so `SET available = false` on a row whose legacy unit is not in the
-- lookup does not re-check it. That is the opposite of a CHECK constraint --
-- see below.
ALTER TABLE offerings
    DROP CONSTRAINT IF EXISTS offerings_unit_fk;

ALTER TABLE offerings
    ADD CONSTRAINT offerings_unit_fk
        FOREIGN KEY (unit) REFERENCES offering_unit(value)
        NOT VALID;

-- offerings.image_url is the one catalog image column migration 022 left without
-- a scheme constraint, and it cannot be closed the way that migration closed the
-- others.
--
-- 022 used NOT VALID on vouchers, pools and reports to grandfather rows holding
-- base64 data URIs. That works only for a column nothing updates. PostgreSQL
-- re-evaluates a CHECK constraint on every UPDATE of the row regardless of which
-- column changed, so a grandfathered data URI here would make an unrelated price
-- edit -- or the `SET available = false` that unlists an offering -- fail with a
-- constraint violation the caller cannot fix.
--
-- So the legacy values are cleared first and the constraint is added validated.
-- The photo is optional everywhere it appears and an embedded data URI is not a
-- durable bucket URL, so an offering that cannot be edited or unlisted is the
-- worse outcome. Run the count below before deploying to record what this clears:
--
--   SELECT count(*) FROM offerings
--   WHERE image_url IS NOT NULL AND image_url !~* '^https?://';
UPDATE offerings
SET image_url = NULL
WHERE image_url IS NOT NULL
  AND image_url !~* '^https?://';

ALTER TABLE offerings
    DROP CONSTRAINT IF EXISTS offerings_image_url_http;

ALTER TABLE offerings
    ADD CONSTRAINT offerings_image_url_http
        CHECK (image_url IS NULL OR image_url ~* '^https?://');
