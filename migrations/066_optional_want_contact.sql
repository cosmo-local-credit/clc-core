ALTER TABLE wants
    DROP CONSTRAINT IF EXISTS wants_has_contact;

---- create above / drop below ----

ALTER TABLE wants
    ADD CONSTRAINT wants_has_contact CHECK (
        contact_permission <> 0 OR NULLIF(BTRIM(contact_text), '') IS NOT NULL
    ) NOT VALID;
