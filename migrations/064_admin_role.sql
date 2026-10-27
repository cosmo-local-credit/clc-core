ALTER TYPE user_role ADD VALUE 'ADMIN' BEFORE 'SUPERADMIN';

---- create above / drop below ----

DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM users WHERE role = 'ADMIN') THEN
        RAISE EXCEPTION 'Cannot remove ADMIN role while users still have it';
    END IF;
END $$;

ALTER TABLE users ALTER COLUMN role DROP DEFAULT;
ALTER TYPE user_role RENAME TO user_role_with_admin;
CREATE TYPE user_role AS ENUM ('USER', 'SUPERADMIN');
ALTER TABLE users
    ALTER COLUMN role TYPE user_role USING role::TEXT::user_role;
ALTER TABLE users ALTER COLUMN role SET DEFAULT 'USER';
DROP TYPE user_role_with_admin;
