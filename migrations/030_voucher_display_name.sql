-- A voucher's ERC20 name is immutable once deployed, but its public catalog
-- label is not. Keep the override separate so a corrected label can never
-- change the contract snapshot or be overwritten by deploy promotion.
ALTER TABLE vouchers
    ADD COLUMN IF NOT EXISTS display_name TEXT;

ALTER TABLE vouchers
    DROP CONSTRAINT IF EXISTS vouchers_display_name_valid;

ALTER TABLE vouchers
    ADD CONSTRAINT vouchers_display_name_valid
        CHECK (display_name IS NULL
            OR (display_name = btrim(display_name)
                AND char_length(display_name) BETWEEN 1 AND 120));

-- NULL deliberately means "show the immutable contract name". It avoids a
-- full-table backfill and leaves imports and previously confirmed deployments
-- with exactly their existing label.

-- Catalog search follows the label users see, not a stale deployment typo.
DROP INDEX IF EXISTS vouchers_active_name_trgm_idx;

CREATE INDEX IF NOT EXISTS vouchers_active_display_name_trgm_idx
    ON vouchers USING gin (lower(COALESCE(display_name, name)) gin_trgm_ops)
    WHERE active;

ANALYZE vouchers;
