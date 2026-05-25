-- Migration 008: add phone and country to users; relax email uniqueness for
-- WebAuthn-only accounts that register without an email address.

ALTER TABLE users ADD COLUMN IF NOT EXISTS phone   TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS country TEXT NOT NULL DEFAULT '';

-- Remove the UNIQUE constraint on email so accounts created via the relayer
-- flow (with no email yet) don't collide.
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_email_key;
