-- Indexes for the per-account catalog relationships the operator console reads:
-- vouchers by owner, reports by author, and the pools those vouchers belong to.
--
-- Expression indexes on lower() because the comparisons are case-folded. An
-- address reaches these columns from several sources — a checksummed deploy, a
-- lowercased URL, an operator paste — and `reports_author_created_idx` is a plain
-- btree on author_address, which a lower() comparison cannot use.
--
-- Not measured at representative scale: the local catalog holds 2,711 vouchers and
-- 4,632 reports, where the planner may reasonably scan either. These are equality
-- lookups on a selective column, which is the case a btree serves without needing
-- a measurement to justify it.
CREATE INDEX IF NOT EXISTS vouchers_owner_lower_idx
    ON vouchers (lower(owner_address));

CREATE INDEX IF NOT EXISTS reports_author_lower_idx
    ON reports (lower(author_address));

-- Expression-index statistics only exist once the index does.
ANALYZE vouchers;
ANALYZE reports;
