-- Dispatch statuses for reth's rejection vocabulary.
--
-- Before this migration the dispatcher could express only four chain-level
-- rejections (LOW_NONCE, NO_GAS, LOW_GAS_PRICE, REPLACEMENT_UNDERPRICED), all
-- matched against geth substrings. Every other rejection — reth's pool
-- vocabulary included — fell through classification and was recorded as
-- IN_NETWORK, consuming the nonce for a transaction that had never been
-- accepted.
INSERT INTO dispatch_status_type (value) VALUES
    ('POOL_FULL'),
    ('POOL_CONFLICT'),
    ('NONCE_GAP'),
    ('INVALID_TX')
ON CONFLICT (value) DO NOTHING;

-- The reconciler walks a signer's unsettled transactions in nonce order. Without
-- this it is a seq scan over otx joined to dispatch on every tick.
CREATE INDEX IF NOT EXISTS otx_signer_nonce_idx
    ON otx (signer_account, nonce);

CREATE INDEX IF NOT EXISTS dispatch_unsettled_idx
    ON dispatch (otx_id)
    WHERE status NOT IN ('SUCCESS', 'REVERTED', 'LOW_NONCE', 'INVALID_TX');
