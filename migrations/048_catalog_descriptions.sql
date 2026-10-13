ALTER TABLE catalogs
    ADD COLUMN description TEXT,
    ADD CONSTRAINT catalogs_description_valid CHECK (
        description IS NULL
        OR (description = btrim(description) AND char_length(description) BETWEEN 1 AND 500)
    );

CREATE INDEX catalogs_name_trgm_idx ON catalogs USING gin (lower(name) gin_trgm_ops);
CREATE INDEX catalogs_location_trgm_idx ON catalogs USING gin (lower(location_name) gin_trgm_ops);
CREATE INDEX catalogs_description_trgm_idx ON catalogs USING gin (lower(description) gin_trgm_ops)
    WHERE description IS NOT NULL;

---- create above / drop below ----
DROP INDEX catalogs_description_trgm_idx;
DROP INDEX catalogs_location_trgm_idx;
DROP INDEX catalogs_name_trgm_idx;
ALTER TABLE catalogs DROP CONSTRAINT catalogs_description_valid;
ALTER TABLE catalogs DROP COLUMN description;
