-- Existing token/pool allowances are preserved. Over-limit resources stay intact.
ALTER TABLE users ALTER COLUMN max_tokens SET DEFAULT 1;
ALTER TABLE users
    ADD COLUMN max_catalogs INT NOT NULL DEFAULT 1 CHECK (max_catalogs >= 0),
    ADD COLUMN max_offers INT NOT NULL DEFAULT 3 CHECK (max_offers >= 0);

ALTER TABLE catalogs DROP CONSTRAINT catalogs_one_per_owner;
CREATE INDEX catalogs_owner_idx ON catalogs(owner_user_id);
CREATE INDEX offerings_available_seller_idx ON offerings(lower(seller_address)) WHERE available;

---- create above / drop below ----
-- Refuse rollback if multiple catalogs exist; never delete user content.
ALTER TABLE catalogs ADD CONSTRAINT catalogs_one_per_owner UNIQUE(owner_user_id);
DROP INDEX catalogs_owner_idx;
DROP INDEX offerings_available_seller_idx;
ALTER TABLE users DROP COLUMN max_catalogs, DROP COLUMN max_offers;
ALTER TABLE users ALTER COLUMN max_tokens SET DEFAULT 20;
