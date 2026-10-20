-- New accounts get one pool, matching the one voucher and one catalog they
-- start with. Existing pool allowances are preserved, as 051 did for tokens.
ALTER TABLE users ALTER COLUMN max_pools SET DEFAULT 1;

---- create above / drop below ----
ALTER TABLE users ALTER COLUMN max_pools SET DEFAULT 10;
