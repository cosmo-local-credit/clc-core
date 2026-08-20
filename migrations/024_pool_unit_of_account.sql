-- Pool deployment requests now carry their unit of account through the
-- asynchronous deployment registry before promotion into the network catalog.
-- Existing and in-flight deploys remain valid with a NULL value.

ALTER TABLE deployed_pools
    ADD COLUMN IF NOT EXISTS unit_of_account TEXT;

---- create above / drop below ----

ALTER TABLE deployed_pools
    DROP COLUMN IF EXISTS unit_of_account;
