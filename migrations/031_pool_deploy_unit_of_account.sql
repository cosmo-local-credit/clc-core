ALTER TABLE deployed_pools
    ADD COLUMN IF NOT EXISTS unit_of_account TEXT;
