CREATE TABLE next_erc20_workflow (
	id BIGSERIAL PRIMARY KEY,
	tracking_id UUID NOT NULL UNIQUE,
	action VARCHAR(32) NOT NULL,
	status VARCHAR(32) NOT NULL,
	name TEXT NOT NULL,
	symbol TEXT NOT NULL,
	decimals INTEGER NOT NULL,
	initial_supply BIGINT NOT NULL,
	initial_mintee VARCHAR(42) NOT NULL,
	owner VARCHAR(42) NOT NULL,
	expiry_timestamp BIGINT NOT NULL DEFAULT 0,
	factory_address VARCHAR(42) NOT NULL,
	admin_address VARCHAR(42) NOT NULL,
	proxy_address VARCHAR(42) NOT NULL DEFAULT '',
	implementation_address VARCHAR(42) NOT NULL DEFAULT '',
	previous_implementation_address VARCHAR(42) NOT NULL DEFAULT '',
	version INTEGER NOT NULL,
	upgrade_calldata TEXT NOT NULL DEFAULT '',
	implementation_tx_hash VARCHAR(66) NOT NULL DEFAULT '',
	action_tx_hash VARCHAR(66) NOT NULL DEFAULT '',
	created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_next_erc20_workflow_proxy ON next_erc20_workflow(proxy_address, version DESC);
CREATE INDEX idx_next_erc20_workflow_symbol ON next_erc20_workflow(symbol, version DESC);

INSERT INTO otx_tx_type (value) VALUES
('NEXT_ERC20_IMPLEMENTATION_DEPLOY'),
('NEXT_ERC20_PROXY_DEPLOY'),
('NEXT_ERC20_INITIAL_MINT'),
('NEXT_ERC20_PROXY_UPGRADE');