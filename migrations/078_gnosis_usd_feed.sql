-- Gnosis: Chainlink USDC / USD as the verified USD feed, so a USD voucher can be
-- priced. There is no USD / USD aggregator; USDC / USD tracks the peg (about
-- 0.9998). It updates on a 24 h heartbeat, so it takes the same 2 h margin as
-- EUR / USD.
INSERT INTO oracle_feeds (chain_id, name, address, description, decimals, max_staleness_seconds) VALUES
    (100, 'USD', '0x26C31ac71010aF62E6B486D1132E266D6298857D', 'USDC / USD', 8, 93600)
ON CONFLICT (chain_id, name) DO UPDATE SET
    address               = EXCLUDED.address,
    description           = EXCLUDED.description,
    decimals              = EXCLUDED.decimals,
    max_staleness_seconds = EXCLUDED.max_staleness_seconds,
    updated_at            = CURRENT_TIMESTAMP;

-- Mappings recorded before the row existed now resolve to it.
UPDATE pool_tokens pt
SET oracle_feed_id = f.id
FROM pools p, oracle_feeds f
WHERE p.id = pt.pool_id
  AND f.chain_id = p.chain_id
  AND f.chain_id = 100 AND f.name = 'USD'
  AND pt.oracle_feed_id IS NULL
  AND lower(pt.oracle_address) = lower(f.address);

---- create above / drop below ----

UPDATE pool_tokens
SET oracle_feed_id = NULL
WHERE oracle_feed_id IN (SELECT id FROM oracle_feeds WHERE chain_id = 100 AND name = 'USD');

DELETE FROM oracle_feeds WHERE chain_id = 100 AND name = 'USD';
