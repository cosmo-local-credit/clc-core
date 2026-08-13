-- Standardize on TIMESTAMPTZ. users and user_deployments already use it;
-- this converts the remaining naive TIMESTAMP columns.
--
-- No explicit conversion clause: with the session timezone pinned to UTC
-- the bare type change is catalog-only (no table rewrite), whereas an
-- explicit `AT TIME ZONE` expression forces a full rewrite. SET LOCAL
-- relies on tern running this file inside its default per-migration
-- transaction — this file must keep running transactionally.
SET LOCAL TIME ZONE 'UTC';

ALTER TABLE keystore
    ALTER COLUMN created_at TYPE TIMESTAMPTZ,
    ALTER COLUMN updated_at TYPE TIMESTAMPTZ;

ALTER TABLE master_key
    ALTER COLUMN created_at TYPE TIMESTAMPTZ,
    ALTER COLUMN updated_at TYPE TIMESTAMPTZ;

ALTER TABLE noncestore
    ALTER COLUMN updated_at TYPE TIMESTAMPTZ;

ALTER TABLE otx
    ALTER COLUMN created_at TYPE TIMESTAMPTZ,
    ALTER COLUMN updated_at TYPE TIMESTAMPTZ;

ALTER TABLE dispatch
    ALTER COLUMN created_at TYPE TIMESTAMPTZ,
    ALTER COLUMN updated_at TYPE TIMESTAMPTZ;

ALTER TABLE upgrade_meta
    ALTER COLUMN created_at TYPE TIMESTAMPTZ;

ALTER TABLE platform_contracts
    ALTER COLUMN created_at TYPE TIMESTAMPTZ,
    ALTER COLUMN updated_at TYPE TIMESTAMPTZ;

ALTER TABLE deployed_tokens
    ALTER COLUMN created_at TYPE TIMESTAMPTZ,
    ALTER COLUMN updated_at TYPE TIMESTAMPTZ;

DO $$
BEGIN
    IF to_regclass('public.pool_addresses') IS NOT NULL THEN
        ALTER TABLE pool_addresses
            ALTER COLUMN created_at TYPE TIMESTAMPTZ,
            ALTER COLUMN updated_at TYPE TIMESTAMPTZ;
    END IF;
    IF to_regclass('public.protocol_addresses') IS NOT NULL THEN
        ALTER TABLE protocol_addresses
            ALTER COLUMN created_at TYPE TIMESTAMPTZ,
            ALTER COLUMN updated_at TYPE TIMESTAMPTZ;
    END IF;
END $$;

ALTER TABLE deployed_pools
    ALTER COLUMN created_at TYPE TIMESTAMPTZ,
    ALTER COLUMN updated_at TYPE TIMESTAMPTZ;

ALTER TABLE oracle_feeds
    ALTER COLUMN created_at TYPE TIMESTAMPTZ,
    ALTER COLUMN updated_at TYPE TIMESTAMPTZ;

ALTER TABLE webauthn_credentials
    ALTER COLUMN created_at TYPE TIMESTAMPTZ;
