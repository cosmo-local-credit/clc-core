-- clc-core executes a Safe transaction once its signers have confirmed it:
-- execTransaction is permissionless with threshold owner signatures, and the
-- system signer pays the gas. The submission is a tracked OTX like any other.
INSERT INTO otx_tx_type (value) VALUES ('SAFE_EXECUTE') ON CONFLICT (value) DO NOTHING;
