-- What a pool charges for a swap and where it pays it, recorded on the deploy
-- row so the catalog promotion has something to copy.
--
-- pools.fee_ppm and pools.fee_address have existed since the baseline but were
-- only ever written by the federation importer, so every pool published through
-- this service read back as having no fee recipient at all.
ALTER TABLE deployed_pools
    ADD COLUMN IF NOT EXISTS fee_ppm     NUMERIC,
    ADD COLUMN IF NOT EXISTS fee_address TEXT;

ALTER TABLE deployed_pools
    DROP CONSTRAINT IF EXISTS deployed_pools_fee_address_format;

ALTER TABLE deployed_pools
    ADD CONSTRAINT deployed_pools_fee_address_format
        CHECK (fee_address IS NULL
            OR fee_address ~ '^0x[0-9a-fA-F]{40}$');

ALTER TABLE deployed_pools
    DROP CONSTRAINT IF EXISTS deployed_pools_fee_ppm_range;

ALTER TABLE deployed_pools
    ADD CONSTRAINT deployed_pools_fee_ppm_range
        CHECK (fee_ppm IS NULL OR (fee_ppm >= 0 AND fee_ppm <= 1000000));
