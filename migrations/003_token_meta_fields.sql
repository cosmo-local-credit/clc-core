-- Extended token metadata: scalar fields on staging and final tables
ALTER TABLE token_deploy_meta
  ADD COLUMN IF NOT EXISTS description    TEXT,
  ADD COLUMN IF NOT EXISTS website        TEXT,
  ADD COLUMN IF NOT EXISTS email          TEXT,
  ADD COLUMN IF NOT EXISTS phone          TEXT,
  ADD COLUMN IF NOT EXISTS voucher_uoa    TEXT,
  ADD COLUMN IF NOT EXISTS voucher_value  NUMERIC,
  ADD COLUMN IF NOT EXISTS location_name  TEXT,
  ADD COLUMN IF NOT EXISTS geo_lat        FLOAT8,
  ADD COLUMN IF NOT EXISTS geo_lng        FLOAT8;

ALTER TABLE deployed_tokens
  ADD COLUMN IF NOT EXISTS description    TEXT,
  ADD COLUMN IF NOT EXISTS website        TEXT,
  ADD COLUMN IF NOT EXISTS email          TEXT,
  ADD COLUMN IF NOT EXISTS phone          TEXT,
  ADD COLUMN IF NOT EXISTS voucher_uoa    TEXT,
  ADD COLUMN IF NOT EXISTS voucher_value  NUMERIC,
  ADD COLUMN IF NOT EXISTS location_name  TEXT,
  ADD COLUMN IF NOT EXISTS geo_lat        FLOAT8,
  ADD COLUMN IF NOT EXISTS geo_lng        FLOAT8;

-- Predefined tag lookup (referential integrity anchor)
CREATE TABLE IF NOT EXISTS token_tag (
    value TEXT PRIMARY KEY
);
INSERT INTO token_tag (value) VALUES
    ('Community'), ('Voucher'), ('Celo'),
    ('Agriculture'), ('Education'), ('Eco'),
    ('Women-led'), ('Urgent'), ('Liquidity'), ('Swap')
ON CONFLICT (value) DO NOTHING;

-- Staging: tags chosen at deploy time (linked via otx_id to token_deploy_meta)
CREATE TABLE IF NOT EXISTS token_deploy_meta_tags (
    otx_id    INT  REFERENCES token_deploy_meta(otx_id) ON DELETE CASCADE NOT NULL,
    tag_value TEXT REFERENCES token_tag(value) NOT NULL,
    PRIMARY KEY (otx_id, tag_value)
);

-- Final: tags on confirmed deployed tokens
CREATE TABLE IF NOT EXISTS deployed_token_tags (
    deployed_token_id INT  REFERENCES deployed_tokens(id) ON DELETE CASCADE NOT NULL,
    tag_value         TEXT REFERENCES token_tag(value) NOT NULL,
    PRIMARY KEY (deployed_token_id, tag_value)
);
