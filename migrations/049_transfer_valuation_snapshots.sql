CREATE TABLE transfer_valuation_snapshots (
    tracking_id       UUID NOT NULL,
    token_address     TEXT NOT NULL,
    voucher_value     NUMERIC NOT NULL,
    voucher_uoa       TEXT NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (tracking_id, token_address),
    CONSTRAINT transfer_valuation_token_address_format
        CHECK (token_address ~ '^0x[0-9a-fA-F]{40}$'),
    CONSTRAINT transfer_valuation_voucher_value_positive
        CHECK (voucher_value > 0),
    CONSTRAINT transfer_valuation_uoa_valid
        CHECK (voucher_uoa = btrim(voucher_uoa)
            AND voucher_uoa ~ '^[A-Za-z0-9]{2,16}$')
);
