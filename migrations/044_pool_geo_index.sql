-- Nearby pool browsing prefilters by a lat/lng box so the GiST point index can
-- drive it, the same core point_ops class vouchers_geo_gist_idx uses (008).
-- PostGIS is not installed and is not required.
--
-- Not partial: the box test is strict, so a pool without coordinates never
-- matches it, and a plain index keeps the planner's proof trivial.
CREATE INDEX IF NOT EXISTS pools_geo_gist_idx
    ON pools USING gist (point(geo_lng, geo_lat));
