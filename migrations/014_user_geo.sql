-- Capture precise user location from onboarding / profile edit.
-- Reuses the location_name + geo_lat/geo_lng pattern already on
-- deployed_tokens, deployed_pools, vouchers, and pools. No index: nothing
-- queries users by proximity yet. Reverses locked decision I4 narrowly
-- (geo only, not demographics generally).

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS location_name TEXT,
    ADD COLUMN IF NOT EXISTS geo_lat       FLOAT8,
    ADD COLUMN IF NOT EXISTS geo_lng       FLOAT8;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'users'::regclass
          AND conname = 'users_geo_valid'
    ) THEN
        ALTER TABLE users
            ADD CONSTRAINT users_geo_valid CHECK (
                ((geo_lat IS NULL) = (geo_lng IS NULL))
                AND (geo_lat IS NULL OR geo_lat BETWEEN -90 AND 90)
                AND (geo_lng IS NULL OR geo_lng BETWEEN -180 AND 180)
            );
    END IF;
END $$;
