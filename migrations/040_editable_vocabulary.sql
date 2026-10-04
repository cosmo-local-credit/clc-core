-- Preserve catalog associations when a vocabulary value is renamed.
--
-- The existing keys are found by what they reference, not by name. They were
-- declared inline, so their names are Postgres's defaults; a database whose
-- history named one differently must neither fail this upgrade nor keep a
-- second, non-cascading key that would reject every rename.
DO $$
DECLARE
    fk RECORD;
BEGIN
    FOR fk IN
        SELECT conrelid::regclass AS referencing, conname
        FROM pg_constraint
        WHERE contype = 'f'
          AND confrelid IN ('token_tag'::regclass, 'offering_unit'::regclass)
          AND conrelid IN (
              'deployed_token_tags'::regclass,
              'deployed_pool_tags'::regclass,
              'voucher_tags'::regclass,
              'pool_tags'::regclass,
              'report_tags'::regclass,
              'offering_tags'::regclass,
              'offerings'::regclass
          )
    LOOP
        EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I', fk.referencing, fk.conname);
    END LOOP;
END $$;

ALTER TABLE deployed_token_tags
    ADD CONSTRAINT deployed_token_tags_tag_value_fkey
        FOREIGN KEY (tag_value) REFERENCES token_tag(value) ON UPDATE CASCADE;

ALTER TABLE deployed_pool_tags
    ADD CONSTRAINT deployed_pool_tags_tag_value_fkey
        FOREIGN KEY (tag_value) REFERENCES token_tag(value) ON UPDATE CASCADE;

ALTER TABLE voucher_tags
    ADD CONSTRAINT voucher_tags_tag_value_fkey
        FOREIGN KEY (tag_value) REFERENCES token_tag(value) ON UPDATE CASCADE;

ALTER TABLE pool_tags
    ADD CONSTRAINT pool_tags_tag_value_fkey
        FOREIGN KEY (tag_value) REFERENCES token_tag(value) ON UPDATE CASCADE;

ALTER TABLE report_tags
    ADD CONSTRAINT report_tags_tag_value_fkey
        FOREIGN KEY (tag_value) REFERENCES token_tag(value) ON UPDATE CASCADE;

ALTER TABLE offering_tags
    ADD CONSTRAINT offering_tags_tag_value_fkey
        FOREIGN KEY (tag_value) REFERENCES token_tag(value) ON UPDATE CASCADE;

ALTER TABLE offerings
    ADD CONSTRAINT offerings_unit_fk
        -- Preserve the legacy free-text units grandfathered by migration 023.
        FOREIGN KEY (unit) REFERENCES offering_unit(value) ON UPDATE CASCADE NOT VALID;
