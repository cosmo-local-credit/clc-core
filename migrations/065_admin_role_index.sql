CREATE INDEX users_role_id_idx ON users (id DESC)
WHERE role IN ('ADMIN', 'SUPERADMIN');

---- create above / drop below ----

DROP INDEX users_role_id_idx;
