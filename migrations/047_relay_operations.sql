-- relay_operations makes POST /relayer/relay idempotent for callers that send
-- an operationId. A client that lost the response to a relay cannot tell
-- whether its job was queued, and signing a new request would risk a second
-- economic transfer. It resends the same signed request under the same id
-- instead, and the row below answers with the original tracking id.
--
-- request_digest binds the id to the payer, the chain and every field of the
-- signed request, so reusing an id for anything else is refused, and an id on
-- its own reveals nothing: only a caller holding the exact signed request can
-- look its submission up again.
--
-- Rows are inserted in the same transaction as the river job they track.
-- Nothing prunes them yet; they are a few hundred bytes each and only CLC's
-- card-collection flow sends an operationId. created_at is indexed so a
-- periodic sweep can be added without a further migration.
CREATE TABLE IF NOT EXISTS relay_operations (
    operation_id   UUID        PRIMARY KEY,
    user_address   TEXT        NOT NULL,
    request_digest BYTEA       NOT NULL,
    tracking_id    UUID        NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS relay_operations_created_at_idx
    ON relay_operations (created_at);
