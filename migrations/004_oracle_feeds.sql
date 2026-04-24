-- Chainlink oracle feed registry: stores known oracle feed addresses for price feeds
CREATE TABLE IF NOT EXISTS oracle_feeds (
    id         INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    address    TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TRIGGER update_oracle_feeds_timestamp
    BEFORE UPDATE ON oracle_feeds
    FOR EACH ROW EXECUTE PROCEDURE update_timestamp();

-- Seed Chainlink oracle feeds (Celo mainnet, all relative to USD)
INSERT INTO oracle_feeds (name, address) VALUES
    ('USDT', '0x5e37AF40A7A344ec9b03CCD34a250F3dA9a20B02'),
    ('USDC', '0xc7A353BaE210aed958a1A2928b654938EC59DaB2'),
    ('EURC', '0xc7A353BaE210aed958a1A2928b654938EC59DaB2'),
    ('CELO', '0x0568fD19986748cEfF3301e55c0eb1E729E0Ab7e'),
    ('CUSD', '0xe38A27BE4E7d866327e09736F3C570F256FFd048'),
    ('BRL',  '0xe8EcaF727080968Ed5F6DBB595B91e50eEb9F8B3'),
    ('GBP',  '0xe76FE54dfeD2ce8B4d1AC63c982DfF7CFc92bf82'),
    ('NGN',  '0xc17cBE2dB40e53F4984C46F608DA6DA1fF074c11'),
    ('ZAR',  '0x11b7221a0DD025778A95e9E0B87b477522C32E02'),
    ('INR',  '0x85d4Ec34339478F73c153710B19f2D5C402dce6F'),
    ('KES',  '0x85d4Ec34339478F73c153710B19f2D5C402dce6F')
ON CONFLICT (name) DO UPDATE SET
    address    = EXCLUDED.address,
    updated_at = CURRENT_TIMESTAMP;
