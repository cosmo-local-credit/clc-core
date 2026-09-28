-- Storage only: membership transitions, HTTP scope and catalog filtering are
-- subsequent changes. Existing users, imports and queued deployments keep NULL
-- provenance until an explicit assignment is made.
CREATE TABLE domains (
    id              INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    slug            TEXT NOT NULL UNIQUE,
    name            TEXT NOT NULL CHECK (btrim(name) <> ''),
    description     TEXT NOT NULL DEFAULT '',
    is_global       BOOLEAN NOT NULL DEFAULT false,
    status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT domains_slug_format CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$' AND length(slug) <= 63)
);

CREATE UNIQUE INDEX domains_one_global_key ON domains (is_global) WHERE is_global;

CREATE TABLE domain_hosts (
    id              INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    domain_id       INT NOT NULL UNIQUE REFERENCES domains(id),
    hostname        TEXT NOT NULL UNIQUE,
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'verified', 'active', 'disabled')),
    verified_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    -- Store normalized ASCII/IDNA hostnames, without scheme, port or final dot.
    CONSTRAINT domain_hosts_hostname_format CHECK (
        length(hostname) <= 253 AND
        hostname ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$'
    ),
    CONSTRAINT domain_hosts_verified_state CHECK (
        status NOT IN ('verified', 'active') OR verified_at IS NOT NULL
    )
);
CREATE TABLE user_domains (
    user_id    INT NOT NULL REFERENCES users(id),
    domain_id  INT NOT NULL REFERENCES domains(id),
    status     TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'left', 'suspended')),
    joined_at  TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    left_at    TIMESTAMPTZ,
    PRIMARY KEY (user_id, domain_id),
    CONSTRAINT user_domains_left_at_valid CHECK (
        (status = 'left') = (left_at IS NOT NULL) AND
        (left_at IS NULL OR left_at >= joined_at)
    )
);
CREATE INDEX user_domains_active_domain_idx
    ON user_domains (domain_id, user_id) WHERE status = 'active';

ALTER TABLE users
    ADD COLUMN registration_domain_id INT REFERENCES domains(id),
    ADD COLUMN domain_revision BIGINT NOT NULL DEFAULT 0 CHECK (domain_revision >= 0);
CREATE INDEX users_registration_domain_idx ON users (registration_domain_id)
    WHERE registration_domain_id IS NOT NULL;

ALTER TABLE vouchers ADD COLUMN origin_domain_id INT REFERENCES domains(id);
ALTER TABLE pools ADD COLUMN origin_domain_id INT REFERENCES domains(id);
ALTER TABLE reports ADD COLUMN origin_domain_id INT REFERENCES domains(id);
ALTER TABLE deployed_tokens ADD COLUMN origin_domain_id INT REFERENCES domains(id);
ALTER TABLE deployed_pools ADD COLUMN origin_domain_id INT REFERENCES domains(id);

CREATE INDEX vouchers_origin_domain_id_idx ON vouchers (origin_domain_id, id DESC);
CREATE INDEX pools_origin_domain_id_idx ON pools (origin_domain_id, id DESC);
CREATE INDEX reports_origin_domain_id_idx ON reports (origin_domain_id, id);
CREATE INDEX reports_origin_domain_feed_idx ON reports (origin_domain_id, updated_at DESC, id DESC)
    WHERE status = 'APPROVED';
CREATE INDEX deployed_tokens_origin_domain_idx ON deployed_tokens (origin_domain_id);
CREATE INDEX deployed_pools_origin_domain_idx ON deployed_pools (origin_domain_id);

CREATE TABLE domain_audit_events (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    domain_id    INT NOT NULL REFERENCES domains(id),
    actor_key    TEXT NOT NULL CHECK (btrim(actor_key) <> ''),
    action       TEXT NOT NULL CHECK (btrim(action) <> ''),
    entity_type  TEXT NOT NULL CHECK (btrim(entity_type) <> ''),
    entity_key   TEXT NOT NULL CHECK (btrim(entity_key) <> ''),
    before_state JSONB,
    after_state  JSONB,
    reason       TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT domain_audit_before_valid CHECK (
        before_state IS NULL OR (jsonb_typeof(before_state) = 'object' AND octet_length(before_state::text) <= 16384)
    ),
    CONSTRAINT domain_audit_after_valid CHECK (
        after_state IS NULL OR (jsonb_typeof(after_state) = 'object' AND octet_length(after_state::text) <= 16384)
    )
);
CREATE INDEX domain_audit_events_domain_created_idx
    ON domain_audit_events (domain_id, created_at DESC, id DESC);

-- Allow assigning old NULL provenance once; subsequent ordinary SQL updates
-- cannot clear or move it. No session setting can silently bypass this guard.
CREATE FUNCTION protect_domain_assignment() RETURNS trigger AS $$
BEGIN
    IF to_jsonb(OLD)->>TG_ARGV[0] IS NOT NULL AND
       (to_jsonb(NEW)->>TG_ARGV[0]) IS DISTINCT FROM (to_jsonb(OLD)->>TG_ARGV[0]) THEN
        RAISE EXCEPTION '% on % is immutable once assigned', TG_ARGV[0], TG_TABLE_NAME
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER domain_hosts_domain_immutable BEFORE UPDATE OF domain_id ON domain_hosts
    FOR EACH ROW EXECUTE FUNCTION protect_domain_assignment('domain_id');
CREATE TRIGGER domain_hosts_hostname_immutable BEFORE UPDATE OF hostname ON domain_hosts
    FOR EACH ROW EXECUTE FUNCTION protect_domain_assignment('hostname');
CREATE TRIGGER users_registration_domain_immutable BEFORE UPDATE OF registration_domain_id ON users
    FOR EACH ROW EXECUTE FUNCTION protect_domain_assignment('registration_domain_id');
CREATE TRIGGER vouchers_origin_domain_immutable BEFORE UPDATE OF origin_domain_id ON vouchers
    FOR EACH ROW EXECUTE FUNCTION protect_domain_assignment('origin_domain_id');
CREATE TRIGGER pools_origin_domain_immutable BEFORE UPDATE OF origin_domain_id ON pools
    FOR EACH ROW EXECUTE FUNCTION protect_domain_assignment('origin_domain_id');
CREATE TRIGGER reports_origin_domain_immutable BEFORE UPDATE OF origin_domain_id ON reports
    FOR EACH ROW EXECUTE FUNCTION protect_domain_assignment('origin_domain_id');
CREATE TRIGGER deployed_tokens_origin_domain_immutable BEFORE UPDATE OF origin_domain_id ON deployed_tokens
    FOR EACH ROW EXECUTE FUNCTION protect_domain_assignment('origin_domain_id');
CREATE TRIGGER deployed_pools_origin_domain_immutable BEFORE UPDATE OF origin_domain_id ON deployed_pools
    FOR EACH ROW EXECUTE FUNCTION protect_domain_assignment('origin_domain_id');

CREATE FUNCTION protect_global_domain() RETURNS trigger AS $$
BEGIN
    IF OLD.is_global THEN
        IF TG_OP = 'DELETE' THEN
            RAISE EXCEPTION 'the global Sarafu domain cannot be deleted' USING ERRCODE = '23514';
        END IF;
        IF NEW.is_global IS DISTINCT FROM OLD.is_global OR NEW.status <> 'active' OR NEW.id <> OLD.id THEN
            RAISE EXCEPTION 'the global Sarafu domain identity must remain active' USING ERRCODE = '23514';
        END IF;
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER domains_protect_global BEFORE UPDATE OR DELETE ON domains
    FOR EACH ROW EXECUTE FUNCTION protect_global_domain();

CREATE TRIGGER update_domains_timestamp BEFORE UPDATE ON domains
    FOR EACH ROW EXECUTE FUNCTION update_timestamp();
CREATE TRIGGER update_domain_hosts_timestamp BEFORE UPDATE ON domain_hosts
    FOR EACH ROW EXECUTE FUNCTION update_timestamp();

-- BEGIN SARAFU SEED: repeatable identity, never evidence of DNS verification.
INSERT INTO domains (slug, name, is_global)
SELECT 'sarafu', 'Sarafu', true
WHERE NOT EXISTS (SELECT 1 FROM domains WHERE is_global)
ON CONFLICT (is_global) WHERE is_global DO NOTHING;

INSERT INTO domain_hosts (domain_id, hostname)
SELECT id, 'sarafu.network' FROM domains d WHERE is_global
    AND NOT EXISTS (SELECT 1 FROM domain_hosts h WHERE h.domain_id = d.id)
ON CONFLICT (hostname) DO NOTHING;
-- END SARAFU SEED

---- create above / drop below ----

-- Development rollback only; dropping provenance is not an operational rollback
-- after production enrollment. Drop dependent triggers before their functions.
DROP TABLE domain_audit_events;
DROP TABLE user_domains;
DROP TABLE domain_hosts;
DROP TRIGGER users_registration_domain_immutable ON users;
ALTER TABLE users DROP COLUMN registration_domain_id, DROP COLUMN domain_revision;
DROP TRIGGER vouchers_origin_domain_immutable ON vouchers;
DROP TRIGGER pools_origin_domain_immutable ON pools;
DROP TRIGGER reports_origin_domain_immutable ON reports;
DROP TRIGGER deployed_tokens_origin_domain_immutable ON deployed_tokens;
DROP TRIGGER deployed_pools_origin_domain_immutable ON deployed_pools;
ALTER TABLE vouchers DROP COLUMN origin_domain_id;
ALTER TABLE pools DROP COLUMN origin_domain_id;
ALTER TABLE reports DROP COLUMN origin_domain_id;
ALTER TABLE deployed_tokens DROP COLUMN origin_domain_id;
ALTER TABLE deployed_pools DROP COLUMN origin_domain_id;
DROP TABLE domains;
DROP FUNCTION protect_domain_assignment();
DROP FUNCTION protect_global_domain();
