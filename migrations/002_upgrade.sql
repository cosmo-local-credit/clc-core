-- Upgrade meta: staging table to carry upgrade context from worker
-- through to the subscriber confirmation path.
CREATE TABLE IF NOT EXISTS upgrade_meta (
    id                 INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    otx_id             INT REFERENCES otx(id) NOT NULL UNIQUE,
    proxy_address      TEXT NOT NULL,
    contract_type      contract_type NOT NULL,
    new_implementation TEXT NOT NULL,
    version            TEXT NOT NULL,
    created_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Add PROXY_UPGRADE OTX type
INSERT INTO otx_tx_type (value) VALUES ('PROXY_UPGRADE')
ON CONFLICT (value) DO NOTHING;
