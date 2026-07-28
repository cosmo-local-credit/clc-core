-- Supporting index for the report feed (GET /api/v2/reports).

-- The feed is ordered by updated_at, not by id: measured on the imported
-- catalog, updated_at DESC and id DESC agree on only 4 of 3,730 approved rows
-- and drift by as much as 2,606 positions, so id order is not "latest". The
-- existing reports_status_created_idx covers created_at and cannot serve this
-- ordering.
--
-- Column order matches the keyset predicate exactly — (status = 'APPROVED')
-- then (updated_at, id) < (cursor_updated_at, cursor_id) — so both the first
-- page and every cursor page are an index scan with no sort.
CREATE INDEX IF NOT EXISTS reports_status_updated_idx
    ON reports (status, updated_at DESC, id DESC);
