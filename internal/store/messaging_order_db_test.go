package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// messagingOrderUsers inserts n approved users and returns their ids, lowest first.
func messagingOrderUsers(t *testing.T, ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, marker string, n int) []int64 {
	t.Helper()
	rows, err := q.Query(ctx, `
		INSERT INTO users (blockchain_address, name, email, status)
		SELECT '0x' || md5($1 || i::TEXT) || '00000000', $1, $1 || i::TEXT || '@example.test', 'APPROVED'
		FROM generate_series(1, $2::INT) i
		RETURNING id`, marker, n)
	require.NoError(t, err)
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	require.NoError(t, err)
	require.Len(t, ids, n)
	if ids[0] > ids[n-1] {
		for i, j := 0, len(ids)-1; i < j; i, j = i+1, j-1 {
			ids[i], ids[j] = ids[j], ids[i]
		}
	}
	return ids
}

// A request to reopen a conversation that has old messages is today's
// activity: it sorts above an older request and pages by that position.
func TestMessagingListSortsAReopenRequestByTheRequest(t *testing.T) {
	pg := recipientTestStore(t)
	ctx := context.Background()
	tx, err := pg.Pool().Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	ids := messagingOrderUsers(t, ctx, tx, "mo"+uuid.NewString()[:8], 3)
	caller, oldPeer, newerPeer := ids[0], ids[1], ids[2]

	var reopened, older int64
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO conversations (kind, user_a_id, user_b_id, status, actor_id, last_message_at, created_at, updated_at)
		VALUES ('direct', $1, $2, 'declined', $1, now() - interval '10 days', now() - interval '30 days', now() - interval '10 days')
		RETURNING id`, caller, oldPeer).Scan(&reopened))
	_, err = tx.Exec(ctx, `
		INSERT INTO messages (conversation_id, sender_user_id, client_id, body, created_at)
		VALUES ($1, $2, gen_random_uuid(), 'old', now() - interval '10 days')`, reopened, oldPeer)
	require.NoError(t, err)
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO conversations (kind, user_a_id, user_b_id, status, actor_id, created_at, updated_at)
		VALUES ('direct', $1, $2, 'pending', $2, now() - interval '1 day', now() - interval '1 day')
		RETURNING id`, caller, newerPeer).Scan(&older))

	intro := "Can we talk again?"
	ok, err := pg.RerequestDirectConversation(ctx, tx, reopened, caller, &intro)
	require.NoError(t, err)
	require.True(t, ok)

	first, err := pg.ListConversationViews(ctx, tx, caller, "", nil, 1)
	require.NoError(t, err)
	require.Len(t, first, 1)
	require.Equal(t, reopened, first[0].ID, "today's request sorts first")
	require.True(t, first[0].SortAt.After(*first[0].LastMessageAt))

	second, err := pg.ListConversationViews(ctx, tx, caller, "", &Keyset{At: first[0].SortAt, ID: first[0].ID}, 2)
	require.NoError(t, err)
	require.Len(t, second, 1)
	require.Equal(t, older, second[0].ID, "the cursor continues after the request's position")
}

// Two sends whose transactions start in one order and take the conversation
// lock in the other: the later-locked message is newer in id, timestamp and
// the conversation's activity.
func TestMessagingSendTimestampsFollowTheLockNotTheTransaction(t *testing.T) {
	pg := recipientTestStore(t)
	ctx := context.Background()

	marker := "ms" + uuid.NewString()[:8]
	ids := messagingOrderUsers(t, ctx, pg.Pool(), marker, 2)
	t.Cleanup(func() {
		_, _ = pg.Pool().Exec(context.Background(), `DELETE FROM conversations WHERE user_a_id = $1`, ids[0])
		_, _ = pg.Pool().Exec(context.Background(), `DELETE FROM users WHERE name = $1`, marker)
	})
	var conversation int64
	require.NoError(t, pg.Pool().QueryRow(ctx, `
		INSERT INTO conversations (kind, user_a_id, user_b_id, status, actor_id)
		VALUES ('direct', $1, $2, 'accepted', $1) RETURNING id`, ids[0], ids[1]).Scan(&conversation))

	send := func(tx pgx.Tx, sender int64, body string) Message {
		t.Helper()
		_, err := tx.Exec(ctx, pg.queries.MessagingLockMemberConversation, sender, conversation)
		require.NoError(t, err)
		message, inserted, err := pg.InsertMessage(ctx, tx, MessageWrite{
			ConversationID: conversation,
			SenderUserID:   sender,
			ClientID:       uuid.NewString(),
			Body:           &body,
		})
		require.NoError(t, err)
		require.True(t, inserted)
		require.NoError(t, pg.TouchConversation(ctx, tx, conversation, message.CreatedAt))
		return message
	}

	early, err := pg.Pool().Begin(ctx)
	require.NoError(t, err)
	defer early.Rollback(ctx)
	_, err = early.Exec(ctx, "SELECT 1")
	require.NoError(t, err)
	time.Sleep(20 * time.Millisecond)

	late, err := pg.Pool().Begin(ctx)
	require.NoError(t, err)
	defer late.Rollback(ctx)
	first := send(late, ids[1], "first")
	require.NoError(t, late.Commit(ctx))

	second := send(early, ids[0], "second")
	require.NoError(t, early.Commit(ctx))

	require.Greater(t, second.ID, first.ID)
	require.True(t, second.CreatedAt.After(first.CreatedAt),
		"second %s must be after first %s", second.CreatedAt, first.CreatedAt)

	var lastMessageAt time.Time
	require.NoError(t, pg.Pool().QueryRow(ctx,
		`SELECT last_message_at FROM conversations WHERE id = $1`, conversation).Scan(&lastMessageAt))
	require.True(t, lastMessageAt.Equal(second.CreatedAt), "activity is the newest message, %s", lastMessageAt)
}
