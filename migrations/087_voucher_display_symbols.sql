-- Store editable labels separately from recorded contract identity. Existing
-- labels are reviewed and assigned outside startup; no migration rewrites them.
ALTER TABLE vouchers ADD COLUMN display_symbol TEXT
    CONSTRAINT vouchers_display_symbol_valid CHECK (display_symbol ~ '^[A-Z0-9]{3,6}$');
ALTER TABLE deployed_tokens ADD COLUMN display_symbol TEXT
    CONSTRAINT deployed_tokens_display_symbol_valid CHECK (display_symbol ~ '^[A-Z0-9]{3,6}$');

ALTER TABLE identity_claims DROP CONSTRAINT identity_claims_namespace_check;
ALTER TABLE identity_claims ADD CONSTRAINT identity_claims_namespace_check
    CHECK (namespace IN ('recipient', 'symbol', 'display_symbol'));

CREATE INDEX vouchers_active_effective_symbol_trgm_idx
    ON vouchers USING gin (lower(COALESCE(display_symbol, symbol)) gin_trgm_ops) WHERE active;
CREATE INDEX vouchers_symbol_identity_idx ON vouchers (normalize_identity_name(symbol));
CREATE INDEX vouchers_display_symbol_identity_idx
    ON vouchers (normalize_identity_name(COALESCE(display_symbol, symbol))) WHERE active;
CREATE INDEX pools_symbol_identity_idx ON pools (normalize_identity_name(symbol));
CREATE INDEX deployed_tokens_symbol_identity_idx ON deployed_tokens (normalize_identity_name(symbol))
    WHERE status IN ('PENDING', 'CONFIRMED') AND voucher_id IS NULL;
CREATE INDEX deployed_tokens_display_symbol_identity_idx
    ON deployed_tokens (normalize_identity_name(COALESCE(display_symbol, symbol)))
    WHERE status IN ('PENDING', 'CONFIRMED') AND voucher_id IS NULL;
CREATE INDEX deployed_pools_symbol_identity_idx ON deployed_pools (normalize_identity_name(symbol))
    WHERE status IN ('PENDING', 'CONFIRMED') AND pool_id IS NULL;

-- Claims serialize all new writers. Catalog and unlinked deploy reads also
-- account for pre-claim legacy rows, without manufacturing a winner for any
-- existing collision. A promoted deploy is no longer a CLC-label reservation.
CREATE FUNCTION clc_symbol_occupancy(wanted_namespace TEXT)
RETURNS TABLE (symbol_key TEXT, owner_kind TEXT, owner_key TEXT)
LANGUAGE sql STABLE AS $$
    SELECT c.name, c.owner_kind, c.owner_key FROM identity_claims c
    WHERE c.namespace = wanted_namespace
    UNION ALL
    -- An old queued job can have a native reservation before its worker
    -- has created a deployment row. Its intended display label is the old
    -- ticker, but a new job with an explicit separate display claim is not.
    SELECT c.name, c.owner_kind, c.owner_key FROM identity_claims c
    WHERE wanted_namespace = 'display_symbol' AND c.namespace = 'symbol'
      AND starts_with(c.owner_key, 'deploy:')
      AND NOT EXISTS (
          SELECT 1 FROM identity_claims label WHERE label.namespace = 'display_symbol'
            AND label.owner_kind = c.owner_kind AND label.owner_key = c.owner_key
      )
    UNION ALL
    SELECT normalize_identity_name(v.symbol), 'voucher', v.chain_id::TEXT || ':' || lower(v.address)
    FROM vouchers v WHERE wanted_namespace = 'symbol'
    UNION ALL
    SELECT normalize_identity_name(COALESCE(v.display_symbol, v.symbol)), 'voucher',
           v.chain_id::TEXT || ':' || lower(v.address)
    FROM vouchers v WHERE wanted_namespace = 'display_symbol' AND v.active
    UNION ALL
    SELECT normalize_identity_name(p.symbol), 'pool', p.chain_id::TEXT || ':' || lower(p.address)
    FROM pools p WHERE wanted_namespace IN ('symbol', 'display_symbol')
    UNION ALL
    SELECT normalize_identity_name(d.symbol), 'voucher', 'deploy:' || o.tracking_id::TEXT
    FROM deployed_tokens d JOIN otx o ON o.id = d.otx_id
    WHERE wanted_namespace = 'symbol' AND d.status IN ('PENDING', 'CONFIRMED') AND d.voucher_id IS NULL
    UNION ALL
    SELECT normalize_identity_name(COALESCE(d.display_symbol, d.symbol)), 'voucher', 'deploy:' || o.tracking_id::TEXT
    FROM deployed_tokens d JOIN otx o ON o.id = d.otx_id
    WHERE wanted_namespace = 'display_symbol' AND d.status IN ('PENDING', 'CONFIRMED') AND d.voucher_id IS NULL
    UNION ALL
    SELECT normalize_identity_name(d.symbol), 'pool', 'deploy:' || o.tracking_id::TEXT
    FROM deployed_pools d JOIN otx o ON o.id = d.otx_id
    WHERE wanted_namespace IN ('symbol', 'display_symbol') AND d.status IN ('PENDING', 'CONFIRMED')
      AND d.pool_id IS NULL;
$$;

CREATE FUNCTION clc_display_symbol_occupied(value TEXT, kind TEXT, entity_key TEXT,
                                             deployment_key TEXT DEFAULT NULL)
RETURNS BOOLEAN LANGUAGE sql STABLE AS $$
    SELECT EXISTS (
        SELECT 1 FROM clc_symbol_occupancy('display_symbol') c
        WHERE c.symbol_key = normalize_identity_name(value)
          AND NOT (c.owner_kind = kind AND
                   (c.owner_key IS NOT DISTINCT FROM entity_key
                    OR c.owner_key IS NOT DISTINCT FROM deployment_key))
    );
$$;

CREATE FUNCTION claim_clc_display_symbol(value TEXT, kind TEXT, entity_key TEXT,
                                          deployment_key TEXT DEFAULT NULL)
RETURNS VOID LANGUAGE plpgsql AS $$
BEGIN
    IF value IS NULL OR btrim(value) = '' THEN RETURN; END IF;
    IF clc_display_symbol_occupied(value, kind, entity_key, deployment_key) THEN
        RAISE unique_violation;
    END IF;
    PERFORM claim_identity('display_symbol', value, kind, entity_key, deployment_key);
EXCEPTION WHEN unique_violation THEN
    RAISE EXCEPTION 'display symbol is already in use'
        USING ERRCODE = '23505', CONSTRAINT = 'clc_display_symbol_key';
END;
$$;

-- Non-raising conditional insert allows the server to try the next immutable
-- contract ticker without poisoning its transaction on a competing claim.
CREATE FUNCTION try_reserve_clc_native_symbol(value TEXT, kind TEXT, entity_key TEXT)
RETURNS BOOLEAN LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM clc_symbol_occupancy('symbol') c
        WHERE c.symbol_key = normalize_identity_name(value)
          AND NOT (c.owner_kind = kind AND c.owner_key = entity_key)
    ) THEN RETURN FALSE; END IF;
    INSERT INTO identity_claims (namespace, name, owner_kind, owner_key)
    VALUES ('symbol', normalize_identity_name(value), kind, entity_key)
    ON CONFLICT (namespace, name) DO UPDATE SET owner_key = EXCLUDED.owner_key
    WHERE identity_claims.owner_kind = EXCLUDED.owner_kind
      AND identity_claims.owner_key = EXCLUDED.owner_key;
    RETURN FOUND;
END;
$$;

-- Do not re-claim an untouched contract ticker during a deployment replay;
-- historical catalog rows can predate the original identity claims migration.
-- Keep native reservations independent from display lifecycle changes.
CREATE OR REPLACE FUNCTION sync_catalog_identity() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
    kind TEXT;
    entity_key TEXT;
    previous_key TEXT;
    deployment_key TEXT;
BEGIN
    kind := CASE TG_TABLE_NAME WHEN 'users' THEN 'user'
                WHEN 'pools' THEN 'pool' ELSE 'voucher' END;
    IF TG_OP = 'UPDATE' AND kind = 'voucher' THEN
        IF ROW(OLD.chain_id, OLD.address, OLD.symbol)
            IS NOT DISTINCT FROM ROW(NEW.chain_id, NEW.address, NEW.symbol)
            AND NOT EXISTS (
                SELECT 1 FROM deployed_tokens d JOIN otx o ON o.id = d.otx_id
                JOIN platform_contracts pc ON pc.id = d.platform_contract_id
                JOIN identity_claims c ON c.owner_key = 'deploy:' || o.tracking_id::TEXT
                WHERE pc.chain_id = NEW.chain_id AND lower(d.address) = lower(NEW.address)
                  AND c.namespace = 'symbol' AND c.owner_kind = 'voucher'
                  AND c.name = normalize_identity_name(NEW.symbol)
            ) THEN
            RETURN NEW;
        END IF;
    END IF;
    IF TG_OP <> 'INSERT' THEN
        IF kind = 'user' THEN previous_key := lower(OLD.blockchain_address);
        ELSE previous_key := OLD.chain_id::TEXT || ':' || lower(OLD.address); END IF;
        DELETE FROM identity_claims WHERE namespace IN ('recipient', 'symbol')
          AND owner_kind = kind AND owner_key = previous_key;
    END IF;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    IF kind = 'user' THEN
        PERFORM claim_identity('recipient', NEW.username, kind, lower(NEW.blockchain_address));
        RETURN NEW;
    END IF;

    entity_key := NEW.chain_id::TEXT || ':' || lower(NEW.address);
    IF kind = 'pool' THEN
        SELECT 'deploy:' || o.tracking_id::TEXT INTO deployment_key
        FROM deployed_pools d JOIN otx o ON o.id = d.otx_id
        JOIN platform_contracts pc ON pc.id = d.platform_contract_id
        WHERE lower(d.address) = lower(NEW.address) AND pc.chain_id = NEW.chain_id;
        PERFORM claim_identity('recipient', COALESCE(NEW.display_name, NEW.name), kind, entity_key, deployment_key);
    ELSE
        SELECT 'deploy:' || o.tracking_id::TEXT INTO deployment_key
        FROM deployed_tokens d JOIN otx o ON o.id = d.otx_id
        JOIN platform_contracts pc ON pc.id = d.platform_contract_id
        WHERE lower(d.address) = lower(NEW.address) AND pc.chain_id = NEW.chain_id;
    END IF;
    PERFORM claim_identity('symbol', NEW.symbol, kind, entity_key, deployment_key);
    RETURN NEW;
END;
$$;

CREATE FUNCTION sync_catalog_display_symbol() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
    kind TEXT := CASE TG_TABLE_NAME WHEN 'pools' THEN 'pool' ELSE 'voucher' END;
    entity_key TEXT;
    deployment_key TEXT;
    label TEXT;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF kind = 'voucher' THEN
            IF ROW(OLD.chain_id, OLD.address, OLD.active, COALESCE(OLD.display_symbol, OLD.symbol))
                IS NOT DISTINCT FROM
               ROW(NEW.chain_id, NEW.address, NEW.active, COALESCE(NEW.display_symbol, NEW.symbol))
               AND OLD.display_symbol IS NOT DISTINCT FROM NEW.display_symbol THEN
                RETURN NEW;
            END IF;
        ELSE
            IF ROW(OLD.chain_id, OLD.address, OLD.symbol)
                IS NOT DISTINCT FROM ROW(NEW.chain_id, NEW.address, NEW.symbol) THEN
                RETURN NEW;
            END IF;
        END IF;
    END IF;

    IF TG_OP <> 'INSERT' THEN
        DELETE FROM identity_claims WHERE namespace = 'display_symbol' AND owner_kind = kind
          AND owner_key = OLD.chain_id::TEXT || ':' || lower(OLD.address);
    END IF;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;

    entity_key := NEW.chain_id::TEXT || ':' || lower(NEW.address);
    IF kind = 'voucher' THEN
        IF NOT NEW.active THEN RETURN NEW; END IF;
        label := COALESCE(NEW.display_symbol, NEW.symbol);
        SELECT 'deploy:' || o.tracking_id::TEXT INTO deployment_key
        FROM deployed_tokens d JOIN otx o ON o.id = d.otx_id
        JOIN platform_contracts pc ON pc.id = d.platform_contract_id
        WHERE lower(d.address) = lower(NEW.address) AND pc.chain_id = NEW.chain_id;
    ELSE
        label := NEW.symbol;
        SELECT 'deploy:' || o.tracking_id::TEXT INTO deployment_key
        FROM deployed_pools d JOIN otx o ON o.id = d.otx_id
        JOIN platform_contracts pc ON pc.id = d.platform_contract_id
        WHERE lower(d.address) = lower(NEW.address) AND pc.chain_id = NEW.chain_id;
    END IF;
    PERFORM claim_clc_display_symbol(label, kind, entity_key, deployment_key);
    RETURN NEW;
END;
$$;

-- The existing native claim triggers run first on the same row. All current
-- reservation paths take native claims before CLC claims.
CREATE TRIGGER zz_vouchers_display_symbol_identity
AFTER INSERT OR UPDATE OF symbol, display_symbol, active, address, chain_id OR DELETE ON vouchers
FOR EACH ROW EXECUTE FUNCTION sync_catalog_display_symbol();
CREATE TRIGGER zz_pools_display_symbol_identity
AFTER INSERT OR UPDATE OF symbol, address, chain_id OR DELETE ON pools
FOR EACH ROW EXECUTE FUNCTION sync_catalog_display_symbol();

CREATE FUNCTION sync_deployment_display_symbol() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
    kind TEXT := CASE TG_TABLE_NAME WHEN 'deployed_pools' THEN 'pool' ELSE 'voucher' END;
    entity_key TEXT;
    label TEXT;
BEGIN
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF; -- native trigger cleans all deploy claims
    IF NEW.status <> 'PENDING' THEN RETURN NEW; END IF;
    SELECT 'deploy:' || tracking_id::TEXT INTO entity_key FROM otx WHERE id = NEW.otx_id;
    IF kind = 'pool' THEN label := NEW.symbol;
    ELSE label := COALESCE(NEW.display_symbol, NEW.symbol); END IF;
    PERFORM claim_clc_display_symbol(label, kind, entity_key);
    RETURN NEW;
END;
$$;

CREATE TRIGGER zz_deployed_tokens_display_symbol_identity
AFTER INSERT OR UPDATE OF status, symbol, display_symbol ON deployed_tokens
FOR EACH ROW EXECUTE FUNCTION sync_deployment_display_symbol();
CREATE TRIGGER zz_deployed_pools_display_symbol_identity
AFTER INSERT OR UPDATE OF status, symbol ON deployed_pools
FOR EACH ROW EXECUTE FUNCTION sync_deployment_display_symbol();
