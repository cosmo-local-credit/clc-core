-- Preserve catalog associations when a vocabulary value is renamed.
ALTER TABLE deployed_token_tags
    DROP CONSTRAINT deployed_token_tags_tag_value_fkey,
    ADD CONSTRAINT deployed_token_tags_tag_value_fkey
        FOREIGN KEY (tag_value) REFERENCES token_tag(value) ON UPDATE CASCADE;

ALTER TABLE deployed_pool_tags
    DROP CONSTRAINT deployed_pool_tags_tag_value_fkey,
    ADD CONSTRAINT deployed_pool_tags_tag_value_fkey
        FOREIGN KEY (tag_value) REFERENCES token_tag(value) ON UPDATE CASCADE;

ALTER TABLE voucher_tags
    DROP CONSTRAINT voucher_tags_tag_value_fkey,
    ADD CONSTRAINT voucher_tags_tag_value_fkey
        FOREIGN KEY (tag_value) REFERENCES token_tag(value) ON UPDATE CASCADE;

ALTER TABLE pool_tags
    DROP CONSTRAINT pool_tags_tag_value_fkey,
    ADD CONSTRAINT pool_tags_tag_value_fkey
        FOREIGN KEY (tag_value) REFERENCES token_tag(value) ON UPDATE CASCADE;

ALTER TABLE report_tags
    DROP CONSTRAINT report_tags_tag_value_fkey,
    ADD CONSTRAINT report_tags_tag_value_fkey
        FOREIGN KEY (tag_value) REFERENCES token_tag(value) ON UPDATE CASCADE;

ALTER TABLE offering_tags
    DROP CONSTRAINT offering_tags_tag_value_fkey,
    ADD CONSTRAINT offering_tags_tag_value_fkey
        FOREIGN KEY (tag_value) REFERENCES token_tag(value) ON UPDATE CASCADE;

ALTER TABLE offerings
    DROP CONSTRAINT offerings_unit_fk,
    ADD CONSTRAINT offerings_unit_fk
        -- Preserve the legacy free-text units grandfathered by migration 023.
        FOREIGN KEY (unit) REFERENCES offering_unit(value) ON UPDATE CASCADE NOT VALID;
