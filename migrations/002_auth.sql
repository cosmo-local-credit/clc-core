-- Enums
CREATE TYPE user_role AS ENUM ('USER', 'SUPERADMIN');

CREATE TYPE user_status AS ENUM ('PENDING', 'APPROVED', 'REJECTED');

-- Users
CREATE TABLE IF NOT EXISTS users (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ethereum_address TEXT UNIQUE NOT NULL,
    name             TEXT NOT NULL,
    email            TEXT UNIQUE NOT NULL,
    role             user_role NOT NULL DEFAULT 'USER',
    status           user_status NOT NULL DEFAULT 'PENDING',
    max_pools        INT NOT NULL DEFAULT 10,
    max_tokens       INT NOT NULL DEFAULT 20,
    created_at       TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at       TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS users_ethereum_address_idx ON users(ethereum_address);
CREATE INDEX IF NOT EXISTS users_status_idx ON users(status);

CREATE TRIGGER update_users_timestamp
    BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE PROCEDURE update_timestamp();

-- User deployments: links a user to a tracked on-chain resource (token or pool)
CREATE TABLE IF NOT EXISTS user_deployments (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tracking_id   UUID NOT NULL UNIQUE,
    resource_type TEXT NOT NULL CHECK (resource_type IN ('TOKEN', 'POOL')),
    created_at    TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS user_deployments_user_id_idx ON user_deployments(user_id);
CREATE INDEX IF NOT EXISTS user_deployments_tracking_id_idx ON user_deployments(tracking_id);
