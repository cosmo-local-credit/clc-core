-- A member clears only their own copy. Shared messages remain available to
-- the other participant and to authorized abuse-report moderation.
CREATE TABLE conversation_deletions (
    conversation_id BIGINT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    user_id INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    cleared_through_message_id BIGINT NOT NULL CHECK (cleared_through_message_id >= 0),
    cleared_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    hidden BOOLEAN NOT NULL DEFAULT true,
    PRIMARY KEY (conversation_id, user_id)
);
