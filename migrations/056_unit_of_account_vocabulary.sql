-- A shared, reusable vocabulary for profile, voucher and pool Units of Account.
-- Current metadata participates in renames; historical transfer snapshots do not.
CREATE TABLE unit_of_account (
    value TEXT PRIMARY KEY,
    is_custom BOOLEAN NOT NULL DEFAULT false,
    CONSTRAINT unit_of_account_value_valid CHECK (
        value = btrim(value)
        AND value <> ''
    )
);

CREATE UNIQUE INDEX unit_of_account_value_lower_idx
    ON unit_of_account (lower(value));

INSERT INTO unit_of_account (value, is_custom)
SELECT value, false
FROM unnest(string_to_array(
    'AED AFN ALL AMD ANG AOA ARS AUD AWG AZN BAM BBD BDT BGN BHD BIF BMD BND BOB BRL BSD BTN BWP BYN BZD CAD CDF CHF CLP CNY COP CRC CUP CVE CZK DJF DKK DOP DZD EGP ERN ETB EUR FJD FKP GBP GEL GHS GIP GMD GNF GTQ GYD HKD HNL HRK HTG HUF IDR ILS INR IQD IRR ISK JMD JOD JPY KES KGS KHR KMF KPW KRW KWD KYD KZT LAK LBP LKR LRD LSL LYD MAD MDL MGA MKD MMK MNT MOP MRU MUR MVR MWK MXN MYR MZN NAD NGN NIO NOK NPR NZD OMR PAB PEN PGK PHP PKR PLN PYG QAR RON RSD RUB RWF SAR SBD SCR SDG SEK SGD SHP SLE SOS SRD SSP STN SYP SZL THB TJS TMT TND TOP TRY TTD TWD TZS UAH UGX USD UYU UZS VES VND VUV WST XAF XCD XOF XPF YER ZAR ZMW ZWL HOUR',
    ' '
)) AS value;

ALTER TABLE users ALTER COLUMN preferred_currency TYPE TEXT;
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_preferred_currency_format;

-- Collapse whitespace and resolve case-insensitive built-in spellings first.
UPDATE users SET preferred_currency = regexp_replace(btrim(preferred_currency), '\s+', ' ', 'g')
WHERE preferred_currency IS NOT NULL;
UPDATE vouchers SET voucher_uoa = regexp_replace(btrim(voucher_uoa), '\s+', ' ', 'g')
WHERE voucher_uoa IS NOT NULL;
UPDATE pools SET unit_of_account = regexp_replace(btrim(unit_of_account), '\s+', ' ', 'g')
WHERE unit_of_account IS NOT NULL;
UPDATE deployed_tokens SET voucher_uoa = regexp_replace(btrim(voucher_uoa), '\s+', ' ', 'g')
WHERE voucher_uoa IS NOT NULL;
UPDATE deployed_pools SET unit_of_account = regexp_replace(btrim(unit_of_account), '\s+', ' ', 'g')
WHERE unit_of_account IS NOT NULL;

-- Preserve every legacy value, including data that predates the new 32-character
-- API write limit. New writes and renames enforce that limit in the handlers.
INSERT INTO unit_of_account (value, is_custom)
SELECT DISTINCT ON (lower(value)) value, true
FROM (
    SELECT preferred_currency AS value FROM users
    UNION ALL SELECT voucher_uoa FROM vouchers
    UNION ALL SELECT unit_of_account FROM pools
    UNION ALL SELECT voucher_uoa FROM deployed_tokens
    UNION ALL SELECT unit_of_account FROM deployed_pools
) legacy
WHERE value IS NOT NULL AND value <> ''
ORDER BY lower(value), value
ON CONFLICT DO NOTHING;

UPDATE users u SET preferred_currency = vocabulary.value
FROM unit_of_account vocabulary
WHERE u.preferred_currency IS NOT NULL
  AND lower(u.preferred_currency) = lower(vocabulary.value);
UPDATE vouchers v SET voucher_uoa = vocabulary.value
FROM unit_of_account vocabulary
WHERE v.voucher_uoa IS NOT NULL
  AND lower(v.voucher_uoa) = lower(vocabulary.value);
UPDATE pools p SET unit_of_account = vocabulary.value
FROM unit_of_account vocabulary
WHERE p.unit_of_account IS NOT NULL
  AND lower(p.unit_of_account) = lower(vocabulary.value);
UPDATE deployed_tokens d SET voucher_uoa = vocabulary.value
FROM unit_of_account vocabulary
WHERE d.voucher_uoa IS NOT NULL
  AND lower(d.voucher_uoa) = lower(vocabulary.value);
UPDATE deployed_pools d SET unit_of_account = vocabulary.value
FROM unit_of_account vocabulary
WHERE d.unit_of_account IS NOT NULL
  AND lower(d.unit_of_account) = lower(vocabulary.value);

ALTER TABLE users ADD CONSTRAINT users_preferred_currency_uoa_fk
    FOREIGN KEY (preferred_currency) REFERENCES unit_of_account(value) ON UPDATE CASCADE;
ALTER TABLE vouchers ADD CONSTRAINT vouchers_voucher_uoa_fk
    FOREIGN KEY (voucher_uoa) REFERENCES unit_of_account(value) ON UPDATE CASCADE;
ALTER TABLE pools ADD CONSTRAINT pools_unit_of_account_fk
    FOREIGN KEY (unit_of_account) REFERENCES unit_of_account(value) ON UPDATE CASCADE;
ALTER TABLE deployed_tokens ADD CONSTRAINT deployed_tokens_voucher_uoa_fk
    FOREIGN KEY (voucher_uoa) REFERENCES unit_of_account(value) ON UPDATE CASCADE;
ALTER TABLE deployed_pools ADD CONSTRAINT deployed_pools_unit_of_account_fk
    FOREIGN KEY (unit_of_account) REFERENCES unit_of_account(value) ON UPDATE CASCADE;

ALTER TABLE transfer_valuation_snapshots
    DROP CONSTRAINT transfer_valuation_uoa_valid;
ALTER TABLE transfer_valuation_snapshots
    ADD CONSTRAINT transfer_valuation_uoa_valid CHECK (
        voucher_uoa = btrim(voucher_uoa)
        AND voucher_uoa <> ''
        AND char_length(voucher_uoa) <= 32
    );
