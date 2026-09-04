-- Seed the Gnosis (chain 100) platform contracts.
--
-- Migration 001 seeds only chain 42220, so on a Gnosis deployment every
-- deploy endpoint answered E07 "Record(s) not found": the handler looks up
-- (chain_id, contract_type) in platform_contracts and chain 100 had no rows.
--
-- Addresses come from clc-protocol v1.0.0,
-- deployments/implementations-100-20260818T103109Z.json (also its
-- implementations-latest.json), and each one was confirmed to carry code on
-- chain 100 with eth_getCode before being written here. The Celo addresses in
-- 001 are all empty on Gnosis, so copying them forward would have produced
-- deploy transactions calling an address with no code — and the deploy jobs
-- run with MaxAttempts: 1, so the nonce would be spent on a revert.
--
-- The factory is Solady's ERC1967Factory, one address for every type, matching
-- how 001 seeds a single factory across all seven Celo rows.
--
-- The chain 42220 rows are deliberately left in place. platform_contracts is
-- chain-scoped and every lookup filters on chain_id, so they are inert here,
-- while deployed_tokens.platform_contract_id and deployed_pools reference them
-- by FK on the Celo database.
INSERT INTO platform_contracts (chain_id, contract_type, factory_address, implementation_address, version) VALUES
    (100, 'ERC20',                     '0xB286994c648F98fD3a3BA6C43934828a5b88162b', '0x34445d13F112A11f72C1d353a7dcdc407F3df2d8', 'v1.0.0'),
    (100, 'SWAPPOOL',                  '0xB286994c648F98fD3a3BA6C43934828a5b88162b', '0x9e694D342Cab02e295262B2290b0E64A0334160D', 'v1.0.0'),
    (100, 'TOKEN_UNIQUE_SYMBOL_INDEX', '0xB286994c648F98fD3a3BA6C43934828a5b88162b', '0x0CEB18BA6562D3227717D98c044A5849bE8362EF', 'v1.0.0'),
    (100, 'LIMITER',                   '0xB286994c648F98fD3a3BA6C43934828a5b88162b', '0x258AAd6c933F70D7F071E112800a41Fb60434048', 'v1.0.0'),
    (100, 'FEE_POLICY',                '0xB286994c648F98fD3a3BA6C43934828a5b88162b', '0x1b97FfFAF2D2e16C8F7de6826F4F658dc898E5b6', 'v1.0.0'),
    (100, 'ORACLE_QUOTER',             '0xB286994c648F98fD3a3BA6C43934828a5b88162b', '0x3334fd1eA4c7e4dCA51f5E62EE3F5f7Dcbd098BA', 'v1.0.0'),
    (100, 'RELATIVE_QUOTER',           '0xB286994c648F98fD3a3BA6C43934828a5b88162b', '0x0B0986c0E580377389337C453Cc65A3933511165', 'v1.0.0')
ON CONFLICT (chain_id, contract_type) DO UPDATE SET
    factory_address        = EXCLUDED.factory_address,
    implementation_address = EXCLUDED.implementation_address,
    version                = EXCLUDED.version,
    updated_at             = CURRENT_TIMESTAMP;

-- protocol_addresses has no chain_id column — it is a flat key -> address map,
-- so a single row cannot hold both chains. Gnosis is the only chain we settle
-- on now, so these are repointed at the v1.0.0 Gnosis deployment.
--
-- Both Celo values (0x410e7Ff8… and 0x7ff73c18…) are empty on chain 100, so
-- leaving them would have handed pool deploys a fee controller and a decimal
-- quoter that do not exist.
--
-- If a second chain is ever settled on again, this table needs a chain_id and
-- a composite key, not another overwrite.
INSERT INTO protocol_addresses (key, address) VALUES
    ('protocol_fee_controller', '0x302E6d520e7D7AeFceA4813e456234B5daA23B4d'),
    ('decimal_quoter',          '0x336f493d5472FD59e9E05128804E2D05Be89c4B7')
ON CONFLICT (key) DO UPDATE SET
    address    = EXCLUDED.address,
    updated_at = CURRENT_TIMESTAMP;
