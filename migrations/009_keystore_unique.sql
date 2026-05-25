-- Migration 009: add UNIQUE constraint on keystore.public_key to prevent
-- duplicate rows when Bootstrap is called more than once.

ALTER TABLE keystore ADD CONSTRAINT keystore_public_key_key UNIQUE (public_key);
