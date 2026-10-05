-- normalize_identity_name is SQL-inlined during query planning. Keep these
-- expressions identical to the expanded recipient search predicates.
CREATE INDEX users_recipient_name_trgm_idx
    ON users USING gin (lower(regexp_replace(btrim(username), '\s+', ' ', 'g')) gin_trgm_ops)
    WHERE username IS NOT NULL AND status = 'APPROVED';
CREATE INDEX pools_recipient_name_trgm_idx
    ON pools USING gin (lower(regexp_replace(btrim(COALESCE(display_name, name)), '\s+', ' ', 'g')) gin_trgm_ops);

-- Cleanup visits pending deployment owners, never the full catalog population.
CREATE INDEX identity_claims_deployment_owner_idx ON identity_claims (owner_key)
    WHERE starts_with(owner_key, 'deploy:');

---- create above / drop below ----

DROP INDEX identity_claims_deployment_owner_idx;
DROP INDEX pools_recipient_name_trgm_idx;
DROP INDEX users_recipient_name_trgm_idx;
