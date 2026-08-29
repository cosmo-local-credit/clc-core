-- Durable production Web Push state. The browser-generated subscription keys
-- belong to one installation; wallet links are separate so a shared device can
-- receive notifications for more than one authenticated account without making
-- any one account the owner of the device row.

CREATE TABLE IF NOT EXISTS notification_devices (
    device_id              TEXT PRIMARY KEY,
    endpoint               TEXT NOT NULL UNIQUE,
    expiration_time_ms     BIGINT,
    p256dh                  TEXT NOT NULL,
    auth_secret             TEXT NOT NULL,
    vapid_key_version       TEXT NOT NULL,
    platform                TEXT,
    app_version             TEXT,
    service_worker_version  TEXT,
    permission_state        TEXT NOT NULL DEFAULT 'granted',
    subscription_status     TEXT NOT NULL DEFAULT 'active',
    created_at              TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT notification_devices_id_length CHECK (
        CHAR_LENGTH(device_id) BETWEEN 1 AND 128
    ),
    CONSTRAINT notification_devices_endpoint_http CHECK (
        endpoint ~* '^https://'
    ),
    CONSTRAINT notification_devices_expiration_nonnegative CHECK (
        expiration_time_ms IS NULL OR expiration_time_ms >= 0
    ),
    CONSTRAINT notification_devices_permission_valid CHECK (
        permission_state IN ('default', 'denied', 'granted', 'unavailable')
    ),
    CONSTRAINT notification_devices_status_valid CHECK (
        subscription_status IN ('active', 'disabled', 'stale')
    )
);

CREATE TRIGGER update_notification_devices_timestamp
    BEFORE UPDATE ON notification_devices
    FOR EACH ROW EXECUTE PROCEDURE update_timestamp();

CREATE TABLE IF NOT EXISTS notification_device_wallets (
    device_id      TEXT NOT NULL REFERENCES notification_devices(device_id) ON DELETE CASCADE,
    user_id        INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    wallet_address TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (device_id, user_id),
    CONSTRAINT notification_device_wallets_address_format CHECK (
        wallet_address ~ '^0x[0-9a-fA-F]{40}$'
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS notification_device_wallets_address_unique
    ON notification_device_wallets (device_id, LOWER(wallet_address));
CREATE INDEX IF NOT EXISTS notification_device_wallets_wallet_idx
    ON notification_device_wallets (LOWER(wallet_address));

-- One logical event regardless of how many devices receive it. source_key is
-- derived from the authoritative upstream event and makes JetStream redelivery
-- idempotent.
CREATE TABLE IF NOT EXISTS notification_events (
    notification_id       UUID PRIMARY KEY,
    source_key            TEXT NOT NULL UNIQUE,
    kind                  TEXT NOT NULL,
    title                 TEXT NOT NULL,
    body                  TEXT NOT NULL,
    target_url            TEXT NOT NULL,
    wallet_address_target TEXT NOT NULL,
    priority              TEXT NOT NULL,
    created_at            TIMESTAMPTZ NOT NULL,
    expires_at            TIMESTAMPTZ,
    actions               JSONB NOT NULL DEFAULT '[]'::JSONB,
    voucher_id            TEXT,
    swap_id               TEXT,
    offramp_reference     TEXT,
    CONSTRAINT notification_events_kind_valid CHECK (
        kind IN ('voucher.received', 'swap.completed', 'offramp.completed')
    ),
    CONSTRAINT notification_events_title_length CHECK (
        CHAR_LENGTH(title) BETWEEN 1 AND 120
    ),
    CONSTRAINT notification_events_body_length CHECK (
        CHAR_LENGTH(body) BETWEEN 1 AND 240
    ),
    CONSTRAINT notification_events_target_relative CHECK (
        target_url ~ '^/'
    ),
    CONSTRAINT notification_events_wallet_format CHECK (
        wallet_address_target ~ '^0x[0-9a-fA-F]{40}$'
    ),
    CONSTRAINT notification_events_priority_valid CHECK (
        priority IN ('very-low', 'low', 'normal', 'high')
    ),
    CONSTRAINT notification_events_expiry_valid CHECK (
        expires_at IS NULL OR expires_at > created_at
    ),
    CONSTRAINT notification_events_actions_array CHECK (
        jsonb_typeof(actions) = 'array' AND jsonb_array_length(actions) <= 2
    )
);

-- revision is both the primary key and the monotonic per-device inbox cursor.
-- record_id is the stable opaque identity exposed to the client.
CREATE TABLE IF NOT EXISTS notification_deliveries (
    revision         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    record_id        UUID NOT NULL UNIQUE DEFAULT gen_random_uuid(),
    notification_id  UUID NOT NULL REFERENCES notification_events(notification_id) ON DELETE CASCADE,
    device_id        TEXT NOT NULL REFERENCES notification_devices(device_id) ON DELETE CASCADE,
    push_status      TEXT NOT NULL DEFAULT 'pending',
    push_status_code INT,
    push_error       TEXT,
    sent_at          TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    received_at      TIMESTAMPTZ,
    shown_at         TIMESTAMPTZ,
    clicked_at       TIMESTAMPTZ,
    action_id        TEXT,
    deleted_at       TIMESTAMPTZ,
    UNIQUE (notification_id, device_id),
    CONSTRAINT notification_deliveries_status_valid CHECK (
        push_status IN ('pending', 'sent', 'send-error', 'push-service-error', 'stale-subscription')
    ),
    CONSTRAINT notification_deliveries_status_code_valid CHECK (
        push_status_code IS NULL OR push_status_code BETWEEN 100 AND 599
    )
);

CREATE INDEX IF NOT EXISTS notification_deliveries_device_revision_idx
    ON notification_deliveries (device_id, revision);
CREATE INDEX IF NOT EXISTS notification_deliveries_pending_idx
    ON notification_deliveries (revision)
    WHERE push_status IN ('pending', 'send-error');

---- create above / drop below ----

DROP TABLE IF EXISTS notification_deliveries;
DROP TABLE IF EXISTS notification_events;
DROP TABLE IF EXISTS notification_device_wallets;
DROP TABLE IF EXISTS notification_devices;
