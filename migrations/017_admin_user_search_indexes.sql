-- Supporting indexes for the account directory search behind
-- GET /api/v2/admin/users.
--
-- `pg_trgm` for the same reason as 011_catalog_search_indexes.sql: pgx runs
-- these statements under QueryExecModeCacheStatement, so PostgreSQL may plan
-- them generically, and a generic plan cannot derive a B-tree range from
-- `lower(col) LIKE lower($1) || '%'`. The extension is already installed by 011;
-- CREATE EXTENSION is repeated so this migration stands on its own if 011 is
-- ever squashed away.
--
-- These four are the identity and contact columns the search matches
-- (internal/store/user_search.go). Country and role are deliberately not
-- searched, so they are deliberately not indexed.
--
-- Unlike 011 these carry no timing. The development directory holds six
-- accounts, where every plan is a sequential scan and any number taken from it
-- would be noise. What was verified is narrower and worth stating exactly: with
-- `enable_seqscan = off`, the substring predicate the composer emits plans as a
-- Bitmap Index Scan on users_name_trgm_idx with
-- `Index Cond: (lower(name) ~~ '%ada%')`, so the indexed expression is the one
-- the query asks for and the index can serve it. Whether the planner *chooses*
-- it, and what that saves, needs a representative directory — as does the query
-- shape in internal/store/user_search.go, which is inherited from the voucher
-- slice's measurement at 13,181 rows. At six rows a sequential scan is the
-- correct choice and the tests do not assert otherwise.
--
-- The write cost is accepted rather than dismissed: a GIN index is expensive to
-- update, but the rows here change on signup, on a profile edit and on an
-- approval — human-paced events, not the per-block writes the voucher and
-- transaction tables take.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- Unconditional: unlike vouchers, an account carries no visibility flag, and an
-- operator searching the directory must be able to find a rejected account.
CREATE INDEX IF NOT EXISTS users_name_trgm_idx
    ON users USING gin (lower(name) gin_trgm_ops);

CREATE INDEX IF NOT EXISTS users_email_trgm_idx
    ON users USING gin (lower(email) gin_trgm_ops);

CREATE INDEX IF NOT EXISTS users_phone_trgm_idx
    ON users USING gin (lower(phone) gin_trgm_ops);

-- lower(blockchain_address) already carries a unique B-tree
-- (users_blockchain_address_lower_key), which serves equality but not the
-- substring match an operator gets when pasting an address into the search box.
CREATE INDEX IF NOT EXISTS users_blockchain_address_trgm_idx
    ON users USING gin (lower(blockchain_address) gin_trgm_ops);

-- Expression-index statistics only exist once the index does, and the planner
-- needs them to cost `lower(name)` against a trigram index at all. Plain
-- CREATE INDEX (not CONCURRENTLY) because tern wraps each migration in a
-- transaction; the table is small, so the SHARE lock is brief.
ANALYZE users;
