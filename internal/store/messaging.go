package store

import (
	"context"
	"errors"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

// Messaging storage (migration 074). Every method runs in the caller's
// transaction so a state change, its read-back and its outbox job commit or
// roll back together. Users are referenced by users.id here; the API layer maps
// them to checksummed addresses and never returns an id.

const (
	ConversationDirect   = "direct"
	ConversationHelpdesk = "helpdesk"

	ConversationPending  = "pending"
	ConversationAccepted = "accepted"
	ConversationDeclined = "declined"
	ConversationRevoked  = "revoked"
	ConversationOpen     = "open"
	ConversationResolved = "resolved"

	AbuseReportOpen      = "open"
	AbuseReportDismissed = "dismissed"
	AbuseReportActioned  = "actioned"
)

type (
	MessagingStore interface {
		MessagingApprovedUserByAddress(context.Context, pgx.Tx, string) (MessagingUser, error)
		MessagingUserByAddress(context.Context, pgx.Tx, string) (MessagingUser, error)
		GetConversationView(context.Context, pgx.Tx, int64, int64) (ConversationView, error)
		ListConversationViews(context.Context, pgx.Tx, int64, string, *Keyset, int) ([]ConversationView, error)
		MessagingUnreadCounts(context.Context, pgx.Tx, int64) (MessagingUnread, error)
		MemberConversation(context.Context, pgx.Tx, int64, int64, bool) (ConversationMembers, error)
		LockPair(context.Context, pgx.Tx, int64, int64) error
		LockDirectPair(context.Context, pgx.Tx, int64, int64) (ConversationMembers, error)
		AdminHelpdeskConversation(context.Context, pgx.Tx, int64, bool) (ConversationMembers, error)
		InsertDirectConversation(context.Context, pgx.Tx, int64, int64, int64, *string) (int64, bool, error)
		RerequestDirectConversation(context.Context, pgx.Tx, int64, int64, *string) (bool, error)
		RespondDirectRequest(context.Context, pgx.Tx, int64, int64, string) (bool, error)
		RevokeDirectConversation(context.Context, pgx.Tx, int64, int64) (bool, error)
		BlockedEitherWay(context.Context, pgx.Tx, int64, int64) (bool, error)
		Blocks(context.Context, pgx.Tx, int64, int64) (bool, error)
		MessageByClientID(context.Context, pgx.Tx, int64, string) (Message, error)
		InsertMessage(context.Context, pgx.Tx, MessageWrite) (Message, bool, error)
		TouchConversation(context.Context, pgx.Tx, int64, time.Time) error
		ReopenHelpdesk(context.Context, pgx.Tx, int64, int64) (bool, error)
		ListMessages(context.Context, pgx.Tx, int64, MessagePage) ([]Message, bool, error)
		AdvanceReadMarker(context.Context, pgx.Tx, int64, int64, int64) (bool, error)
		MessageInConversation(context.Context, pgx.Tx, int64, int64) (bool, error)
		EnsureHelpdesk(context.Context, pgx.Tx, int64) (int64, bool, error)
		ListBlocks(context.Context, pgx.Tx, int64) ([]BlockedUser, error)
		InsertBlock(context.Context, pgx.Tx, int64, int64) (bool, error)
		DeleteBlock(context.Context, pgx.Tx, int64, int64) (bool, error)
		ConsumeMessagingRequestQuota(context.Context, pgx.Tx, int64, int) (bool, error)
		ReportableMessage(context.Context, pgx.Tx, int64, int64, int64) (bool, error)
		InsertAbuseReport(context.Context, pgx.Tx, int64, int64, *int64, *string) (int64, error)
		ListAdminHelpdesk(context.Context, pgx.Tx, string, *Keyset, int) ([]AdminHelpdeskRow, error)
		GetAdminHelpdesk(context.Context, pgx.Tx, int64) (AdminHelpdeskRow, error)
		SetAdminHelpdeskStatus(context.Context, pgx.Tx, int64, string, int64) (bool, error)
		ListAbuseReports(context.Context, pgx.Tx, string, *Keyset, int) ([]AbuseReportRow, error)
		GetAbuseReport(context.Context, pgx.Tx, int64) (AbuseReportRow, error)
		ResolveAbuseReport(context.Context, pgx.Tx, int64, string, int64) (bool, error)
	}

	// Keyset is the (sort time, id) position of the last row of a page.
	Keyset struct {
		At time.Time
		ID int64
	}

	MessagingUser struct {
		ID       int64   `db:"id"`
		Address  string  `db:"blockchain_address"`
		Username *string `db:"username"`
		Avatar   *string `db:"avatar_url"`
	}

	// ConversationMembers is a conversation row with its members' addresses,
	// loaded by a query that already proved the caller may see it.
	ConversationMembers struct {
		ID           int64   `db:"id"`
		Kind         string  `db:"kind"`
		Status       string  `db:"status"`
		ActorID      *int64  `db:"actor_id"`
		UserAID      int64   `db:"user_a_id"`
		UserAAddress string  `db:"user_a_address"`
		UserBID      *int64  `db:"user_b_id"`
		UserBAddress *string `db:"user_b_address"`
	}

	// ConversationView is one conversation as its caller sees it.
	ConversationView struct {
		ID                   int64      `db:"id"`
		Kind                 string     `db:"kind"`
		Status               string     `db:"status"`
		ActorID              *int64     `db:"actor_id"`
		Intro                *string    `db:"intro"`
		LastMessageAt        *time.Time `db:"last_message_at"`
		CreatedAt            time.Time  `db:"created_at"`
		UpdatedAt            time.Time  `db:"updated_at"`
		SortAt               time.Time  `db:"sort_at"`
		PeerID               *int64     `db:"peer_id"`
		PeerAddress          *string    `db:"peer_address"`
		PeerUsername         *string    `db:"peer_username"`
		PeerAvatar           *string    `db:"peer_avatar"`
		Blocked              bool       `db:"blocked"`
		LastReadMessageID    *int64     `db:"last_read_message_id"`
		UnreadCount          int        `db:"unread_count"`
		LastMessageID        *int64     `db:"last_message_id"`
		LastMessageClientID  *string    `db:"last_message_client_id"`
		LastMessageSenderID  *int64     `db:"last_message_sender_id"`
		LastMessageBody      *string    `db:"last_message_body"`
		LastMessageImageURLs []string   `db:"last_message_image_urls"`
		LastMessageCreatedAt *time.Time `db:"last_message_created_at"`
	}

	MessagingUnread struct {
		Direct   int `db:"direct"`
		Requests int `db:"requests"`
		Helpdesk int `db:"helpdesk"`
	}

	Message struct {
		ID             int64     `db:"id"`
		ConversationID int64     `db:"conversation_id"`
		SenderUserID   int64     `db:"sender_user_id"`
		SenderAddress  string    `db:"sender_address"`
		ClientID       string    `db:"client_id"`
		Body           *string   `db:"body"`
		ImageURLs      []string  `db:"image_urls"`
		CreatedAt      time.Time `db:"created_at"`
	}

	MessageWrite struct {
		ConversationID int64
		SenderUserID   int64
		ClientID       string
		Body           *string
		ImageURLs      []string
	}

	// MessagePage selects the newest Limit messages below Before (0 for the
	// newest overall), or the oldest Limit above After when After > 0.
	MessagePage struct {
		Before int64
		After  int64
		Limit  int
	}

	BlockedUser struct {
		Address   string    `db:"blockchain_address"`
		Username  *string   `db:"username"`
		Avatar    *string   `db:"avatar_url"`
		CreatedAt time.Time `db:"created_at"`
	}

	AdminHelpdeskRow struct {
		ID                       int64      `db:"id"`
		Status                   string     `db:"status"`
		LastMessageAt            *time.Time `db:"last_message_at"`
		CreatedAt                time.Time  `db:"created_at"`
		UpdatedAt                time.Time  `db:"updated_at"`
		SortAt                   time.Time  `db:"sort_at"`
		UserID                   int64      `db:"user_id"`
		UserAddress              string     `db:"user_address"`
		UserUsername             *string    `db:"user_username"`
		UserAvatar               *string    `db:"user_avatar"`
		LastMessageID            *int64     `db:"last_message_id"`
		LastMessageClientID      *string    `db:"last_message_client_id"`
		LastMessageSenderID      *int64     `db:"last_message_sender_id"`
		LastMessageSenderAddress *string    `db:"last_message_sender_address"`
		LastMessageBody          *string    `db:"last_message_body"`
		LastMessageImageURLs     []string   `db:"last_message_image_urls"`
		LastMessageCreatedAt     *time.Time `db:"last_message_created_at"`
	}

	AbuseReportRow struct {
		ID                int64      `db:"id"`
		Status            string     `db:"status"`
		Comment           *string    `db:"comment"`
		ResolvedAt        *time.Time `db:"resolved_at"`
		CreatedAt         time.Time  `db:"created_at"`
		ReporterAddress   string     `db:"reporter_address"`
		ReporterUsername  *string    `db:"reporter_username"`
		ReporterAvatar    *string    `db:"reporter_avatar"`
		ReportedAddress   string     `db:"reported_address"`
		ReportedUsername  *string    `db:"reported_username"`
		ReportedAvatar    *string    `db:"reported_avatar"`
		MessageID         *int64     `db:"message_id"`
		MessageBody       *string    `db:"message_body"`
		MessageImageURLs  []string   `db:"message_image_urls"`
		MessageCreatedAt  *time.Time `db:"message_created_at"`
		ResolvedByAddress *string    `db:"resolved_by_address"`
	}
)

func keysetArgs(after *Keyset) (*time.Time, int64) {
	if after == nil {
		return nil, 0
	}
	return &after.At, after.ID
}

// returnedRow runs an UPDATE ... RETURNING id and reports whether it matched.
func returnedRow(ctx context.Context, tx pgx.Tx, query string, args ...any) (bool, error) {
	var id int64
	err := tx.QueryRow(ctx, query, args...).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func scanExists(ctx context.Context, tx pgx.Tx, query string, args ...any) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, query, args...).Scan(&exists)
	return exists, err
}

func (pg *Pg) MessagingApprovedUserByAddress(ctx context.Context, tx pgx.Tx, address string) (MessagingUser, error) {
	var user MessagingUser
	err := pgxscan.Get(ctx, tx, &user, pg.queries.MessagingApprovedUserByAddress, address)
	return user, err
}

func (pg *Pg) MessagingUserByAddress(ctx context.Context, tx pgx.Tx, address string) (MessagingUser, error) {
	var user MessagingUser
	err := pgxscan.Get(ctx, tx, &user, pg.queries.MessagingUserByAddress, address)
	return user, err
}

// GetConversationView returns pgx.ErrNoRows for a missing id and a non-member alike.
func (pg *Pg) GetConversationView(ctx context.Context, tx pgx.Tx, caller, id int64) (ConversationView, error) {
	var view ConversationView
	err := pgxscan.Get(ctx, tx, &view, pg.queries.MessagingGetConversation, caller, id)
	return view, err
}

func (pg *Pg) ListConversationViews(ctx context.Context, tx pgx.Tx, caller int64, kind string, after *Keyset, limit int) ([]ConversationView, error) {
	at, id := keysetArgs(after)
	var views []ConversationView
	err := pgxscan.Select(ctx, tx, &views, pg.queries.MessagingListConversations, caller, kind, at, id, limit)
	return views, err
}

func (pg *Pg) MessagingUnreadCounts(ctx context.Context, tx pgx.Tx, caller int64) (MessagingUnread, error) {
	var unread MessagingUnread
	err := pgxscan.Get(ctx, tx, &unread, pg.queries.MessagingUnreadCounts, caller)
	return unread, err
}

// MemberConversation loads a conversation the caller belongs to, locking it
// when lock is set. pgx.ErrNoRows covers a missing id and a non-member alike.
func (pg *Pg) MemberConversation(ctx context.Context, tx pgx.Tx, caller, id int64, lock bool) (ConversationMembers, error) {
	query := pg.queries.MessagingMemberConversation
	if lock {
		query = pg.queries.MessagingLockMemberConversation
	}
	var members ConversationMembers
	err := pgxscan.Get(ctx, tx, &members, query, caller, id)
	return members, err
}

// LockPair takes the pair's transaction-scoped advisory lock, held by a
// request and a block change until commit; low < high.
func (pg *Pg) LockPair(ctx context.Context, tx pgx.Tx, low, high int64) error {
	_, err := tx.Exec(ctx, pg.queries.MessagingLockPair, low, high)
	return err
}

// LockDirectPair locks the pair's direct conversation; low < high.
func (pg *Pg) LockDirectPair(ctx context.Context, tx pgx.Tx, low, high int64) (ConversationMembers, error) {
	var members ConversationMembers
	err := pgxscan.Get(ctx, tx, &members, pg.queries.MessagingLockDirectPair, low, high)
	return members, err
}

func (pg *Pg) AdminHelpdeskConversation(ctx context.Context, tx pgx.Tx, id int64, lock bool) (ConversationMembers, error) {
	query := pg.queries.MessagingAdminHelpdeskConversation
	if lock {
		query = pg.queries.MessagingAdminLockHelpdeskConversation
	}
	var members ConversationMembers
	err := pgxscan.Get(ctx, tx, &members, query, id)
	return members, err
}

// InsertDirectConversation creates a pending request from requester; low <
// high. It reports false when the pair already has a row.
func (pg *Pg) InsertDirectConversation(ctx context.Context, tx pgx.Tx, low, high, requester int64, intro *string) (int64, bool, error) {
	var id int64
	err := tx.QueryRow(ctx, pg.queries.MessagingInsertDirectConversation, low, high, requester, intro).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

func (pg *Pg) RerequestDirectConversation(ctx context.Context, tx pgx.Tx, id, caller int64, intro *string) (bool, error) {
	return returnedRow(ctx, tx, pg.queries.MessagingRerequestDirectConversation, id, caller, intro)
}

func (pg *Pg) RespondDirectRequest(ctx context.Context, tx pgx.Tx, id, caller int64, status string) (bool, error) {
	return returnedRow(ctx, tx, pg.queries.MessagingRespondDirectRequest, id, caller, status)
}

func (pg *Pg) RevokeDirectConversation(ctx context.Context, tx pgx.Tx, id, caller int64) (bool, error) {
	return returnedRow(ctx, tx, pg.queries.MessagingRevokeDirectConversation, id, caller)
}

func (pg *Pg) BlockedEitherWay(ctx context.Context, tx pgx.Tx, a, b int64) (bool, error) {
	return scanExists(ctx, tx, pg.queries.MessagingBlockedEitherWay, a, b)
}

// Blocks reports whether blocker has blocked blocked.
func (pg *Pg) Blocks(ctx context.Context, tx pgx.Tx, blocker, blocked int64) (bool, error) {
	return scanExists(ctx, tx, pg.queries.MessagingBlocks, blocker, blocked)
}

func (pg *Pg) MessageByClientID(ctx context.Context, tx pgx.Tx, sender int64, clientID string) (Message, error) {
	var message Message
	err := pgxscan.Get(ctx, tx, &message, pg.queries.MessagingMessageByClientID, sender, clientID)
	return message, err
}

// InsertMessage stores a message and reports false, with no row, when the
// sender already used the client id.
func (pg *Pg) InsertMessage(ctx context.Context, tx pgx.Tx, write MessageWrite) (Message, bool, error) {
	images := write.ImageURLs
	if images == nil {
		images = []string{}
	}
	var message Message
	err := pgxscan.Get(ctx, tx, &message, pg.queries.MessagingInsertMessage,
		write.ConversationID, write.SenderUserID, write.ClientID, write.Body, images)
	if errors.Is(err, pgx.ErrNoRows) {
		return Message{}, false, nil
	}
	return message, err == nil, err
}

func (pg *Pg) TouchConversation(ctx context.Context, tx pgx.Tx, id int64, at time.Time) error {
	_, err := tx.Exec(ctx, pg.queries.MessagingTouchConversation, id, at)
	return err
}

func (pg *Pg) ReopenHelpdesk(ctx context.Context, tx pgx.Tx, id, caller int64) (bool, error) {
	return returnedRow(ctx, tx, pg.queries.MessagingReopenHelpdesk, id, caller)
}

// ListMessages returns one page in ascending id order and whether more
// messages lie beyond it in the paging direction.
func (pg *Pg) ListMessages(ctx context.Context, tx pgx.Tx, conversation int64, page MessagePage) ([]Message, bool, error) {
	var messages []Message
	if page.After > 0 {
		if err := pgxscan.Select(ctx, tx, &messages, pg.queries.MessagingMessagesAfter,
			conversation, page.After, page.Limit+1); err != nil {
			return nil, false, err
		}
		more := len(messages) > page.Limit
		if more {
			messages = messages[:page.Limit]
		}
		return messages, more, nil
	}

	before := page.Before
	if before <= 0 {
		before = 1<<63 - 1
	}
	if err := pgxscan.Select(ctx, tx, &messages, pg.queries.MessagingMessagesBefore,
		conversation, before, page.Limit+1); err != nil {
		return nil, false, err
	}
	more := len(messages) > page.Limit
	if more {
		messages = messages[:page.Limit]
	}
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
	return messages, more, nil
}

// AdvanceReadMarker reports whether the caller's marker moved.
func (pg *Pg) AdvanceReadMarker(ctx context.Context, tx pgx.Tx, conversation, caller, message int64) (bool, error) {
	var marker int64
	err := tx.QueryRow(ctx, pg.queries.MessagingAdvanceReadMarker, conversation, caller, message).Scan(&marker)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (pg *Pg) MessageInConversation(ctx context.Context, tx pgx.Tx, conversation, message int64) (bool, error) {
	return scanExists(ctx, tx, pg.queries.MessagingMessageInConversation, conversation, message)
}

// EnsureHelpdesk returns the user's helpdesk conversation, creating it open,
// and reports whether it was created.
func (pg *Pg) EnsureHelpdesk(ctx context.Context, tx pgx.Tx, user int64) (int64, bool, error) {
	var id int64
	err := tx.QueryRow(ctx, pg.queries.MessagingInsertHelpdesk, user).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, err
	}
	err = tx.QueryRow(ctx, pg.queries.MessagingHelpdeskID, user).Scan(&id)
	return id, false, err
}

func (pg *Pg) ListBlocks(ctx context.Context, tx pgx.Tx, blocker int64) ([]BlockedUser, error) {
	var blocks []BlockedUser
	err := pgxscan.Select(ctx, tx, &blocks, pg.queries.MessagingListBlocks, blocker)
	return blocks, err
}

func (pg *Pg) InsertBlock(ctx context.Context, tx pgx.Tx, blocker, blocked int64) (bool, error) {
	tag, err := tx.Exec(ctx, pg.queries.MessagingInsertBlock, blocker, blocked)
	return err == nil && tag.RowsAffected() == 1, err
}

func (pg *Pg) DeleteBlock(ctx context.Context, tx pgx.Tx, blocker, blocked int64) (bool, error) {
	tag, err := tx.Exec(ctx, pg.queries.MessagingDeleteBlock, blocker, blocked)
	return err == nil && tag.RowsAffected() == 1, err
}

// ConsumeMessagingRequestQuota counts one request against today's (UTC)
// allowance and reports false once limit requests have been used.
func (pg *Pg) ConsumeMessagingRequestQuota(ctx context.Context, tx pgx.Tx, user int64, limit int) (bool, error) {
	var used int
	err := tx.QueryRow(ctx, pg.queries.MessagingConsumeRequestQuota, user, limit).Scan(&used)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (pg *Pg) ReportableMessage(ctx context.Context, tx pgx.Tx, message, reported, reporter int64) (bool, error) {
	return scanExists(ctx, tx, pg.queries.MessagingReportableMessage, message, reported, reporter)
}

func (pg *Pg) InsertAbuseReport(ctx context.Context, tx pgx.Tx, reporter, reported int64, message *int64, comment *string) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, pg.queries.MessagingInsertAbuseReport, reporter, reported, message, comment).Scan(&id)
	return id, err
}

func (pg *Pg) ListAdminHelpdesk(ctx context.Context, tx pgx.Tx, status string, after *Keyset, limit int) ([]AdminHelpdeskRow, error) {
	at, id := keysetArgs(after)
	var rows []AdminHelpdeskRow
	err := pgxscan.Select(ctx, tx, &rows, pg.queries.MessagingAdminListHelpdesk, status, at, id, limit)
	return rows, err
}

func (pg *Pg) GetAdminHelpdesk(ctx context.Context, tx pgx.Tx, id int64) (AdminHelpdeskRow, error) {
	var row AdminHelpdeskRow
	err := pgxscan.Get(ctx, tx, &row, pg.queries.MessagingAdminGetHelpdesk, id)
	return row, err
}

func (pg *Pg) SetAdminHelpdeskStatus(ctx context.Context, tx pgx.Tx, id int64, status string, admin int64) (bool, error) {
	return returnedRow(ctx, tx, pg.queries.MessagingAdminSetHelpdeskStatus, id, status, admin)
}

func (pg *Pg) ListAbuseReports(ctx context.Context, tx pgx.Tx, status string, after *Keyset, limit int) ([]AbuseReportRow, error) {
	at, id := keysetArgs(after)
	var rows []AbuseReportRow
	err := pgxscan.Select(ctx, tx, &rows, pg.queries.MessagingAdminListAbuseReports, status, at, id, limit)
	return rows, err
}

func (pg *Pg) GetAbuseReport(ctx context.Context, tx pgx.Tx, id int64) (AbuseReportRow, error) {
	var row AbuseReportRow
	err := pgxscan.Get(ctx, tx, &row, pg.queries.MessagingAdminGetAbuseReport, id)
	return row, err
}

func (pg *Pg) ResolveAbuseReport(ctx context.Context, tx pgx.Tx, id int64, status string, admin int64) (bool, error) {
	return returnedRow(ctx, tx, pg.queries.MessagingAdminResolveAbuseReport, id, status, admin)
}
