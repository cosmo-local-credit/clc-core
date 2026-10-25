-- Viewport-bounded discovery filters with `point(geo_lng, geo_lat) <@ box(...)`.
-- A B-tree on (geo_lat, geo_lng) cannot drive that predicate, so bounded Wants
-- and Report (post) pages fell back to a sequential scan. These GiST indexes use
-- the same core point_ops class as vouchers_geo_gist_idx (008) and
-- pools_geo_gist_idx (044); PostGIS is not required.
--
-- Wants: not partial. The box test is strict, and the lifecycle predicate
-- (status, starts_at, expires_at) is time-dependent, so it cannot be expressed
-- in an index predicate anyway.
CREATE INDEX IF NOT EXISTS wants_geo_gist_idx
    ON wants USING gist (point(geo_lng, geo_lat));

-- Reports: partial on `status = 'APPROVED'` to match the feed's visibility
-- predicate, as 012 does, so the index holds only rows a feed can return. Rows
-- without coordinates are skipped because the box test never matches them.
CREATE INDEX IF NOT EXISTS reports_approved_geo_gist_idx
    ON reports USING gist (point(geo_lng, geo_lat))
    WHERE status = 'APPROVED' AND geo_lat IS NOT NULL AND geo_lng IS NOT NULL;
