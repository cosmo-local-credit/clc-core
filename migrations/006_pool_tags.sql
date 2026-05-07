-- Pool tag junction tables, referencing the shared token_tag lookup.
CREATE TABLE IF NOT EXISTS pool_deploy_meta_tags (
    otx_id    INT  REFERENCES pool_deploy_meta(otx_id) ON DELETE CASCADE NOT NULL,
    tag_value TEXT REFERENCES token_tag(value) NOT NULL,
    PRIMARY KEY (otx_id, tag_value)
);

CREATE TABLE IF NOT EXISTS deployed_pool_tags (
    deployed_pool_id INT  REFERENCES deployed_pools(id) ON DELETE CASCADE NOT NULL,
    tag_value        TEXT REFERENCES token_tag(value) NOT NULL,
    PRIMARY KEY (deployed_pool_id, tag_value)
);
