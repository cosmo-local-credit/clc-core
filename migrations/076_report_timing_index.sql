-- Past and bounded-future Post discovery filter on period_to. Open-ended Posts
-- continue to use the existing approved feed index.
CREATE INDEX reports_approved_period_to_idx
    ON reports (period_to)
    WHERE status = 'APPROVED' AND period_to IS NOT NULL;

ANALYZE reports;

---- create above / drop below ----

DROP INDEX reports_approved_period_to_idx;
