-- Point protocol_fee_controller at a deployed controller.
--
-- Migration 027 seeded 0x302E6d520e7D7AeFceA4813e456234B5daA23B4d, which is
-- the ProtocolFeeController *implementation* from clc-protocol v1.0.0
-- (deployments/implementations-100-20260818T103109Z.json, key "pfc"). That
-- contract disables its initializers in its constructor, so it can never hold
-- state: owner, protocolFee and protocolFeeRecipient are all zero on chain and
-- initialize() reverts with InvalidInitialization.
--
-- 027 checked each address with eth_getCode before writing it, and an
-- implementation passes that check -- it has code, it just has no state. Every
-- pool deployed since took it as its protocol fee controller, and SwapPool
-- reads a zero fee and a zero recipient from it, so the protocol collected
-- nothing. SwapPool has no setter for the address, so those pools cannot be
-- corrected; they are test deployments and are left as they are.
--
-- The address below is an ERC1967 proxy over that same implementation,
-- initialized with owner and fee recipient 0x33a573149db22e759fB9A38Bcc461C12855c2645
-- and a 20000 PPM protocol fee. Being a proxy, its fee, recipient and active
-- flag stay changeable by its owner without redeploying anything.
--
-- The invariant this table needs and 027 did not state: an entry here is a
-- deployed, initialized instance. Never an implementation address. The deploy
-- worker now refuses a controller whose getProtocolFeeRecipient() is zero, so a
-- repeat of this mistake fails the deploy instead of reaching a pool.
INSERT INTO protocol_addresses (key, address) VALUES
    ('protocol_fee_controller', '0x4969eE5EbD38bBE08469c1923f662f59EaAF4fD7')
ON CONFLICT (key) DO UPDATE SET
    address    = EXCLUDED.address,
    updated_at = CURRENT_TIMESTAMP;
