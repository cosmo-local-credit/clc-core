-- A SwapPool name is immutable once deployed, but its public catalog label is
-- not. Keep the override separate so an edit cannot falsify the chain snapshot
-- or be overwritten by a later deploy promotion.
ALTER TABLE pools
    ADD COLUMN IF NOT EXISTS display_name TEXT;

ALTER TABLE pools
    DROP CONSTRAINT IF EXISTS pools_display_name_valid;

ALTER TABLE pools
    ADD CONSTRAINT pools_display_name_valid
        CHECK (display_name IS NULL
            OR (display_name = btrim(display_name)
                AND char_length(display_name) BETWEEN 1 AND 80));

-- Search follows the effective label shown by the catalog.
DROP INDEX IF EXISTS pools_name_trgm_idx;

CREATE INDEX IF NOT EXISTS pools_display_name_trgm_idx
    ON pools USING gin (lower(COALESCE(display_name, name)) gin_trgm_ops);

ANALYZE pools;
