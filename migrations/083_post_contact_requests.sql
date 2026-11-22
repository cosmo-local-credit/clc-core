-- Keep an author's participation private until they accept contact about the
-- particular Post. A suppressed request has no recipient, but remains visible
-- to its requester with the same pending state as an ordinary request.
CREATE TABLE post_contact_requests (
    id UUID PRIMARY KEY,
    post_id INT NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
    post_title TEXT NOT NULL,
    requester_id INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    author_id INT REFERENCES users(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'accepted', 'declined')),
    intro TEXT CHECK (intro IS NULL OR CHAR_LENGTH(intro) <= 200),
    conversation_id BIGINT REFERENCES conversations(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (requester_id, post_id),
    CHECK (author_id IS NULL OR author_id <> requester_id),
    CHECK (author_id IS NOT NULL OR status = 'pending'),
    CHECK (status = 'accepted' OR conversation_id IS NULL)
);

CREATE INDEX post_contact_requests_requester_page ON post_contact_requests (requester_id, created_at DESC, id DESC);
CREATE INDEX post_contact_requests_author_page ON post_contact_requests (author_id, created_at DESC, id DESC);
CREATE INDEX post_contact_requests_post_idx ON post_contact_requests (post_id);
CREATE INDEX post_contact_requests_conversation_idx ON post_contact_requests (conversation_id)
    WHERE conversation_id IS NOT NULL;

-- Recheck pending delivery whenever the author reads or acts. A block,
-- moderated Post or ineligible account hides pending delivery without giving
-- the requester a way to probe which wallet received the request.
CREATE VIEW deliverable_post_contact_requests AS
SELECT r.id, r.author_id
FROM post_contact_requests r
JOIN reports p ON p.id = r.post_id
JOIN users author ON author.id = r.author_id
JOIN users requester ON requester.id = r.requester_id
WHERE r.status = 'pending'
  AND p.status = 'APPROVED'
  AND lower(p.author_address) = lower(author.blockchain_address)
  AND author.status = 'APPROVED' AND NOT user_retiring(author.id)
  AND requester.status = 'APPROVED' AND NOT user_retiring(requester.id)
  AND NOT EXISTS (
      SELECT 1 FROM user_blocks b
      WHERE (b.blocker_id = r.requester_id AND b.blocked_id = r.author_id)
         OR (b.blocker_id = r.author_id AND b.blocked_id = r.requester_id)
  );
