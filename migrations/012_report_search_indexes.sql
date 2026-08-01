-- Supporting indexes for report search (SEARCH_FILTER_PLAN.md Slice 4).
--
-- The same trigram approach migration 011 established for the voucher and pool
-- catalogs, for the same reason: pgx runs these statements through
-- QueryExecModeCacheStatement, so PostgreSQL may execute them under a generic
-- plan, and a generic plan cannot derive a B-tree range from
-- `lower(title) LIKE lower($1) || '%'`. `pg_trgm` is already installed by 011.
--
-- Title *and* description are indexed because both are useful targets and both
-- are ordinary columns on `reports`. Report **body** search stays parked
-- (PG_SEARCH_PLAN.md): the body is normalized into a separate structure by
-- migration 010, and searching it is a different problem with a different index
-- shape. Title and description are the fields a reader scans in the feed, so
-- they are what a feed search should match.
--
-- Partial on `status = 'APPROVED'` to match the feed's visibility predicate
-- exactly, so the index holds only rows a search can return. The report feed has
-- never shown anything else, and search does not change that.
CREATE INDEX IF NOT EXISTS reports_approved_title_trgm_idx
    ON reports USING gin (lower(title) gin_trgm_ops)
    WHERE status = 'APPROVED';

CREATE INDEX IF NOT EXISTS reports_approved_description_trgm_idx
    ON reports USING gin (lower(description) gin_trgm_ops)
    WHERE status = 'APPROVED';

-- The reverse junction index this slice needs already exists:
-- `report_tags_tag_report_idx` on (tag_value, report_id) was added by migration
-- 011 to complete the junction access pattern, before there was a filter to use
-- it. Nothing to add here.

-- Expression-index statistics only exist once the index does, and the planner
-- needs them to cost `lower(title)` against a trigram index at all. Plain
-- CREATE INDEX (not CONCURRENTLY) because tern wraps each migration in a
-- transaction, matching migration 011.
ANALYZE reports;
