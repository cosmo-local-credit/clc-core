-- Username is the fifth column GET /api/v2/admin/users searches
-- (internal/store/user_search.go). The expression is lower(username), the same
-- form as the four indexes in 018, so a generic plan can bitmap-scan
-- `lower(username) LIKE ...`.
--
-- users_recipient_name_trgm_idx cannot serve this search. It indexes
-- normalize_identity_name(username) and only where status is APPROVED, while an
-- operator must be able to find a pending or rejected account by the username
-- stored on the row.
--
-- Unconditional, for the same reason as 018: an account carries no visibility
-- flag. NULL usernames are simply absent from the index, and a NULL never
-- matches the LIKE, so they need no predicate here.
CREATE INDEX IF NOT EXISTS users_username_trgm_idx
    ON users USING gin (lower(username) gin_trgm_ops);

-- Expression-index statistics only exist once the index does.
ANALYZE users;

---- create above / drop below ----

DROP INDEX IF EXISTS users_username_trgm_idx;
