-- A sign-in card's key is the root key of a Calibur-delegated EOA, and Calibur
-- cannot revoke a root key. Replacing a compromised card therefore moves the
-- account to a new address: users.blockchain_address changes in place, so the
-- user id and everything keyed on it stay, and the old address is recorded
-- here. A row is both the audit record of the move and the reason the old
-- address can never become an account again.
CREATE TABLE address_rotations (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id       INT NOT NULL REFERENCES users(id),
    old_address   TEXT NOT NULL CHECK (old_address ~ '^0x[0-9a-fA-F]{40}$'),
    new_address   TEXT NOT NULL CHECK (new_address ~ '^0x[0-9a-fA-F]{40}$'),
    initiated_by  TEXT NOT NULL CHECK (initiated_by IN ('user', 'admin')),
    admin_user_id INT REFERENCES users(id),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT address_rotations_distinct CHECK (lower(old_address) <> lower(new_address))
);

CREATE UNIQUE INDEX address_rotations_old_address_key ON address_rotations (lower(old_address));
CREATE INDEX address_rotations_user_idx ON address_rotations (user_id, created_at DESC);

-- Every path that creates an account or moves one onto an address passes this
-- trigger: signup, sign-in auto-registration, SIWE verify and a rotation's own
-- UPDATE. The advisory lock is the one RotateUserAddress takes on the old
-- address before retiring it, so a sign-in racing a rotation waits for it and
-- then sees the committed row (READ COMMITTED takes a new snapshot per
-- statement) instead of re-registering the address the rotation just retired.
CREATE FUNCTION reject_retired_user_address() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended('user-address:' || lower(NEW.blockchain_address), 0));
    IF EXISTS (SELECT 1 FROM address_rotations WHERE lower(old_address) = lower(NEW.blockchain_address)) THEN
        RAISE EXCEPTION 'sign-in card % was replaced', NEW.blockchain_address
            USING ERRCODE = '23514', CONSTRAINT = 'users_address_retired';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER users_reject_retired_address
    BEFORE INSERT OR UPDATE OF blockchain_address ON users
    FOR EACH ROW EXECUTE FUNCTION reject_retired_user_address();

-- A passkey on a rotated account stays registered on the old wallet only.
-- 'moved' marks a row re-pointed at the new address that the new wallet's root
-- key has yet to register(): never trusted for sign-in, and re-armed to
-- 'pending' by the relay that registers it, like a failed attempt.
ALTER TABLE webauthn_credentials DROP CONSTRAINT webauthn_credentials_state_check;
ALTER TABLE webauthn_credentials
    ADD CONSTRAINT webauthn_credentials_state_check
    CHECK (state IN ('pending', 'active', 'failed', 'legacy', 'unverified', 'moved'));

-- A passkey a rotation carried stays the account's until the new wallet has
-- registered it: a registration that reverts returns it to 'moved', not
-- 'failed', so the account can retry it and a later rotation carries it on.
-- Cleared when a registration of it succeeds.
ALTER TABLE webauthn_credentials ADD COLUMN carried BOOLEAN NOT NULL DEFAULT false;

---- create above / drop below ----

ALTER TABLE webauthn_credentials DROP COLUMN IF EXISTS carried;

UPDATE webauthn_credentials SET state = 'failed' WHERE state = 'moved';
ALTER TABLE webauthn_credentials DROP CONSTRAINT webauthn_credentials_state_check;
ALTER TABLE webauthn_credentials
    ADD CONSTRAINT webauthn_credentials_state_check
    CHECK (state IN ('pending', 'active', 'failed', 'legacy', 'unverified'));

DROP TRIGGER IF EXISTS users_reject_retired_address ON users;
DROP FUNCTION IF EXISTS reject_retired_user_address();
DROP TABLE IF EXISTS address_rotations;
