ALTER TABLE sn_gas_approver_account ADD COLUMN status TEXT NOT NULL DEFAULT 'PENDING';

CREATE UNIQUE INDEX sn_gas_approver_account_address_unique
    ON sn_gas_approver_account(address)
    WHERE status = 'SUCCESS';
