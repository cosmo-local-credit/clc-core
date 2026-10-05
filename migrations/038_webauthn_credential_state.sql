-- A WebAuthn credential row is what /auth/webauthn/verify trusts to issue a
-- session for an address, so it must only become usable once the chain has
-- confirmed the Calibur register() call that put the key on that wallet.
--
-- Before this migration the relay worker wrote the row before dispatch and a
-- reverted transaction never removed it. Every pre-existing row is therefore
-- 'legacy': not trusted by any sign-in query until the one-time chain check in
-- relayer.VerifyLegacyWebAuthnCredentials proves isRegistered(key_hash) on the
-- wallet. Rows that fail it become 'unverified' and stay for investigation.
--
-- States:
--   pending    - registration relayed, outcome not yet known
--   active     - registration transaction succeeded; usable for sign-in
--   failed     - every transaction for the registration reverted or was dropped
--   legacy     - written before this migration; awaiting the chain check
--   unverified - legacy row the chain check could not confirm
ALTER TABLE webauthn_credentials
    ADD COLUMN IF NOT EXISTS tracking_id UUID,
    ADD COLUMN IF NOT EXISTS state       TEXT        NOT NULL DEFAULT 'legacy',
    ADD COLUMN IF NOT EXISTS updated_at  TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP;

-- Existing rows took 'legacy' from the default above. New rows must name their
-- state, so a forgotten column fails the insert instead of defaulting open.
ALTER TABLE webauthn_credentials ALTER COLUMN state DROP DEFAULT;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'webauthn_credentials'::regclass
          AND conname = 'webauthn_credentials_state_check'
    ) THEN
        ALTER TABLE webauthn_credentials
            ADD CONSTRAINT webauthn_credentials_state_check
            CHECK (state IN ('pending', 'active', 'failed', 'legacy', 'unverified'));
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS webauthn_credentials_tracking_id_idx
    ON webauthn_credentials (tracking_id)
    WHERE tracking_id IS NOT NULL;

DROP TRIGGER IF EXISTS update_webauthn_credentials_timestamp ON webauthn_credentials;
CREATE TRIGGER update_webauthn_credentials_timestamp
    BEFORE UPDATE ON webauthn_credentials
    FOR EACH ROW EXECUTE PROCEDURE update_timestamp();
