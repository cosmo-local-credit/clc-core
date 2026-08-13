-- Supporting indexes for the public market voucher catalog
-- (GET /api/v2/vouchers).

-- The catalog derives each voucher's marker kind from a lateral lookup over
-- its available offerings. This index turns that per-page lookup into an
-- index-only scan: measured at ~5.18 ms before and ~1.785 ms after for a
-- 500-row page. Every offering is currently available, so the partial
-- predicate is a prospective size and write benefit rather than a selectivity
-- one; it is included because unavailable history will grow.
CREATE INDEX IF NOT EXISTS offerings_available_voucher_type_idx
    ON offerings (voucher_id, commodity_type)
    WHERE available;

-- Optional viewport filtering uses the core PostgreSQL GiST point_ops class.
-- PostGIS is not installed and is not required. On the measured Nairobi
-- viewport this replaces a scan removing 2,595 rows at ~0.830 ms with a bitmap
-- index scan at ~0.235 ms.
--
-- No separate partial B-tree index on vouchers(id) is added: the primary key
-- already drives the unbounded descending page scan efficiently.
CREATE INDEX IF NOT EXISTS vouchers_geo_gist_idx
    ON vouchers USING gist (point(geo_lng, geo_lat))
    WHERE active AND geo_lat IS NOT NULL;
