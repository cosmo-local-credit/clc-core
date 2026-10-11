CREATE TABLE transfer_valuation_snapshots (
    tracking_id       UUID PRIMARY KEY,
    token_address     TEXT NOT NULL,
    token_amount_raw  NUMERIC(78, 0) NOT NULL,
    token_decimals    SMALLINT NOT NULL,
    voucher_value     NUMERIC NOT NULL,
    voucher_uoa       TEXT NOT NULL,
    uoa_value         NUMERIC NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT transfer_valuation_token_address_format
        CHECK (token_address ~ '^0x[0-9a-fA-F]{40}$'),
    CONSTRAINT transfer_valuation_token_amount_nonneg
        CHECK (token_amount_raw >= 0),
    CONSTRAINT transfer_valuation_token_decimals_range
        CHECK (token_decimals BETWEEN 0 AND 255),
    CONSTRAINT transfer_valuation_voucher_value_positive
        CHECK (voucher_value > 0),
    CONSTRAINT transfer_valuation_uoa_valid
        CHECK (voucher_uoa = btrim(voucher_uoa)
            AND voucher_uoa ~ '^[A-Za-z0-9]{2,16}$'),
    CONSTRAINT transfer_valuation_uoa_value_nonneg
        CHECK (uoa_value >= 0)
);
