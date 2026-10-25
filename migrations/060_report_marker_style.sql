-- Posts use their own marker vocabulary so they remain visually distinct from
-- voucher and Want pins while sharing the established contrast-tested colors.
CREATE TABLE report_marker_icon (
    value TEXT PRIMARY KEY,
    CONSTRAINT report_marker_icon_value_valid CHECK (value ~ '^[a-z][a-z-]{0,31}$')
);

INSERT INTO report_marker_icon (value) VALUES
    ('post'),
    ('event'),
    ('announcement');

ALTER TABLE reports
    ADD COLUMN marker_icon TEXT NOT NULL DEFAULT 'post'
        REFERENCES report_marker_icon(value),
    ADD COLUMN marker_color TEXT NOT NULL DEFAULT 'pink'
        REFERENCES voucher_marker_color(value);

---- create above / drop below ----

ALTER TABLE reports
    DROP COLUMN IF EXISTS marker_color,
    DROP COLUMN IF EXISTS marker_icon;
DROP TABLE IF EXISTS report_marker_icon;
