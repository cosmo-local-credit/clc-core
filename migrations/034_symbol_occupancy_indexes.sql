-- Exact/prefix occupancy reads for generated voucher and pool symbols. The
-- existing trigram indexes serve human catalog search and do not cover the
-- in-flight deployment tables.
CREATE INDEX IF NOT EXISTS vouchers_symbol_upper_idx
    ON vouchers (upper(symbol));

CREATE INDEX IF NOT EXISTS pools_symbol_upper_idx
    ON pools (upper(symbol));

CREATE INDEX IF NOT EXISTS deployed_tokens_open_symbol_upper_idx
    ON deployed_tokens (upper(symbol))
    WHERE status IN ('PENDING', 'CONFIRMED');

CREATE INDEX IF NOT EXISTS deployed_pools_open_symbol_upper_idx
    ON deployed_pools (upper(symbol))
    WHERE status IN ('PENDING', 'CONFIRMED');

ANALYZE vouchers;
ANALYZE pools;
ANALYZE deployed_tokens;
ANALYZE deployed_pools;
