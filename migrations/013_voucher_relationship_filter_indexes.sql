-- Cover the reverse direction used when voucher membership drives report and
-- pool browsing. The original single-column indexes can locate a voucher's
-- junction rows, but these shapes also need the entity ID to build candidates
-- and count relationship facets without visiting every heap tuple.

CREATE INDEX IF NOT EXISTS report_vouchers_voucher_report_idx
    ON report_vouchers (voucher_id, report_id);

-- "Pool contains voucher" follows the catalog's existing public semantics:
-- only currently allowed composition rows count. Keeping the index partial
-- makes that predicate exact and excludes historical disallowed rows.
CREATE INDEX IF NOT EXISTS pool_tokens_allowed_voucher_pool_idx
    ON pool_tokens (voucher_id, pool_id)
    WHERE is_allowed;

ANALYZE report_vouchers;
ANALYZE pool_tokens;
