-- Supporting indexes for the post management reads
-- (GET /api/v2/users/me/reports and GET /api/v2/admin/reports).
--
-- Every report index so far is scoped to approved rows — 009's keyset index
-- leads with status, 012's trigram indexes are partial on it — because the
-- public feed has never returned anything else. A management list returns all
-- four statuses, and its default shape is the queue, status = 'SUBMITTED'.
--
-- 009's reports_status_updated_idx already serves a status-filtered page:
-- (status, updated_at DESC, id DESC) matches the predicate and the ORDER BY
-- exactly, and the console's default queue is status = 'SUBMITTED'.
--
-- Deliberately no bare (updated_at DESC, id DESC) index for the console's "All"
-- view. Measured at the 5x report seed, adding one took the feed's own plan:
-- both list-reports statements switched to it and demoted status = 'APPROVED'
-- to a row filter, which is what 009 exists to prevent
-- (TestUnfilteredReportFeedKeepsItsMeasuredPlan fails). "All" sorts instead —
-- one operator view over thousands of rows, against every feed request.

-- One author's own posts, in every status. lower() because the handler compares
-- case-insensitively, matching how migration 018 indexes owner addresses.
CREATE INDEX IF NOT EXISTS reports_author_updated_idx
    ON reports (lower(author_address), updated_at DESC, id DESC);

-- 012's trigram indexes are partial on status = 'APPROVED', so a management
-- search — which exists to find a SUBMITTED post by title — cannot use either
-- and degrades to a sequential scan with a LIKE filter. Same expressions
-- without the visibility predicate. The partial pair is kept: it is smaller and
-- remains the better choice for the public feed's own search.
CREATE INDEX IF NOT EXISTS reports_title_trgm_idx
    ON reports USING gin (lower(title) gin_trgm_ops);

CREATE INDEX IF NOT EXISTS reports_description_trgm_idx
    ON reports USING gin (lower(description) gin_trgm_ops);

-- Expression-index statistics only exist once the index does. Plain CREATE
-- INDEX because tern wraps each migration in a transaction, as in 011 and 012.
ANALYZE reports;
