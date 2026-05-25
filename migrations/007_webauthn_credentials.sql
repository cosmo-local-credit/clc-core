-- WebAuthn credentials registered through the relayer during initial
-- ephemeral-key registration. Looked up by credential_id during recovery
-- to map a passkey back to its Calibur-delegated wallet.
CREATE TABLE IF NOT EXISTS webauthn_credentials (
    credential_id TEXT      PRIMARY KEY,
    address       TEXT      NOT NULL,
    key_hash      TEXT      NOT NULL,
    public_key_x  NUMERIC   NOT NULL,
    public_key_y  NUMERIC   NOT NULL,
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS webauthn_credentials_address_idx
    ON webauthn_credentials (address);

-- Relayer OTX types — otx.otx_type has a FK on otx_tx_type, so the
-- relayer cannot insert without these values being present.
INSERT INTO otx_tx_type (value) VALUES
    ('RELAY_DELEGATE'),
    ('RELAY_EXEC'),
    ('RELAY_DELEGATE_EXEC')
ON CONFLICT (value) DO NOTHING;
