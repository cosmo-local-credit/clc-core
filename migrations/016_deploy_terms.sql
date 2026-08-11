ALTER TABLE deployed_tokens
    ADD COLUMN IF NOT EXISTS terms TEXT;

ALTER TABLE deployed_pools
    ADD COLUMN IF NOT EXISTS terms TEXT;
