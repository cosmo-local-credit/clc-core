-- Add recipient identities after the existing schema migrations.
-- This project starts from clean data; no historical name backfill is required.
ALTER TABLE users ADD COLUMN username TEXT;
ALTER TABLE users ADD CONSTRAINT users_username_valid CHECK (username IS NULL OR (
    username = lower(btrim(username)) AND char_length(username) BETWEEN 3 AND 24
    AND username ~ '^[a-z0-9][a-z0-9-]*[a-z0-9]$' AND username ~ '[a-z]'));

-- Current recipient names and catalog symbols. Claims use existing pool
-- display names; no additional pool alias field or legacy name history.
CREATE FUNCTION normalize_identity_name(value TEXT) RETURNS TEXT
LANGUAGE sql IMMUTABLE STRICT AS $$
    SELECT lower(regexp_replace(btrim(value), '\s+', ' ', 'g'));
$$;

CREATE TABLE identity_claims (
    namespace TEXT NOT NULL CHECK (namespace IN ('recipient', 'symbol')),
    name TEXT NOT NULL CHECK (name <> ''),
    owner_kind TEXT NOT NULL CHECK (owner_kind IN ('user', 'pool', 'voucher')),
    owner_key TEXT NOT NULL,
    CONSTRAINT identity_claims_name_key PRIMARY KEY (namespace, name),
    CONSTRAINT identity_claims_owner_key UNIQUE (namespace, owner_kind, owner_key)
);

-- Unique insertion serializes concurrent claims, including before a worker
-- creates its deployment row. Promotion can transfer only its own reservation.
CREATE FUNCTION claim_identity(ns TEXT, value TEXT, kind TEXT, entity_key TEXT,
                               deployment_key TEXT DEFAULT NULL) RETURNS VOID
LANGUAGE plpgsql AS $$
BEGIN
    IF value IS NULL OR btrim(value) = '' THEN RETURN; END IF;
    INSERT INTO identity_claims(namespace, name, owner_kind, owner_key)
    VALUES (ns, normalize_identity_name(value), kind, entity_key)
    ON CONFLICT (namespace, name) DO UPDATE SET owner_key = EXCLUDED.owner_key
    WHERE identity_claims.owner_kind = EXCLUDED.owner_kind
      AND (identity_claims.owner_key = EXCLUDED.owner_key
           OR identity_claims.owner_key = deployment_key);
    IF NOT FOUND THEN
        RAISE EXCEPTION 'name or symbol is already in use'
            USING ERRCODE = '23505', CONSTRAINT = 'identity_claims_name_key';
    END IF;
END;
$$;

CREATE FUNCTION sync_catalog_identity() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
    kind TEXT;
    entity_key TEXT;
    previous_key TEXT;
    deployment_key TEXT;
    ticker TEXT;
BEGIN
    kind := CASE TG_TABLE_NAME WHEN 'users' THEN 'user'
                WHEN 'pools' THEN 'pool' ELSE 'voucher' END;
    IF TG_OP <> 'INSERT' THEN
        IF kind = 'user' THEN previous_key := lower(OLD.blockchain_address);
        ELSE previous_key := OLD.chain_id::TEXT || ':' || lower(OLD.address); END IF;
        DELETE FROM identity_claims WHERE owner_kind = kind AND owner_key = previous_key;
    END IF;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    IF kind = 'user' THEN
        entity_key := lower(NEW.blockchain_address);
        PERFORM claim_identity('recipient', NEW.username, kind, entity_key);
    ELSE
        entity_key := NEW.chain_id::TEXT || ':' || lower(NEW.address);
        ticker := NEW.symbol;
        IF kind = 'pool' THEN
            SELECT 'deploy:' || o.tracking_id::TEXT INTO deployment_key
            FROM deployed_pools d JOIN otx o ON o.id = d.otx_id
            JOIN platform_contracts pc ON pc.id = d.platform_contract_id
            WHERE lower(d.address) = lower(NEW.address) AND pc.chain_id = NEW.chain_id;
        ELSE
            SELECT 'deploy:' || o.tracking_id::TEXT INTO deployment_key
            FROM deployed_tokens d JOIN otx o ON o.id = d.otx_id
            JOIN platform_contracts pc ON pc.id = d.platform_contract_id
            WHERE lower(d.address) = lower(NEW.address) AND pc.chain_id = NEW.chain_id;
        END IF;
        IF kind = 'pool' THEN
            PERFORM claim_identity('recipient', COALESCE(NEW.display_name, NEW.name), kind, entity_key, deployment_key);
        END IF;
        PERFORM claim_identity('symbol', ticker, kind, entity_key, deployment_key);
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER users_identity AFTER INSERT OR UPDATE OF username, blockchain_address OR DELETE
ON users FOR EACH ROW EXECUTE FUNCTION sync_catalog_identity();
CREATE TRIGGER pools_identity AFTER INSERT OR UPDATE OF display_name, name, symbol, address, chain_id OR DELETE
ON pools FOR EACH ROW EXECUTE FUNCTION sync_catalog_identity();
CREATE TRIGGER vouchers_identity AFTER INSERT OR UPDATE OF symbol, address, chain_id OR DELETE
ON vouchers FOR EACH ROW EXECUTE FUNCTION sync_catalog_identity();

CREATE FUNCTION sync_deployment_identity() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
    kind TEXT := CASE TG_TABLE_NAME WHEN 'deployed_pools' THEN 'pool' ELSE 'voucher' END;
    entity_key TEXT;
BEGIN
    IF TG_OP = 'DELETE' THEN
        SELECT 'deploy:' || tracking_id::TEXT INTO entity_key FROM otx WHERE id = OLD.otx_id;
        DELETE FROM identity_claims WHERE owner_kind = kind AND owner_key = entity_key;
        RETURN OLD;
    END IF;
    SELECT 'deploy:' || tracking_id::TEXT INTO entity_key FROM otx WHERE id = NEW.otx_id;
    IF NEW.status = 'FAILED' THEN
        DELETE FROM identity_claims WHERE owner_kind = kind AND owner_key = entity_key;
    ELSIF NEW.status = 'PENDING' THEN
        IF kind = 'pool' THEN
            PERFORM claim_identity('recipient', NEW.name, kind, entity_key);
        END IF;
        PERFORM claim_identity('symbol', NEW.symbol, kind, entity_key);
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER deployed_pools_identity AFTER INSERT OR UPDATE OF status, name, symbol OR DELETE
ON deployed_pools FOR EACH ROW EXECUTE FUNCTION sync_deployment_identity();
CREATE TRIGGER deployed_tokens_identity AFTER INSERT OR UPDATE OF status, symbol OR DELETE
ON deployed_tokens FOR EACH ROW EXECUTE FUNCTION sync_deployment_identity();

---- create above / drop below ----

DROP TRIGGER deployed_tokens_identity ON deployed_tokens;
DROP TRIGGER deployed_pools_identity ON deployed_pools;
DROP TRIGGER vouchers_identity ON vouchers;
DROP TRIGGER pools_identity ON pools;
DROP TRIGGER users_identity ON users;
DROP FUNCTION sync_deployment_identity();
DROP FUNCTION sync_catalog_identity();
DROP FUNCTION claim_identity(TEXT, TEXT, TEXT, TEXT, TEXT);
DROP TABLE identity_claims;
DROP FUNCTION normalize_identity_name(TEXT);
ALTER TABLE users DROP COLUMN username;
