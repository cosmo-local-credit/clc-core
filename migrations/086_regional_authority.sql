-- The PostGIS package must be available on the database host. If the migrator
-- cannot create extensions, install it with the database operator first.
CREATE EXTENSION IF NOT EXISTS postgis;

ALTER TYPE user_role ADD VALUE IF NOT EXISTS 'REGIONAL_ADMIN' AFTER 'USER';

CREATE TABLE marketplace_regions (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    country_code TEXT CHECK (country_code ~ '^[A-Z]{3}$'),
    boundary geometry(MultiPolygon, 4326) NOT NULL CHECK (ST_IsValid(boundary) AND NOT ST_IsEmpty(boundary)),
    active BOOLEAN NOT NULL DEFAULT FALSE,
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision BETWEEN 1 AND 9007199254740991)
);

CREATE INDEX marketplace_regions_active_boundary_idx ON marketplace_regions
USING GIST (boundary) WHERE active;

CREATE TABLE marketplace_region_assignments (
    region_id BIGINT NOT NULL REFERENCES marketplace_regions (id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    PRIMARY KEY (region_id, user_id)
);

CREATE INDEX marketplace_region_assignments_user_idx ON marketplace_region_assignments (user_id, region_id);

CREATE TABLE marketplace_region_events (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    region_id BIGINT NOT NULL REFERENCES marketplace_regions (id),
    actor_id BIGINT REFERENCES users (id) ON DELETE SET NULL,
    subject_id BIGINT REFERENCES users (id) ON DELETE SET NULL,
    event TEXT NOT NULL CHECK (event IN ('CREATED', 'UPDATED', 'ASSIGNED', 'REMOVED')),
    revision BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

---- create above / drop below ----

UPDATE users SET role = 'USER' WHERE role = 'REGIONAL_ADMIN';
DROP TABLE marketplace_region_events;
DROP TABLE marketplace_region_assignments;
DROP TABLE marketplace_regions;
-- Keep the enum value and PostGIS installed. Dropping and recreating user_role
-- or PostGIS here would also touch objects outside this feature.
