-- Let authenticated catalog writers extend the two shared vocabularies while
-- keeping the values shipped by migrations protected from admin cleanup.

ALTER TABLE token_tag
    ADD COLUMN IF NOT EXISTS is_custom BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE offering_unit
    ADD COLUMN IF NOT EXISTS is_custom BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE token_tag
    DROP CONSTRAINT IF EXISTS token_tag_value_valid;
ALTER TABLE token_tag
    ADD CONSTRAINT token_tag_value_valid CHECK (
        value = btrim(value)
        AND value <> ''
        AND char_length(value) <= 100
        AND strpos(value, ',') = 0
    );

ALTER TABLE offering_unit
    DROP CONSTRAINT IF EXISTS offering_unit_value_valid;
ALTER TABLE offering_unit
    ADD CONSTRAINT offering_unit_value_valid CHECK (
        value = btrim(value)
        AND value <> ''
        AND char_length(value) <= 32
    );

-- Canonicalisation happens in the API, but these indexes close the race where
-- two requests submit differently-cased spellings at the same time.
CREATE UNIQUE INDEX IF NOT EXISTS token_tag_value_lower_idx
    ON token_tag (lower(value));
CREATE UNIQUE INDEX IF NOT EXISTS offering_unit_value_lower_idx
    ON offering_unit (lower(value));
