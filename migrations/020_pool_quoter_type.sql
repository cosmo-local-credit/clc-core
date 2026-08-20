-- Keep the quoter kind a pool was published with.
--
-- The deploy request has always carried it (pkg/api DeployPoolRequest.quoterType,
-- validated oneof=decimal oracle relative) and the worker branches on it to pick
-- which quoter proxy to deploy, but nothing stored it. A client reading a pool
-- back therefore could not tell an OracleQuoter from a RelativeQuoter, and it
-- cannot work the answer out for itself: the two report identical ERC-165
-- interfaces (0x01ffc9a7, 0x9493f8b2, 0xdbb21d40) while taking different setters
-- — setOracle(token, aggregator) against setPriceIndexValue(token, rate). An
-- operator console adding a token to a pool has to know which call to sign.
--
-- Nullable, with no backfill. Pools imported from federation never went through
-- this deploy path, and pools deployed before this migration did not record the
-- value, so NULL means "unknown" rather than any particular quoter. Guessing a
-- default here would be worse than saying so.

ALTER TABLE deployed_pools
    ADD COLUMN IF NOT EXISTS quoter_type TEXT;

ALTER TABLE deployed_pools
    DROP CONSTRAINT IF EXISTS deployed_pools_quoter_type_valid;

ALTER TABLE deployed_pools
    ADD CONSTRAINT deployed_pools_quoter_type_valid
        CHECK (quoter_type IS NULL
            OR quoter_type IN ('decimal', 'oracle', 'relative'));

ALTER TABLE pools
    ADD COLUMN IF NOT EXISTS quoter_type TEXT;

ALTER TABLE pools
    DROP CONSTRAINT IF EXISTS pools_quoter_type_valid;

ALTER TABLE pools
    ADD CONSTRAINT pools_quoter_type_valid
        CHECK (quoter_type IS NULL
            OR quoter_type IN ('decimal', 'oracle', 'relative'));
