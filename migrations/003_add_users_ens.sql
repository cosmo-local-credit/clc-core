-- Add the missing ENS identity field and indexes used by Send-to prefix search.

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS ens TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS users_ens_lower_unique_idx
    ON users (LOWER(ens))
    WHERE ens IS NOT NULL;

CREATE INDEX IF NOT EXISTS users_phone_approved_prefix_idx
    ON users USING spgist (phone)
    WHERE phone IS NOT NULL AND status = 'APPROVED';

CREATE INDEX IF NOT EXISTS users_email_lower_approved_prefix_idx
    ON users USING spgist (LOWER(email))
    WHERE email IS NOT NULL AND status = 'APPROVED';

CREATE INDEX IF NOT EXISTS users_ens_lower_approved_prefix_idx
    ON users USING spgist (LOWER(ens))
    WHERE ens IS NOT NULL AND status = 'APPROVED';
