-- Removes what an out-of-tree recovery build added to the production database
-- as versions 070-072 (those versions are no-ops here, see 070-072). None of it
-- was ever populated: six tables, eight triggers, seven functions, and a
-- replacement sync_catalog_identity().
--
-- Safe to run anywhere. Every drop is IF EXISTS, so a database that never had
-- these objects (staging, fresh installs) passes through unchanged, and the
-- function is restored to the definition migration 041 created, which that
-- database already holds. If any of the six tables holds a row, the migration
-- stops and drops nothing: those rows would be an audit trail, not leftovers.
DO $$
DECLARE
    tbl TEXT;
    populated BOOLEAN;
BEGIN
    FOREACH tbl IN ARRAY ARRAY[
        'recovery_campaigns', 'voucher_recoveries', 'voucher_name_reservations',
        'recovery_executions', 'recovery_execution_vouchers', 'recovery_identity_supersessions'
    ] LOOP
        IF to_regclass(format('public.%I', tbl)) IS NOT NULL THEN
            EXECUTE format('SELECT EXISTS (SELECT 1 FROM public.%I)', tbl) INTO populated;
            IF populated THEN
                RAISE EXCEPTION 'cannot drop recovery table %: it holds rows', tbl;
            END IF;
        END IF;
    END LOOP;
END $$;

-- Triggers first: they call the functions below.
DROP TRIGGER IF EXISTS identity_claims_recovery_retention ON identity_claims;
DROP TRIGGER IF EXISTS recovery_execution_link_guard ON recovery_execution_vouchers;
DROP TRIGGER IF EXISTS recovery_execution_guard ON recovery_executions;
DROP TRIGGER IF EXISTS recovery_supersession_history_guard ON recovery_identity_supersessions;
DROP TRIGGER IF EXISTS users_recovery_identity_guard ON users;
DROP TRIGGER IF EXISTS vouchers_recovery_guard ON vouchers;
DROP TRIGGER IF EXISTS vouchers_recovery_retirement_guard ON vouchers;
DROP TRIGGER IF EXISTS zz_vouchers_recovery_link ON vouchers;

-- The catalog identity trigger keeps working throughout; only its body changes
-- back, before the tables it consulted are dropped.
CREATE OR REPLACE FUNCTION sync_catalog_identity() RETURNS TRIGGER LANGUAGE plpgsql AS $$
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

DROP FUNCTION IF EXISTS protect_recovery_voucher();
DROP FUNCTION IF EXISTS link_recovery_voucher();
DROP FUNCTION IF EXISTS retain_recovery_identity();
DROP FUNCTION IF EXISTS protect_recovery_execution_link();
DROP FUNCTION IF EXISTS protect_recovery_execution();
DROP FUNCTION IF EXISTS protect_recovered_identity();
DROP FUNCTION IF EXISTS prevent_recovery_source_restore();

DROP TABLE IF EXISTS recovery_identity_supersessions;
DROP TABLE IF EXISTS recovery_execution_vouchers;
DROP TABLE IF EXISTS recovery_executions;
DROP TABLE IF EXISTS voucher_name_reservations;
DROP TABLE IF EXISTS voucher_recoveries;
DROP TABLE IF EXISTS recovery_campaigns;

---- create above / drop below ----

-- The dropped objects were never part of this codebase and are not recreated.
SELECT 1;
