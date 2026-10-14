CREATE TABLE catalog_tags (
    catalog_id BIGINT NOT NULL REFERENCES catalogs(id) ON DELETE CASCADE,
    tag_value TEXT NOT NULL REFERENCES token_tag(value) ON UPDATE CASCADE,
    PRIMARY KEY (catalog_id, tag_value)
);

CREATE INDEX catalog_tags_tag_catalog_idx ON catalog_tags(tag_value, catalog_id);
