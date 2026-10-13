-- A curated catalog is independent of pool membership and all on-chain state.
CREATE TABLE catalogs (
    id INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    owner_user_id INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    image_url TEXT,
    location_name TEXT NOT NULL,
    geo_lat FLOAT8 NOT NULL,
    geo_lng FLOAT8 NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT catalogs_one_per_owner UNIQUE (owner_user_id),
    CONSTRAINT catalogs_name_valid CHECK (name = btrim(name) AND char_length(name) BETWEEN 1 AND 120),
    CONSTRAINT catalogs_location_valid CHECK (location_name = btrim(location_name) AND char_length(location_name) BETWEEN 1 AND 200),
    CONSTRAINT catalogs_image_url_http CHECK (image_url IS NULL OR image_url ~ '^https?://[^/[:space:]]+'),
    CONSTRAINT catalogs_geo_valid CHECK (geo_lat BETWEEN -90 AND 90 AND geo_lng BETWEEN -180 AND 180)
);
CREATE TRIGGER update_catalogs_timestamp BEFORE UPDATE ON catalogs
    FOR EACH ROW EXECUTE PROCEDURE update_timestamp();
CREATE INDEX catalogs_geo_idx ON catalogs USING gist (point(geo_lng, geo_lat));
CREATE TABLE catalog_pools (
    catalog_id INT NOT NULL REFERENCES catalogs(id) ON DELETE CASCADE,
    pool_id INT NOT NULL REFERENCES pools(id) ON DELETE CASCADE,
    PRIMARY KEY (catalog_id, pool_id)
);
CREATE INDEX catalog_pools_pool_idx ON catalog_pools(pool_id);
CREATE TABLE catalog_vouchers (
    catalog_id INT NOT NULL REFERENCES catalogs(id) ON DELETE CASCADE,
    voucher_id INT NOT NULL REFERENCES vouchers(id) ON DELETE CASCADE,
    PRIMARY KEY (catalog_id, voucher_id)
);
CREATE INDEX catalog_vouchers_voucher_idx ON catalog_vouchers(voucher_id);

---- create above / drop below ----
DROP TABLE catalog_vouchers;
DROP TABLE catalog_pools;
DROP TABLE catalogs;
