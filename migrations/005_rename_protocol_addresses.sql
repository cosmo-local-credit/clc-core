DO $$
BEGIN
    IF to_regclass('public.pool_addresses') IS NOT NULL
       AND to_regclass('public.protocol_addresses') IS NULL THEN
        ALTER TABLE pool_addresses RENAME TO protocol_addresses;
    END IF;
END $$;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'public.protocol_addresses'::regclass
          AND conname = 'pool_addresses_address_format'
    ) THEN
        ALTER TABLE protocol_addresses
            RENAME CONSTRAINT pool_addresses_address_format
            TO protocol_addresses_address_format;
    END IF;
END $$;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_trigger
        WHERE tgrelid = 'public.protocol_addresses'::regclass
          AND tgname = 'update_pool_addresses_timestamp'
    ) THEN
        ALTER TRIGGER update_pool_addresses_timestamp
            ON protocol_addresses
            RENAME TO update_protocol_addresses_timestamp;
    END IF;
END $$;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = to_regclass('public.protocol_addresses')
          AND conname = 'pool_addresses_pkey'
    ) THEN
        ALTER TABLE protocol_addresses
            RENAME CONSTRAINT pool_addresses_pkey TO protocol_addresses_pkey;
    END IF;

    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = to_regclass('public.protocol_addresses')
          AND conname = 'pool_addresses_key_key'
    ) THEN
        ALTER TABLE protocol_addresses
            RENAME CONSTRAINT pool_addresses_key_key TO protocol_addresses_key_key;
    END IF;

    IF to_regclass('public.pool_addresses_id_seq') IS NOT NULL THEN
        ALTER SEQUENCE pool_addresses_id_seq RENAME TO protocol_addresses_id_seq;
    END IF;
END $$;

DO $$
DECLARE
    col TEXT;
BEGIN
    FOREACH col IN ARRAY ARRAY['id', 'key', 'address', 'created_at', 'updated_at'] LOOP
        IF EXISTS (
            SELECT 1 FROM pg_constraint
            WHERE conrelid = to_regclass('public.protocol_addresses')
              AND conname = 'pool_addresses_' || col || '_not_null'
        ) THEN
            EXECUTE format(
                'ALTER TABLE protocol_addresses RENAME CONSTRAINT %I TO %I',
                'pool_addresses_' || col || '_not_null',
                'protocol_addresses_' || col || '_not_null');
        END IF;
    END LOOP;
END $$;
