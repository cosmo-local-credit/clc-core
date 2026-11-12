-- Account retirement and moderation history (clc-app issue #397).
--
-- Retirement is a lifecycle orthogonal to users.status: Ban/Unban keep using
-- user_status, and a retiring account keeps whatever moderation status it had.
-- A lifecycle row is "active" while restored_at IS NULL; a restore stamps the row
-- and keeps it as history, so one account can retire, restore and retire again.
CREATE TABLE account_retirements (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id      INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    state        TEXT NOT NULL DEFAULT 'RETIREMENT_SCHEDULED'
        CHECK (state IN ('RETIREMENT_SCHEDULED', 'ERASURE_PENDING', 'ERASED')),
    requested_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    closes_at    TIMESTAMPTZ NOT NULL,
    reason       TEXT NOT NULL DEFAULT '' CHECK (CHAR_LENGTH(reason) <= 2000),
    requested_by INT REFERENCES users(id) ON DELETE SET NULL,
    request_source TEXT NOT NULL CHECK (request_source IN ('SELF', 'ADMIN')),
    restored_at  TIMESTAMPTZ,
    restored_by  INT REFERENCES users(id) ON DELETE SET NULL,
    version      INT NOT NULL DEFAULT 1,
    CHECK (closes_at > requested_at)
);

-- One active lifecycle per account.
CREATE UNIQUE INDEX account_retirements_active
    ON account_retirements (user_id) WHERE restored_at IS NULL;
CREATE INDEX account_retirements_closing
    ON account_retirements (closes_at) WHERE restored_at IS NULL;

-- Append-only operational facts. Deliberately no reason column: the reason lives
-- on the retirement row, and events never copy profile content.
CREATE TABLE account_lifecycle_events (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id       INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    retirement_id BIGINT REFERENCES account_retirements(id) ON DELETE CASCADE,
    action        TEXT NOT NULL CHECK (action IN ('retired', 'restored')),
    source        TEXT NOT NULL CHECK (source IN ('SELF', 'ADMIN')),
    actor_id      INT REFERENCES users(id) ON DELETE SET NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX account_lifecycle_events_user ON account_lifecycle_events (user_id, id DESC);

-- Ban/Unban history. A ban is users.status = 'REJECTED'.
CREATE TABLE user_moderation_events (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id         INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    actor_id        INT REFERENCES users(id) ON DELETE SET NULL,
    action          TEXT NOT NULL CHECK (action IN ('ban', 'unban')),
    previous_status user_status NOT NULL,
    new_status      user_status NOT NULL,
    reason          TEXT NOT NULL DEFAULT '' CHECK (CHAR_LENGTH(reason) <= 2000),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX user_moderation_events_user ON user_moderation_events (user_id, id DESC);

-- Public content authored by a retiring account is hidden by predicate, not by
-- rewriting the content: its own moderation, expiry and closed states stay
-- untouched, so a restore brings back exactly what was eligible before. An
-- address or user id with no local account row (imported or legacy records) is
-- never retiring, so those records keep their existing visibility.
CREATE FUNCTION account_retiring(address TEXT) RETURNS BOOLEAN
LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT address IS NOT NULL AND EXISTS (
        SELECT 1 FROM users u
        JOIN account_retirements r ON r.user_id = u.id AND r.restored_at IS NULL
        WHERE lower(u.blockchain_address) = lower(address))
$$;

CREATE FUNCTION user_retiring(user_id INT) RETURNS BOOLEAN
LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT EXISTS (
        SELECT 1 FROM account_retirements r
        WHERE r.user_id = user_retiring.user_id AND r.restored_at IS NULL)
$$;

-- Operator-initiated resource retirement shares the existing request tables, so
-- the source of a transition is recorded beside the request.
ALTER TABLE retirement_requests
    ADD COLUMN source TEXT NOT NULL DEFAULT 'OWNER' CHECK (source IN ('OWNER', 'ADMIN'));
-- A direct admin retirement may carry no reason; owner requests still require one
-- (enforced by the handler), so relax only the length floor.
ALTER TABLE retirement_requests DROP CONSTRAINT retirement_requests_reason_check;
ALTER TABLE retirement_requests
    ADD CONSTRAINT retirement_requests_reason_check
    CHECK (CHAR_LENGTH(BTRIM(reason)) <= 2000 AND (source = 'ADMIN' OR CHAR_LENGTH(BTRIM(reason)) >= 1));

---- create above / drop below ----

DROP FUNCTION IF EXISTS user_retiring(INT);
DROP FUNCTION IF EXISTS account_retiring(TEXT);
ALTER TABLE retirement_requests DROP CONSTRAINT retirement_requests_reason_check;
ALTER TABLE retirement_requests
    ADD CONSTRAINT retirement_requests_reason_check
    CHECK (length(btrim(reason)) BETWEEN 1 AND 2000);
ALTER TABLE retirement_requests DROP COLUMN source;
DROP TABLE IF EXISTS user_moderation_events;
DROP TABLE IF EXISTS account_lifecycle_events;
DROP TABLE IF EXISTS account_retirements;
