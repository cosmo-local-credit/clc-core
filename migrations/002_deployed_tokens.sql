-- Deployed token registry: tracks successfully deployed ERC20 tokens
CREATE TABLE IF NOT EXISTS deployed_tokens (
    id                    INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    otx_id                INT REFERENCES otx(id) NOT NULL,
    platform_contract_id  INT REFERENCES platform_contracts(id) NOT NULL,
    address               TEXT NOT NULL,
    symbol                TEXT NOT NULL,
    name                  TEXT NOT NULL,
    admin                 TEXT NOT NULL,
    created_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (otx_id),
    UNIQUE (address)
);

CREATE TRIGGER update_deployed_tokens_timestamp
    BEFORE UPDATE ON deployed_tokens
    FOR EACH ROW EXECUTE PROCEDURE update_timestamp();

-- Token deploy metadata: staging table to carry token metadata from deploy
-- time through to the subscriber confirmation path.
CREATE TABLE IF NOT EXISTS token_deploy_meta (
    id                    INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    otx_id                INT REFERENCES otx(id) NOT NULL UNIQUE,
    platform_contract_id  INT REFERENCES platform_contracts(id) NOT NULL,
    token_name            TEXT NOT NULL,
    token_symbol          TEXT NOT NULL,
    admin                 TEXT NOT NULL,
    created_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TRIGGER update_token_deploy_meta_timestamp
    BEFORE UPDATE ON token_deploy_meta
    FOR EACH ROW EXECUTE PROCEDURE update_timestamp();
