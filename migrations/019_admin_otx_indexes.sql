-- Indexes for the operator console's OTX page, which lists every origin
-- transaction newest first and narrows by type, dispatch status, tracking ID,
-- or transaction hash.
--
-- The unfiltered page needs nothing: it orders by otx.id, so it is a backward
-- primary-key scan. A filtered page is the case that would otherwise sort the
-- whole table to find one page, because the filter is selective on a column the
-- primary key says nothing about.
--
-- otx_type is paired with id so the filter and the ordering come from one index
-- and the keyset window stays a range scan. dispatch.status has no such pairing
-- available — the ordering column lives in the other table — so it is a plain
-- btree that narrows before the join.
CREATE INDEX IF NOT EXISTS otx_type_id_idx
    ON otx (otx_type, id DESC);

CREATE INDEX IF NOT EXISTS dispatch_status_idx
    ON dispatch (status);

-- `q` matches opaque identifiers as case-insensitive prefixes (two characters)
-- or substrings (three or more). pg_trgm supports both under pgx's cached
-- statements, while a B-tree cannot derive a range from a generic LIKE
-- parameter. The extension was installed by migration 017, but is repeated so
-- this migration remains valid if earlier history is squashed.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE INDEX IF NOT EXISTS otx_tracking_id_trgm_idx
    ON otx USING gin (lower(tracking_id::text) gin_trgm_ops);

CREATE INDEX IF NOT EXISTS otx_tx_hash_trgm_idx
    ON otx USING gin (lower(tx_hash) gin_trgm_ops);

ANALYZE otx;
