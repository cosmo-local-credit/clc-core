-- Migration 011: add ACCOUNT_REGISTER OTX type for onchain user registration
-- via AccountIndex.add(address). Triggered when a Calibur wallet is activated
-- (EIP-7702 delegation). The otx.otx_type column has a FK on otx_tx_type, so
-- the new type must exist before the RegisterUserWorker can insert.

INSERT INTO otx_tx_type (value) VALUES
    ('ACCOUNT_REGISTER')
ON CONFLICT (value) DO NOTHING;
