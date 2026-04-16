DROP TABLE IF EXISTS next_erc20_workflow;

CREATE TYPE contract_type AS ENUM (
    'ERC20',
    'ERC20_DEMURRAGE',
    'SWAPPOOL'
);

CREATE TABLE IF NOT EXISTS platform_contracts (
    id                      INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    chain_id                INT NOT NULL,
    contract_type           contract_type NOT NULL,
    factory_address         TEXT NOT NULL,
    implementation_address  TEXT NOT NULL,
    created_at              TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at              TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (chain_id, contract_type)
);

INSERT INTO otx_tx_type (value) VALUES
('TOKEN_PROXY_DEPLOY'),
('TOKEN_IMPL_DEPLOY');
