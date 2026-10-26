-- Custom Safe-compatible multisigs can expose getOwners without getThreshold.
-- Their signer set still authorizes metadata without inventing a quorum.
ALTER TABLE safe_state ALTER COLUMN threshold DROP NOT NULL;
