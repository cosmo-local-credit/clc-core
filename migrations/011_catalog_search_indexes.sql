-- Supporting indexes for voucher and pool catalog search and tag filtering.
--
-- `pg_trgm` rather than a `text_pattern_ops` B-tree: pgx runs these statements
-- through QueryExecModeCacheStatement, so PostgreSQL may execute them under a
-- generic plan, and a generic plan cannot derive a B-tree range from
-- `lower(name) LIKE lower($1) || '%'`. Trigram GIN indexes accept a runtime
-- parameter and keep an index condition under EXPLAIN (GENERIC_PLAN).
--
-- Creating the index is necessary but not sufficient: whether the planner picks
-- it depends on the query shape. Measured against 13,181 active geolocated
-- vouchers (5x the current catalog) on a *cursor* page, comparing a query that
-- applies `ORDER BY v.id DESC LIMIT n` directly to the search predicate against
-- one that materializes the match set first:
--
--   term                  backward pkey walk         match set materialized
--   common (551 matches)  1.147 ms (1,881 removed)   0.673 ms
--   rare (1 match)        8.706 ms (13,710 removed)  0.085 ms
--   no match              7.749 ms (13,711 removed)  0.075 ms
--
-- Adding the keyset predicate to a query already ordered by `v.id DESC LIMIT n`
-- makes `vouchers_pkey` look like it serves the filter, the ordering, and the
-- early abort together, so the planner takes it and demotes the search to a row
-- filter that scans the catalog. On a first page, with no cursor, both shapes
-- plan and measure identically. `internal/store/catalog_search.go` therefore
-- materializes the match set before ordering, and the tests assert the plan.
--
-- `pg_trgm` is a trusted extension, so a database owner can install it without
-- superuser. `cube`/`earthdistance` are deliberately absent: distance is a
-- later slice.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- Partial on `active` to match the catalog's visibility predicate exactly, so
-- the index holds only rows a search can return. Both name and symbol are
-- indexed because the public search matches either.
CREATE INDEX IF NOT EXISTS vouchers_active_name_trgm_idx
    ON vouchers USING gin (lower(name) gin_trgm_ops)
    WHERE active;

CREATE INDEX IF NOT EXISTS vouchers_active_symbol_trgm_idx
    ON vouchers USING gin (lower(symbol) gin_trgm_ops)
    WHERE active;

-- Pools carry no visibility flag, so these are unconditional.
CREATE INDEX IF NOT EXISTS pools_name_trgm_idx
    ON pools USING gin (lower(name) gin_trgm_ops);

CREATE INDEX IF NOT EXISTS pools_symbol_trgm_idx
    ON pools USING gin (lower(symbol) gin_trgm_ops);

-- Reverse tag indexes. Every junction's primary key is entity-first, which
-- serves "load one entity's tags" but not the tag-first direction that filters
-- and facet aggregation need.
--
-- Voucher tag filtering is offering-derived (direct and
-- derived tags): `voucher_tags` is empty, so `offering_tags` is the junction
-- the voucher facet and filter actually read. `voucher_tags` gets its index
-- anyway so curated voucher tags need no further migration; the pool index
-- serves the direct `pool_tags` filter that Slice 2 ships. The report index
-- completes the junction access pattern — report browsing is out of scope and
-- nothing queries it yet.
--
-- These are small tables (the largest is 584 kB) and the planner is free to
-- choose a sequential scan over any of them; that is the correct choice at this
-- size and the tests do not assert otherwise.
CREATE INDEX IF NOT EXISTS offering_tags_tag_offering_idx
    ON offering_tags (tag_value, offering_id);

CREATE INDEX IF NOT EXISTS voucher_tags_tag_voucher_idx
    ON voucher_tags (tag_value, voucher_id);

CREATE INDEX IF NOT EXISTS pool_tags_tag_pool_idx
    ON pool_tags (tag_value, pool_id);

CREATE INDEX IF NOT EXISTS report_tags_tag_report_idx
    ON report_tags (tag_value, report_id);

-- Expression-index statistics only exist once the index does, and the planner
-- needs them to cost `lower(name)` against a trigram index at all. Plain
-- CREATE INDEX (not CONCURRENTLY) because tern wraps each migration in a
-- transaction; the largest table here is 1.9 MB, so the SHARE lock is brief.
ANALYZE vouchers;
ANALYZE pools;
ANALYZE offering_tags;
ANALYZE voucher_tags;
ANALYZE pool_tags;
ANALYZE report_tags;
