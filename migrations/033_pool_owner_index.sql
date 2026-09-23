-- Index for the per-account pool list behind GET /api/v2/pools?owner=.
--
-- Same reasoning as 019_account_relationship_indexes.sql, which indexed
-- vouchers and reports by their owning address but not pools: nothing filtered
-- pools by owner until the wallet gained a "Your pools" section. The expression
-- matches ownerPredicate's case-folded comparison, because an address reaches
-- this column from a checksummed deploy, a lowercased URL, or an operator paste.
--
-- Equality on a selective column, so a btree serves it without needing a
-- measurement to justify it.
CREATE INDEX IF NOT EXISTS pools_owner_lower_idx
    ON pools (lower(owner_address));

-- Expression-index statistics only exist once the index does.
ANALYZE pools;
