INSERT INTO otx_tx_type (value) VALUES ('USERS_INDEX_ADD');

CREATE TABLE IF NOT EXISTS sn_gas_approver_account (
    tracking_id uuid PRIMARY KEY,
    address TEXT NOT NULL,
    users_index TEXT NOT NULL,
    active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

create trigger update_sn_gas_approver_account_timestamp
    before update on sn_gas_approver_account
for each row
execute procedure update_timestamp();