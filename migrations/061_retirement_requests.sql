ALTER TABLE pools ADD COLUMN active BOOLEAN NOT NULL DEFAULT true;

CREATE TABLE retirement_requests (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    voucher_id INT REFERENCES vouchers(id),
    pool_id INT REFERENCES pools(id),
    requester_address TEXT NOT NULL CHECK (requester_address ~ '^0x[0-9a-f]{40}$'),
    owner_address TEXT NOT NULL CHECK (owner_address ~ '^0x[0-9a-f]{40}$'),
    reason TEXT NOT NULL CHECK (length(btrim(reason)) BETWEEN 1 AND 2000),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected','cancelled','invalidated')),
    snapshot JSONB NOT NULL,
    requested_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    decision_at TIMESTAMPTZ,
    decision_by TEXT,
    decision_reason TEXT,
    decision_snapshot JSONB,
    CHECK (num_nonnulls(voucher_id, pool_id) = 1),
    CHECK ((status = 'pending') = (decision_at IS NULL))
);
CREATE UNIQUE INDEX retirement_requests_pending_voucher ON retirement_requests(voucher_id) WHERE status='pending';
CREATE UNIQUE INDEX retirement_requests_pending_pool ON retirement_requests(pool_id) WHERE status='pending';
CREATE INDEX retirement_requests_pending_queue ON retirement_requests(id DESC) WHERE status='pending';
CREATE INDEX retirement_requests_requester ON retirement_requests(requester_address,id DESC);

CREATE TABLE retirement_events (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    voucher_id INT REFERENCES vouchers(id),
    pool_id INT REFERENCES pools(id),
    request_id BIGINT REFERENCES retirement_requests(id),
    action TEXT NOT NULL CHECK (action IN ('requested','approved','rejected','cancelled','invalidated','restored')),
    actor_address TEXT NOT NULL CHECK (actor_address ~ '^0x[0-9a-f]{40}$'),
    reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (num_nonnulls(voucher_id, pool_id) = 1)
);
CREATE INDEX retirement_events_voucher ON retirement_events(voucher_id,id DESC);
CREATE INDEX retirement_events_pool ON retirement_events(pool_id,id DESC);

---- create above / drop below ----

-- Rollback must never silently expose a retired pool or discard lifecycle history.
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pools WHERE NOT active)
        OR EXISTS (SELECT 1 FROM retirement_requests)
        OR EXISTS (SELECT 1 FROM retirement_events) THEN
        RAISE EXCEPTION 'Cannot remove retirement schema while retained lifecycle data exists';
    END IF;
END $$;
DROP TABLE retirement_events;
DROP TABLE retirement_requests;
ALTER TABLE pools DROP COLUMN active;
