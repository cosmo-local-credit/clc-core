-- Exact occupancy reads for voucher and pool display names. Unlike a symbol,
-- which the client generates and suffixes around a collision, a name is typed by
-- the user and a duplicate is rejected outright -- so these back an equality
-- probe rather than the prefix scan 034 added.
--
-- The indexed expression is the normalized label: trimmed, inner whitespace
-- collapsed, lowercased. It must stay byte-identical to the predicate in
-- voucher-display-name-taken / pool-display-name-taken or the planner will fall
-- back to a sequential scan.
--
-- Deliberately NOT unique. deployed_tokens rows are written by the River worker
-- after the deploy transaction is already signed, so a unique violation there
-- would fire too late to reject; and the catalog already holds duplicates from
-- before this check existed, which a unique index could not be built over.
-- Rejection lives in the deploy and rename handlers instead.
CREATE INDEX IF NOT EXISTS vouchers_normalized_name_idx
    ON vouchers (lower(regexp_replace(btrim(COALESCE(display_name, name)), '\s+', ' ', 'g')));

CREATE INDEX IF NOT EXISTS pools_normalized_name_idx
    ON pools (lower(regexp_replace(btrim(COALESCE(display_name, name)), '\s+', ' ', 'g')));

-- FAILED deploys are deliberately reusable, as with symbol occupancy.
CREATE INDEX IF NOT EXISTS deployed_tokens_open_normalized_name_idx
    ON deployed_tokens (lower(regexp_replace(btrim(name), '\s+', ' ', 'g')))
    WHERE status IN ('PENDING', 'CONFIRMED');

CREATE INDEX IF NOT EXISTS deployed_pools_open_normalized_name_idx
    ON deployed_pools (lower(regexp_replace(btrim(name), '\s+', ' ', 'g')))
    WHERE status IN ('PENDING', 'CONFIRMED');

ANALYZE vouchers;
ANALYZE pools;
ANALYZE deployed_tokens;
ANALYZE deployed_pools;
