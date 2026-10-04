-- The map marker an issuer picks for a voucher: a glyph and a colour, each from
-- a closed set the clients know how to draw. NULL in either column means
-- automatic, so the client keeps deriving that half from what the voucher
-- offers and every existing voucher keeps the pin it has today. Nothing is
-- backfilled.
--
-- Lookup tables rather than CHECK lists, like offering_unit, so a new glyph or
-- colour is an INSERT. The foreign keys deliberately do not cascade a rename:
-- the values are keys into client code, and a renamed one would become a value
-- no client can draw.
CREATE TABLE IF NOT EXISTS voucher_marker_icon (
    value TEXT PRIMARY KEY,
    CONSTRAINT voucher_marker_icon_value_valid CHECK (value ~ '^[a-z][a-z-]{0,31}$')
);
INSERT INTO voucher_marker_icon (value) VALUES
    ('basket'),
    ('hand-coin'),
    ('coupon'),
    ('food'),
    ('farm'),
    ('tools'),
    ('health')
ON CONFLICT (value) DO NOTHING;

CREATE TABLE IF NOT EXISTS voucher_marker_color (
    value TEXT PRIMARY KEY,
    CONSTRAINT voucher_marker_color_value_valid CHECK (value ~ '^[a-z][a-z-]{0,31}$')
);
INSERT INTO voucher_marker_color (value) VALUES
    ('blue'),
    ('purple'),
    ('orange'),
    ('pink'),
    ('cyan'),
    ('brown')
ON CONFLICT (value) DO NOTHING;

-- The catalog row the map reads.
ALTER TABLE vouchers
    ADD COLUMN IF NOT EXISTS marker_icon TEXT;

ALTER TABLE vouchers
    ADD COLUMN IF NOT EXISTS marker_color TEXT;

ALTER TABLE vouchers
    DROP CONSTRAINT IF EXISTS vouchers_marker_icon_fk;

ALTER TABLE vouchers
    ADD CONSTRAINT vouchers_marker_icon_fk
        FOREIGN KEY (marker_icon) REFERENCES voucher_marker_icon(value);

ALTER TABLE vouchers
    DROP CONSTRAINT IF EXISTS vouchers_marker_color_fk;

ALTER TABLE vouchers
    ADD CONSTRAINT vouchers_marker_color_fk
        FOREIGN KEY (marker_color) REFERENCES voucher_marker_color(value);

-- The deploy row that carries the choice to catalog promotion, as 022 did for
-- the icon and banner.
ALTER TABLE deployed_tokens
    ADD COLUMN IF NOT EXISTS marker_icon TEXT;

ALTER TABLE deployed_tokens
    ADD COLUMN IF NOT EXISTS marker_color TEXT;

ALTER TABLE deployed_tokens
    DROP CONSTRAINT IF EXISTS deployed_tokens_marker_icon_fk;

ALTER TABLE deployed_tokens
    ADD CONSTRAINT deployed_tokens_marker_icon_fk
        FOREIGN KEY (marker_icon) REFERENCES voucher_marker_icon(value);

ALTER TABLE deployed_tokens
    DROP CONSTRAINT IF EXISTS deployed_tokens_marker_color_fk;

ALTER TABLE deployed_tokens
    ADD CONSTRAINT deployed_tokens_marker_color_fk
        FOREIGN KEY (marker_color) REFERENCES voucher_marker_color(value);
