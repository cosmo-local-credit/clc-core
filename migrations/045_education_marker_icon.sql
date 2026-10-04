-- Classes and training use an education pin. Seed it separately so databases
-- that already applied the marker palette receive the new choice too.
INSERT INTO voucher_marker_icon (value) VALUES
    ('education')
ON CONFLICT (value) DO NOTHING;
