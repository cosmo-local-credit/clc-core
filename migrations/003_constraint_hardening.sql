-- Constraint hardening: enforce invariants the application already relies on.
--
-- Every ADD CONSTRAINT is guarded so re-runs are no-ops and so the same
-- constraints can live inline in the 001 baseline for fresh databases.

-- 1a. Dispatch: the application models exactly one mutable dispatch row per
-- OTX (insert once, update in place). Enforce it so joins cannot fan out.
ALTER TABLE dispatch
    ALTER COLUMN otx_id SET NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'dispatch'::regclass
          AND conname = 'dispatch_otx_id_unique'
    ) THEN
        ALTER TABLE dispatch ADD CONSTRAINT dispatch_otx_id_unique UNIQUE (otx_id);
    END IF;
END $$;

-- 1b. master_key.id is a keystore id (see bootstrap-master-key). Add the
-- missing FK and enforce the singleton in the database instead of relying on
-- the racy application-level WHERE NOT EXISTS guard.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'master_key'::regclass
          AND conname = 'master_key_id_fkey'
    ) THEN
        ALTER TABLE master_key
            ADD CONSTRAINT master_key_id_fkey FOREIGN KEY (id) REFERENCES keystore(id);
    END IF;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS master_key_singleton_idx
    ON master_key ((true));

-- 1c. Nonce hardening. BIGINT is signed 64-bit (0..2^63-1 after the check) —
-- not Ethereum's full uint64 range, but unreachable in practice.
UPDATE noncestore
SET next_nonce = 0
WHERE next_nonce IS NULL;

ALTER TABLE noncestore
    ALTER COLUMN next_nonce TYPE BIGINT,
    ALTER COLUMN next_nonce SET NOT NULL,
    ALTER COLUMN next_nonce SET DEFAULT 0;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'noncestore'::regclass
          AND conname = 'noncestore_next_nonce_nonneg'
    ) THEN
        ALTER TABLE noncestore
            ADD CONSTRAINT noncestore_next_nonce_nonneg CHECK (next_nonce >= 0);
    END IF;
END $$;

ALTER TABLE otx
    ALTER COLUMN nonce TYPE BIGINT;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'otx'::regclass
          AND conname = 'otx_nonce_nonneg'
    ) THEN
        ALTER TABLE otx ADD CONSTRAINT otx_nonce_nonneg CHECK (nonce >= 0);
    END IF;
END $$;

-- 1d. Domain checks.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'deployed_tokens'::regclass
          AND conname = 'deployed_tokens_decimals_range'
    ) THEN
        ALTER TABLE deployed_tokens
            ADD CONSTRAINT deployed_tokens_decimals_range
            CHECK (decimals BETWEEN 0 AND 255);
    END IF;
END $$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'deployed_pools'::regclass
          AND conname = 'deployed_pools_decimals_range'
    ) THEN
        ALTER TABLE deployed_pools
            ADD CONSTRAINT deployed_pools_decimals_range
            CHECK (decimals BETWEEN 0 AND 255);
    END IF;
END $$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'users'::regclass
          AND conname = 'users_quotas_nonneg'
    ) THEN
        ALTER TABLE users
            ADD CONSTRAINT users_quotas_nonneg
            CHECK (max_pools >= 0 AND max_tokens >= 0);
    END IF;
END $$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'deployed_tokens'::regclass
          AND conname = 'deployed_tokens_geo_valid'
    ) THEN
        ALTER TABLE deployed_tokens
            ADD CONSTRAINT deployed_tokens_geo_valid
            CHECK (
                ((geo_lat IS NULL) = (geo_lng IS NULL))
                AND (geo_lat IS NULL OR geo_lat BETWEEN -90 AND 90)
                AND (geo_lng IS NULL OR geo_lng BETWEEN -180 AND 180)
            );
    END IF;
END $$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'deployed_pools'::regclass
          AND conname = 'deployed_pools_geo_valid'
    ) THEN
        ALTER TABLE deployed_pools
            ADD CONSTRAINT deployed_pools_geo_valid
            CHECK (
                ((geo_lat IS NULL) = (geo_lng IS NULL))
                AND (geo_lat IS NULL OR geo_lat BETWEEN -90 AND 90)
                AND (geo_lng IS NULL OR geo_lng BETWEEN -180 AND 180)
            );
    END IF;
END $$;

-- 002 made address nullable to support PENDING rows; a confirmed row must
-- always have one.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'deployed_tokens'::regclass
          AND conname = 'deployed_tokens_confirmed_has_address'
    ) THEN
        ALTER TABLE deployed_tokens
            ADD CONSTRAINT deployed_tokens_confirmed_has_address
            CHECK (status <> 'CONFIRMED' OR address IS NOT NULL);
    END IF;
END $$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'deployed_pools'::regclass
          AND conname = 'deployed_pools_confirmed_has_address'
    ) THEN
        ALTER TABLE deployed_pools
            ADD CONSTRAINT deployed_pools_confirmed_has_address
            CHECK (status <> 'CONFIRMED' OR address IS NOT NULL);
    END IF;
END $$;

-- 1e. Drop indexes already implied by unique constraints.
DROP INDEX IF EXISTS public_key_idx;                    -- dup of keystore_public_key_key
DROP INDEX IF EXISTS users_blockchain_address_idx;      -- dup of users_blockchain_address_unique
DROP INDEX IF EXISTS user_deployments_tracking_id_idx;  -- dup of UNIQUE (tracking_id)

-- 2a. EIP-55 canonicalization: the seeded superadmin predates the checksum
-- policy and is stored lowercase. All other seed literals are already
-- checksummed. Checksummed form computed with go-ethereum
-- (common.HexToAddress(...).Hex()).
UPDATE users
SET blockchain_address = '0x33a573149db22e759fB9A38Bcc461C12855c2645'
WHERE blockchain_address = '0x33a573149db22e759fb9a38bcc461c12855c2645';

-- 2b. Case-insensitive uniqueness backstops. Stored values stay EIP-55
-- checksummed; these indexes prevent case-variant duplicates from any future
-- non-canonical write path. NULL addresses (PENDING rows) never collide.
CREATE UNIQUE INDEX IF NOT EXISTS keystore_public_key_lower_key
    ON keystore (lower(public_key));
CREATE UNIQUE INDEX IF NOT EXISTS users_blockchain_address_lower_key
    ON users (lower(blockchain_address));
CREATE UNIQUE INDEX IF NOT EXISTS deployed_tokens_address_lower_key
    ON deployed_tokens (lower(address));
CREATE UNIQUE INDEX IF NOT EXISTS deployed_pools_address_lower_key
    ON deployed_pools (lower(address));

-- 2b. Address shape checks across the full inventory, one constraint per
-- table. The pattern is case-insensitive so EIP-55 values pass; NULLs pass
-- any CHECK, so nullable columns are unaffected. Checksum validation itself
-- needs keccak256 and stays in the application layer.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'keystore'::regclass
          AND conname = 'keystore_public_key_format'
    ) THEN
        ALTER TABLE keystore
            ADD CONSTRAINT keystore_public_key_format
            CHECK (public_key ~ '^0x[0-9a-fA-F]{40}$');
    END IF;
END $$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'users'::regclass
          AND conname = 'users_blockchain_address_format'
    ) THEN
        ALTER TABLE users
            ADD CONSTRAINT users_blockchain_address_format
            CHECK (blockchain_address ~ '^0x[0-9a-fA-F]{40}$');
    END IF;
END $$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'deployed_tokens'::regclass
          AND conname = 'deployed_tokens_address_format'
    ) THEN
        ALTER TABLE deployed_tokens
            ADD CONSTRAINT deployed_tokens_address_format
            CHECK (address ~ '^0x[0-9a-fA-F]{40}$'
               AND admin   ~ '^0x[0-9a-fA-F]{40}$');
    END IF;
END $$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'deployed_pools'::regclass
          AND conname = 'deployed_pools_address_format'
    ) THEN
        ALTER TABLE deployed_pools
            ADD CONSTRAINT deployed_pools_address_format
            CHECK (address                         ~ '^0x[0-9a-fA-F]{40}$'
               AND admin                           ~ '^0x[0-9a-fA-F]{40}$'
               AND token_registry_address          ~ '^0x[0-9a-fA-F]{40}$'
               AND limiter_address                 ~ '^0x[0-9a-fA-F]{40}$'
               AND fee_policy_address              ~ '^0x[0-9a-fA-F]{40}$'
               AND quoter_address                  ~ '^0x[0-9a-fA-F]{40}$'
               AND protocol_fee_controller_address ~ '^0x[0-9a-fA-F]{40}$');
    END IF;
END $$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'upgrade_meta'::regclass
          AND conname = 'upgrade_meta_address_format'
    ) THEN
        ALTER TABLE upgrade_meta
            ADD CONSTRAINT upgrade_meta_address_format
            CHECK (proxy_address      ~ '^0x[0-9a-fA-F]{40}$'
               AND new_implementation ~ '^0x[0-9a-fA-F]{40}$');
    END IF;
END $$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'platform_contracts'::regclass
          AND conname = 'platform_contracts_address_format'
    ) THEN
        ALTER TABLE platform_contracts
            ADD CONSTRAINT platform_contracts_address_format
            CHECK (factory_address        ~ '^0x[0-9a-fA-F]{40}$'
               AND implementation_address ~ '^0x[0-9a-fA-F]{40}$');
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('public.pool_addresses') IS NOT NULL
       AND NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = to_regclass('public.pool_addresses')
          AND conname = 'pool_addresses_address_format'
    ) THEN
        ALTER TABLE pool_addresses
            ADD CONSTRAINT pool_addresses_address_format
            CHECK (address ~ '^0x[0-9a-fA-F]{40}$');
    END IF;

    IF to_regclass('public.protocol_addresses') IS NOT NULL
       AND NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = to_regclass('public.protocol_addresses')
          AND conname IN ('protocol_addresses_address_format', 'pool_addresses_address_format')
    ) THEN
        ALTER TABLE protocol_addresses
            ADD CONSTRAINT protocol_addresses_address_format
            CHECK (address ~ '^0x[0-9a-fA-F]{40}$');
    END IF;
END $$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'oracle_feeds'::regclass
          AND conname = 'oracle_feeds_address_format'
    ) THEN
        ALTER TABLE oracle_feeds
            ADD CONSTRAINT oracle_feeds_address_format
            CHECK (address ~ '^0x[0-9a-fA-F]{40}$');
    END IF;
END $$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'webauthn_credentials'::regclass
          AND conname = 'webauthn_address_format'
    ) THEN
        ALTER TABLE webauthn_credentials
            ADD CONSTRAINT webauthn_address_format
            CHECK (address ~ '^0x[0-9a-fA-F]{40}$');
    END IF;
END $$;
