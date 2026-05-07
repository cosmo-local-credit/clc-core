-- Extended pool metadata: contact & location fields on staging and final tables
ALTER TABLE pool_deploy_meta
  ADD COLUMN IF NOT EXISTS description   TEXT,
  ADD COLUMN IF NOT EXISTS website       TEXT,
  ADD COLUMN IF NOT EXISTS email         TEXT,
  ADD COLUMN IF NOT EXISTS phone         TEXT,
  ADD COLUMN IF NOT EXISTS location_name TEXT,
  ADD COLUMN IF NOT EXISTS geo_lat       FLOAT8,
  ADD COLUMN IF NOT EXISTS geo_lng       FLOAT8;

ALTER TABLE deployed_pools
  ADD COLUMN IF NOT EXISTS description   TEXT,
  ADD COLUMN IF NOT EXISTS website       TEXT,
  ADD COLUMN IF NOT EXISTS email         TEXT,
  ADD COLUMN IF NOT EXISTS phone         TEXT,
  ADD COLUMN IF NOT EXISTS location_name TEXT,
  ADD COLUMN IF NOT EXISTS geo_lat       FLOAT8,
  ADD COLUMN IF NOT EXISTS geo_lng       FLOAT8;
