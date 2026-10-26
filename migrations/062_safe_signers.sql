-- Signer sets of Safe multisigs that own catalog assets. Fed by the
-- AddedOwner / RemovedOwner / ChangedThreshold / SafeSetup events and by
-- cmd/reconcile-ownership, ordered by (block_number, log_index) like
-- catalog_ownership. An address absent from safe_state is not a known Safe.
CREATE TABLE safe_state (
    chain_id BIGINT NOT NULL,
    safe_address TEXT NOT NULL CHECK (safe_address = lower(safe_address)),
    threshold INTEGER NOT NULL CHECK (threshold >= 1),
    block_number BIGINT NOT NULL CHECK (block_number >= 0),
    log_index BIGINT NOT NULL CHECK (log_index >= 0),
    refreshed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (chain_id, safe_address)
);

CREATE TABLE safe_signers (
    chain_id BIGINT NOT NULL,
    safe_address TEXT NOT NULL CHECK (safe_address = lower(safe_address)),
    signer TEXT NOT NULL CHECK (signer = lower(signer)),
    PRIMARY KEY (chain_id, safe_address, signer),
    FOREIGN KEY (chain_id, safe_address) REFERENCES safe_state (chain_id, safe_address) ON DELETE CASCADE
);

CREATE INDEX safe_signers_signer_idx ON safe_signers (chain_id, signer);
