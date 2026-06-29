-- Migration 010: rename ethereum_address to blockchain_address; make email
-- and phone nullable with UNIQUE constraints; enforce at-least-one-contact
-- via a CHECK constraint.
--
-- Design decisions:
--   * email and phone are both nullable but at least one must be present
--     (CHECK email IS NOT NULL OR phone IS NOT NULL).
--   * PostgreSQL UNIQUE allows multiple NULLs, so users who only provide
--     email (phone IS NULL) or only phone (email IS NULL) don't collide.
--   * Empty strings are normalised to NULL so the UNIQUE constraint isn't
--     defeated by sentinel '' values.
--   * country remains NOT NULL DEFAULT '' (unchanged) — it is not a contact
--     identifier and is not part of the at-least-one-contact rule.

-- 1. Rename ethereum_address -> blockchain_address (idempotent).
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'users' AND column_name = 'ethereum_address'
    ) THEN
        ALTER TABLE users RENAME COLUMN ethereum_address TO blockchain_address;
    END IF;
END $$;

-- 2. Rebuild the address index under the new column name.
DROP INDEX IF EXISTS users_ethereum_address_idx;
CREATE INDEX IF NOT EXISTS users_blockchain_address_idx ON users(blockchain_address);

-- 3. Make email nullable (was NOT NULL from baseline 001).
ALTER TABLE users ALTER COLUMN email DROP NOT NULL;

-- 4. Make phone nullable (was NOT NULL DEFAULT '' from migration 008).
ALTER TABLE users ALTER COLUMN phone DROP NOT NULL;
ALTER TABLE users ALTER COLUMN phone DROP DEFAULT;

-- 5. Normalise empty strings to NULL so UNIQUE works correctly.
UPDATE users SET email = NULL WHERE email = '';
UPDATE users SET phone = NULL WHERE phone = '';

-- 6. Deduplicate existing email/phone values. Migration 008 dropped the
--    email uniqueness constraint, so duplicates may have accumulated.
--    Keep the first occurrence (lowest id); NULL the rest. This runs
--    before the CHECK constraint exists, so NULLing is always safe.
UPDATE users u
SET email = NULL
FROM (
    SELECT email, MIN(id) AS keep_id
    FROM users
    WHERE email IS NOT NULL
    GROUP BY email
    HAVING COUNT(*) > 1
) dup
WHERE u.email = dup.email
  AND u.id <> dup.keep_id;

UPDATE users u
SET phone = NULL
FROM (
    SELECT phone, MIN(id) AS keep_id
    FROM users
    WHERE phone IS NOT NULL
    GROUP BY phone
    HAVING COUNT(*) > 1
) dup
WHERE u.phone = dup.phone
  AND u.id <> dup.keep_id;

-- 7. Add UNIQUE constraints (PostgreSQL permits multiple NULLs).
--    Drop any implicit-name constraints from older schema versions first.
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_email_key;
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_email_unique;
ALTER TABLE users ADD CONSTRAINT users_email_unique UNIQUE (email);

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_phone_key;
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_phone_unique;
ALTER TABLE users ADD CONSTRAINT users_phone_unique UNIQUE (phone);

-- 8. Enforce at least one contact method. NOT VALID grandfathers existing
--    rows that may have been left with neither contact after dedup; the
--    constraint is enforced on all future inserts and updates.
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_at_least_one_contact;
ALTER TABLE users ADD CONSTRAINT users_at_least_one_contact
    CHECK (email IS NOT NULL OR phone IS NOT NULL) NOT VALID;
