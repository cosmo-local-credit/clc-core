package store

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// The messaging reads that run on every poll must stay on their indexes once a
// conversation has history: the thread page, the membership lookup, the pair
// lock, the caller's conversation list and the unread counts. Seeds in a
// rolled-back transaction and checks pgx's generic cached plans.
func TestMessagingQueryPlans(t *testing.T) {
	pg := recipientTestStore(t)
	ctx := context.Background()
	tx, err := pg.Pool().Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	marker := "mp" + uuid.NewString()[:8]
	for _, statement := range []string{
		// A realistic users table, so a peer lookup is never cheaper as a scan;
		// the first 1500 of them hold conversations.
		`INSERT INTO users (blockchain_address, name, email, status)
		SELECT '0x' || md5('` + marker + `' || i::TEXT) || '00000000',
			CASE WHEN i <= 1500 THEN '` + marker + `' ELSE 'Plan User' END,
			'` + marker + `' || i::TEXT || '@example.test', 'APPROVED'
		FROM generate_series(1, 10000) i`,
		// Each talker talks to the next six, and has a helpdesk conversation.
		`INSERT INTO conversations (kind, user_a_id, user_b_id, status, actor_id)
		SELECT 'direct', a.id, b.id, 'accepted', a.id
		FROM users a JOIN users b ON b.id > a.id AND b.id <= a.id + 6
		WHERE a.name = '` + marker + `' AND b.name = '` + marker + `'`,
		`INSERT INTO conversations (kind, user_a_id, status, actor_id)
		SELECT 'helpdesk', id, 'open', id FROM users WHERE name = '` + marker + `'`,
		`INSERT INTO messages (conversation_id, sender_user_id, client_id, body)
		SELECT c.id, CASE WHEN i % 2 = 0 THEN c.user_a_id ELSE COALESCE(c.user_b_id, c.user_a_id) END,
			gen_random_uuid(), 'm' || i
		FROM conversations c
		JOIN users u ON u.id = c.user_a_id AND u.name = '` + marker + `'
		CROSS JOIN generate_series(1, 6) i`,
		`INSERT INTO conversation_reads (conversation_id, user_id, last_read_message_id)
		SELECT c.id, c.user_a_id, (SELECT max(id) FROM messages m WHERE m.conversation_id = c.id)
		FROM conversations c JOIN users u ON u.id = c.user_a_id AND u.name = '` + marker + `'`,
		"ANALYZE users", "ANALYZE conversations", "ANALYZE messages", "ANALYZE conversation_reads",
		"SET LOCAL plan_cache_mode = force_generic_plan",
	} {
		_, err = tx.Exec(ctx, statement)
		require.NoError(t, err, statement)
	}

	var caller, conversation, low, high int64
	require.NoError(t, tx.QueryRow(ctx, `
		SELECT c.user_a_id, c.id, c.user_a_id, c.user_b_id FROM conversations c
		JOIN users u ON u.id = c.user_a_id AND u.name = $1
		WHERE c.kind = 'direct' ORDER BY c.id DESC LIMIT 1`, marker).Scan(&caller, &conversation, &low, &high))

	// Membership is three index branches, never a scan of every conversation.
	membershipIndexes := []string{
		"conversations_direct_pair_unique", "conversations_user_b_idx",
		"conversations_helpdesk_user_unique", "conversation_reads_pkey", "messages_conversation_page_idx",
	}
	for _, tc := range []struct {
		name, query string
		args        []any
		indexes     []string
	}{
		{"messages_before", pg.queries.MessagingMessagesBefore, []any{conversation, int64(1 << 62), 51}, []string{"messages_conversation_page_idx"}},
		{"messages_after", pg.queries.MessagingMessagesAfter, []any{conversation, int64(1), 51}, []string{"messages_conversation_page_idx"}},
		{"member", pg.queries.MessagingMemberConversation, []any{caller, conversation}, []string{"conversations_pkey"}},
		// Either the pair index or user_b's is a single-row lookup; only a scan fails.
		{"pair", pg.queries.MessagingLockDirectPair, []any{low, high}, nil},
		{"get", pg.queries.MessagingGetConversation, []any{caller, conversation}, []string{"conversations_pkey", "users_pkey", "conversation_reads_pkey", "messages_conversation_page_idx"}},
		{"list", pg.queries.MessagingListConversations, []any{caller, "", nil, int64(0), 31}, membershipIndexes},
		{"unread", pg.queries.MessagingUnreadCounts, []any{caller}, membershipIndexes},
		// Every seeded user has an open helpdesk row, so joining all of users is
		// right here; the queue itself must come from its partial index.
		{"helpdesk_queue", pg.queries.MessagingAdminListHelpdesk, []any{"open", nil, int64(0), 31}, []string{"conversations_helpdesk_queue_idx", "messages_conversation_page_idx"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := tx.Query(ctx, "EXPLAIN (COSTS OFF) "+tc.query, tc.args...)
			require.NoError(t, err)
			lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
			require.NoError(t, err)
			plan := strings.Join(lines, "\n")
			t.Log(plan)
			tables := []string{"messages", "conversations", "users", "conversation_reads"}
			if tc.name == "helpdesk_queue" {
				tables = tables[:2]
			}
			for _, table := range tables {
				require.NotContains(t, plan, "Seq Scan on "+table)
			}
			for _, index := range tc.indexes {
				require.Contains(t, plan, index)
			}
		})
	}
}
