package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/pkg/event"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// User messaging routes. Each /conversations/:id route proves membership in
// the same query that loads the row, so a non-member, a missing id and an
// admin outside a direct conversation all get the same 404. A role grants
// nothing here.

func (a *API) messagingReadTx(ctx context.Context) (pgx.Tx, error) {
	return a.store.Pool().BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
}

// GET /conversations?kind=&cursor=&limit=
func (a *API) listConversationsHandler(c *echo.Context) error {
	kind := c.QueryParam("kind")
	if kind != "" && kind != store.ConversationDirect && kind != store.ConversationHelpdesk {
		return messagingRespond(c, messagingInvalid("kind must be direct or helpdesk"))
	}
	after, err := decodeMessagingCursor(c.QueryParam("cursor"))
	if err != nil {
		return messagingRespond(c, err)
	}
	limit, err := parseMessagingLimit(c.QueryParam("limit"), conversationsLimit)
	if err != nil {
		return messagingRespond(c, err)
	}
	caller, _ := messagingCaller(c)
	ctx := c.Request().Context()
	tx, err := a.messagingReadTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	views, err := a.store.ListConversationViews(ctx, tx, caller, kind, after, limit+1)
	if err != nil {
		return err
	}
	var next *string
	if len(views) > limit {
		views = views[:limit]
		last := views[limit-1]
		cursor := encodeMessagingCursor(last.SortAt, last.ID)
		next = &cursor
	}
	items := make([]messagingConversation, 0, len(views))
	for _, view := range views {
		items = append(items, conversationWire(view, caller))
	}
	return messagingResult(c, http.StatusOK, map[string]any{"items": items, "nextCursor": next})
}

// GET /conversations/unread
func (a *API) unreadConversationsHandler(c *echo.Context) error {
	caller, _ := messagingCaller(c)
	ctx := c.Request().Context()
	tx, err := a.messagingReadTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	unread, err := a.store.MessagingUnreadCounts(ctx, tx, caller)
	if err != nil {
		return err
	}
	return messagingResult(c, http.StatusOK, map[string]int{
		"direct":   unread.Direct,
		"requests": unread.Requests,
		"helpdesk": unread.Helpdesk,
	})
}

// POST /conversations requests a direct conversation. It spends one of the
// caller's daily requests; a request that fails rolls the increment back with
// everything else.
func (a *API) createConversationHandler(c *echo.Context) error {
	var req struct {
		RecipientAddress string  `json:"recipientAddress"`
		Intro            *string `json:"intro"`
	}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	address, err := messagingAddress(req.RecipientAddress, "recipientAddress")
	if err != nil {
		return messagingRespond(c, err)
	}
	intro, err := optionalMessagingText(req.Intro, introMax, "intro")
	if err != nil {
		return messagingRespond(c, err)
	}

	caller, callerAddress := messagingCaller(c)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	allowed, err := a.store.ConsumeMessagingRequestQuota(ctx, tx, caller, messagingDailyRequestQuota)
	if err != nil {
		return err
	}
	if !allowed {
		return messagingQuotaExceeded(c, time.Now())
	}

	recipient, err := a.store.MessagingApprovedUserByAddress(ctx, tx, address)
	if err != nil {
		return messagingRespond(c, err)
	}
	if recipient.ID == caller {
		return messagingRespond(c, errMessagingNotFound)
	}

	// The pair lock comes before the block check, and a block change takes it
	// too, so a block that commits while this request waits is seen here, row
	// or no row.
	low, high := min(caller, recipient.ID), max(caller, recipient.ID)
	if err := a.store.LockPair(ctx, tx, low, high); err != nil {
		return err
	}
	blocked, err := a.store.BlockedEitherWay(ctx, tx, caller, recipient.ID)
	if err != nil {
		return err
	}
	if blocked {
		return messagingRespond(c, errMessagingNotFound)
	}

	existing, err := a.store.LockDirectPair(ctx, tx, low, high)
	var conversationID int64
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		id, inserted, err := a.store.InsertDirectConversation(ctx, tx, low, high, caller, intro)
		if err != nil {
			return err
		}
		if inserted {
			conversationID = id
			break
		}
		// A concurrent first request for the pair committed first; decide
		// against its row.
		if existing, err = a.store.LockDirectPair(ctx, tx, low, high); err != nil {
			return err
		}
		fallthrough
	case err == nil:
		if conversationID, err = a.rerequest(ctx, tx, existing, caller, intro); err != nil {
			return messagingRespond(c, err)
		}
	default:
		return err
	}

	ev := newMessagingEvent(event.ConversationRequested, conversationID, store.ConversationDirect, caller, callerAddress, time.Now())
	ev.Audience = []event.MessagingAudience{
		audienceMember(recipient.ID, recipient.Address, true),
		audienceMember(caller, callerAddress, false),
	}
	if err := a.enqueueMessagingEvent(c, tx, ev); err != nil {
		return err
	}
	view, err := a.store.GetConversationView(ctx, tx, caller, conversationID)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return messagingResult(c, http.StatusCreated, conversationWire(view, caller))
}

// rerequest reopens an existing direct row. Only whoever declined or revoked
// it may; anything else is a 409.
func (a *API) rerequest(ctx context.Context, tx pgx.Tx, existing store.ConversationMembers, caller int64, intro *string) (int64, error) {
	if existing.Status != store.ConversationDeclined && existing.Status != store.ConversationRevoked {
		return 0, messagingConflict("a conversation with this person already exists")
	}
	reopened, err := a.store.RerequestDirectConversation(ctx, tx, existing.ID, caller, intro)
	if err != nil {
		return 0, err
	}
	if !reopened {
		return 0, messagingConflict("only the person who closed this conversation can reopen it")
	}
	return existing.ID, nil
}

// GET /conversations/:id
func (a *API) getConversationHandler(c *echo.Context) error {
	id, err := messagingIDParam(c)
	if err != nil {
		return messagingRespond(c, err)
	}
	caller, _ := messagingCaller(c)
	ctx := c.Request().Context()
	tx, err := a.messagingReadTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	view, err := a.store.GetConversationView(ctx, tx, caller, id)
	if err != nil {
		return messagingRespond(c, err)
	}
	return messagingResult(c, http.StatusOK, conversationWire(view, caller))
}

// PUT /conversations/:id/status accepts, declines or revokes. The row lock
// comes first and the guarded UPDATE decides, so a concurrent accept and revoke
// cannot both apply: a non-member gets 404, a member in the wrong state 409.
func (a *API) updateConversationStatusHandler(c *echo.Context) error {
	id, err := messagingIDParam(c)
	if err != nil {
		return messagingRespond(c, err)
	}
	var req struct {
		Status string `json:"status"`
	}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	switch req.Status {
	case store.ConversationAccepted, store.ConversationDeclined, store.ConversationRevoked:
	default:
		return messagingRespond(c, messagingInvalid("status must be accepted, declined or revoked"))
	}

	caller, callerAddress := messagingCaller(c)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	members, err := a.store.MemberConversation(ctx, tx, caller, id, true)
	if err != nil {
		return messagingRespond(c, err)
	}
	if members.Kind != store.ConversationDirect {
		return messagingRespond(c, messagingConflict("a helpdesk conversation has no request to answer"))
	}

	var changed bool
	if req.Status == store.ConversationRevoked {
		changed, err = a.store.RevokeDirectConversation(ctx, tx, id, caller)
	} else {
		changed, err = a.store.RespondDirectRequest(ctx, tx, id, caller, req.Status)
	}
	if err != nil {
		return err
	}
	if !changed {
		return messagingRespond(c, messagingConflict("the conversation is not in a state that allows this"))
	}

	kind := map[string]string{
		store.ConversationAccepted: event.ConversationAccepted,
		store.ConversationDeclined: event.ConversationDeclined,
		store.ConversationRevoked:  event.ConversationRevoked,
	}[req.Status]
	var pushTo int64
	if req.Status == store.ConversationAccepted {
		// The requester hears about an accept, unless they have since blocked
		// the person accepting.
		requester := otherMember(members, caller)
		blocked, err := a.store.Blocks(ctx, tx, requester, caller)
		if err != nil {
			return err
		}
		if !blocked {
			pushTo = requester
		}
	}
	ev := newMessagingEvent(kind, id, store.ConversationDirect, caller, callerAddress, time.Now())
	ev.Audience = membersAudience(members, pushTo)
	if err := a.enqueueMessagingEvent(c, tx, ev); err != nil {
		return err
	}
	view, err := a.store.GetConversationView(ctx, tx, caller, id)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return messagingResult(c, http.StatusOK, conversationWire(view, caller))
}

// GET /conversations/:id/messages?before=&after=&limit=
func (a *API) listConversationMessagesHandler(c *echo.Context) error {
	id, err := messagingIDParam(c)
	if err != nil {
		return messagingRespond(c, err)
	}
	page, err := parseMessagePage(c)
	if err != nil {
		return messagingRespond(c, err)
	}
	caller, _ := messagingCaller(c)
	ctx := c.Request().Context()
	tx, err := a.messagingReadTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	members, err := a.store.MemberConversation(ctx, tx, caller, id, false)
	if err != nil {
		return messagingRespond(c, err)
	}
	messages, more, err := a.store.ListMessages(ctx, tx, id, page)
	if err != nil {
		return err
	}
	items := make([]messagingMessage, 0, len(messages))
	for _, message := range messages {
		items = append(items, userMessageWire(members.Kind, caller, message))
	}
	return messagingResult(c, http.StatusOK, map[string]any{"items": items, "hasMore": more})
}

// storedMessage answers a send whose clientId the sender already used: the
// stored message with 200 and no second event, or a 409 when that clientId
// belongs to another conversation.
func storedMessage(ctx context.Context, s store.Store, tx pgx.Tx, sender int64, clientID string, conversation int64) (store.Message, bool, error) {
	existing, err := s.MessageByClientID(ctx, tx, sender, clientID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Message{}, false, nil
	}
	if err != nil {
		return store.Message{}, false, err
	}
	if existing.ConversationID != conversation {
		return store.Message{}, false, messagingConflict("clientId was already used for another message")
	}
	return existing, true, nil
}

// POST /conversations/:id/messages. One transaction, in the order the spec
// fixes: lock the row with membership, check status and blocks, insert under
// the lock, touch the conversation, enqueue the event, commit. Message ids in a
// conversation therefore increase in commit order.
func (a *API) postConversationMessageHandler(c *echo.Context) error {
	id, err := messagingIDParam(c)
	if err != nil {
		return messagingRespond(c, err)
	}
	input, err := parseMessageInput(c)
	if err != nil {
		return messagingRespond(c, err)
	}
	caller, callerAddress := messagingCaller(c)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	members, err := a.store.MemberConversation(ctx, tx, caller, id, true)
	if err != nil {
		return messagingRespond(c, err)
	}
	if existing, found, err := storedMessage(ctx, a.store, tx, caller, input.ClientID, id); err != nil || found {
		if err != nil {
			return messagingRespond(c, err)
		}
		return messagingResult(c, http.StatusOK, userMessageWire(members.Kind, caller, existing))
	}

	if members.Kind == store.ConversationDirect {
		if members.Status != store.ConversationAccepted {
			return messagingRespond(c, messagingConflict("the conversation is not accepted"))
		}
		blocked, err := a.store.BlockedEitherWay(ctx, tx, members.UserAID, derefInt64(members.UserBID))
		if err != nil {
			return err
		}
		if blocked {
			return messagingRespond(c, errMessagingNotFound)
		}
	}

	message, inserted, err := a.store.InsertMessage(ctx, tx, store.MessageWrite{
		ConversationID: id,
		SenderUserID:   caller,
		ClientID:       input.ClientID,
		Body:           input.Body,
		ImageURLs:      input.ImageURLs,
	})
	if err != nil {
		return err
	}
	if !inserted {
		// The same clientId committed concurrently, possibly elsewhere.
		existing, _, err := storedMessage(ctx, a.store, tx, caller, input.ClientID, id)
		if err != nil {
			return messagingRespond(c, err)
		}
		return messagingResult(c, http.StatusOK, userMessageWire(members.Kind, caller, existing))
	}
	message.SenderAddress = callerAddress
	if err := a.store.TouchConversation(ctx, tx, id, message.CreatedAt); err != nil {
		return err
	}

	created := newMessagingEvent(event.MessageCreated, id, members.Kind, caller, callerAddress, message.CreatedAt)
	created.MessageID = &message.ID
	if members.Kind == store.ConversationDirect {
		created.Audience = membersAudience(members, otherMember(members, caller))
	} else {
		// No admin push in v1; the user's other devices sync.
		created.Audience = membersAudience(members, 0)
	}
	if err := a.enqueueMessagingEvent(c, tx, created); err != nil {
		return err
	}

	if members.Kind == store.ConversationHelpdesk {
		reopened, err := a.store.ReopenHelpdesk(ctx, tx, id, caller)
		if err != nil {
			return err
		}
		if reopened {
			changed := newMessagingEvent(event.HelpdeskStatusChanged, id, store.ConversationHelpdesk, caller, callerAddress, message.CreatedAt)
			changed.Audience = membersAudience(members, 0)
			if err := a.enqueueMessagingEvent(c, tx, changed); err != nil {
				return err
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return messagingResult(c, http.StatusCreated, userMessageWire(members.Kind, caller, message))
}

// PUT /conversations/:id/read moves the caller's marker forward, only to a
// message of this conversation. An older id leaves it where it is (204); an id
// from another conversation, or one that does not exist, is a 404.
func (a *API) markConversationReadHandler(c *echo.Context) error {
	id, err := messagingIDParam(c)
	if err != nil {
		return messagingRespond(c, err)
	}
	var req struct {
		LastReadMessageID int64 `json:"lastReadMessageId"`
	}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if req.LastReadMessageID <= 0 {
		return messagingRespond(c, messagingInvalid("lastReadMessageId must be a positive message id"))
	}
	caller, callerAddress := messagingCaller(c)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	members, err := a.store.MemberConversation(ctx, tx, caller, id, false)
	if err != nil {
		return messagingRespond(c, err)
	}
	moved, err := a.store.AdvanceReadMarker(ctx, tx, id, caller, req.LastReadMessageID)
	if err != nil {
		return err
	}
	if !moved {
		exists, err := a.store.MessageInConversation(ctx, tx, id, req.LastReadMessageID)
		if err != nil {
			return err
		}
		if !exists {
			return messagingRespond(c, errMessagingNotFound)
		}
		return c.NoContent(http.StatusNoContent)
	}

	ev := newMessagingEvent(event.ConversationRead, id, members.Kind, caller, callerAddress, time.Now())
	ev.Audience = []event.MessagingAudience{audienceMember(caller, callerAddress, false)}
	if err := a.enqueueMessagingEvent(c, tx, ev); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

// POST /helpdesk returns the caller's helpdesk conversation, creating it open.
func (a *API) helpdeskConversationHandler(c *echo.Context) error {
	caller, callerAddress := messagingCaller(c)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	id, created, err := a.store.EnsureHelpdesk(ctx, tx, caller)
	if err != nil {
		return err
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		ev := newMessagingEvent(event.HelpdeskStatusChanged, id, store.ConversationHelpdesk, caller, callerAddress, time.Now())
		ev.Audience = []event.MessagingAudience{audienceMember(caller, callerAddress, false)}
		if err := a.enqueueMessagingEvent(c, tx, ev); err != nil {
			return err
		}
	}
	view, err := a.store.GetConversationView(ctx, tx, caller, id)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return messagingResult(c, status, conversationWire(view, caller))
}

// GET /blocks
func (a *API) listBlocksHandler(c *echo.Context) error {
	caller, _ := messagingCaller(c)
	ctx := c.Request().Context()
	tx, err := a.messagingReadTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	blocks, err := a.store.ListBlocks(ctx, tx, caller)
	if err != nil {
		return err
	}
	type blockItem struct {
		Address   string    `json:"address"`
		Username  *string   `json:"username"`
		Avatar    *string   `json:"avatar"`
		CreatedAt time.Time `json:"createdAt"`
	}
	items := make([]blockItem, 0, len(blocks))
	for _, block := range blocks {
		items = append(items, blockItem{
			Address:   checksum(block.Address),
			Username:  block.Username,
			Avatar:    block.Avatar,
			CreatedAt: block.CreatedAt.UTC(),
		})
	}
	return messagingResult(c, http.StatusOK, map[string]any{"items": items})
}

// PUT /blocks/:address and DELETE /blocks/:address. A block takes the pair
// lock, then the pair's direct conversation row, so a concurrent request or
// send either commits before the block or sees it. Only the blocker hears
// about it.
func (a *API) putBlockHandler(c *echo.Context) error {
	return a.changeBlock(c, true)
}

func (a *API) deleteBlockHandler(c *echo.Context) error {
	return a.changeBlock(c, false)
}

func (a *API) changeBlock(c *echo.Context, block bool) error {
	address, err := messagingAddress(c.Param("address"), "address")
	if err != nil {
		return messagingRespond(c, err)
	}
	caller, callerAddress := messagingCaller(c)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Blocking needs an approved user, like every other way of reaching one;
	// unblocking takes anyone already on the list.
	var target store.MessagingUser
	if block {
		target, err = a.store.MessagingApprovedUserByAddress(ctx, tx, address)
	} else {
		target, err = a.store.MessagingUserByAddress(ctx, tx, address)
	}
	if err != nil {
		return messagingRespond(c, err)
	}
	if target.ID == caller {
		return messagingRespond(c, errMessagingNotFound)
	}

	// The pair lock serializes this with a request, which checks blocks only
	// after taking it; the row lock serializes it with a send.
	low, high := min(caller, target.ID), max(caller, target.ID)
	if err := a.store.LockPair(ctx, tx, low, high); err != nil {
		return err
	}
	var conversationID int64
	pair, err := a.store.LockDirectPair(ctx, tx, low, high)
	switch {
	case err == nil:
		conversationID = pair.ID
	case !errors.Is(err, pgx.ErrNoRows):
		return err
	}

	var changed bool
	if block {
		changed, err = a.store.InsertBlock(ctx, tx, caller, target.ID)
	} else {
		changed, err = a.store.DeleteBlock(ctx, tx, caller, target.ID)
	}
	if err != nil {
		return err
	}
	if changed {
		ev := newMessagingEvent(event.BlockChanged, conversationID, store.ConversationDirect, caller, callerAddress, time.Now())
		ev.Audience = []event.MessagingAudience{audienceMember(caller, callerAddress, false)}
		if err := a.enqueueMessagingEvent(c, tx, ev); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

// POST /abuse-reports. An attached message must come from a conversation the
// reporter belongs to and must have been sent by the reported user.
func (a *API) createAbuseReportHandler(c *echo.Context) error {
	var req struct {
		ReportedAddress string  `json:"reportedAddress"`
		MessageID       *int64  `json:"messageId"`
		Comment         *string `json:"comment"`
	}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	address, err := messagingAddress(req.ReportedAddress, "reportedAddress")
	if err != nil {
		return messagingRespond(c, err)
	}
	comment, err := optionalMessagingText(req.Comment, reportCommentMax, "comment")
	if err != nil {
		return messagingRespond(c, err)
	}
	if req.MessageID != nil && *req.MessageID <= 0 {
		return messagingRespond(c, messagingInvalid("messageId must be a positive message id"))
	}

	caller, _ := messagingCaller(c)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	reported, err := a.store.MessagingUserByAddress(ctx, tx, address)
	if err != nil {
		return messagingRespond(c, err)
	}
	if reported.ID == caller {
		return messagingRespond(c, errMessagingNotFound)
	}
	if req.MessageID != nil {
		ok, err := a.store.ReportableMessage(ctx, tx, *req.MessageID, reported.ID, caller)
		if err != nil {
			return err
		}
		if !ok {
			return messagingRespond(c, errMessagingNotFound)
		}
	}
	id, err := a.store.InsertAbuseReport(ctx, tx, caller, reported.ID, req.MessageID, comment)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return messagingResult(c, http.StatusCreated, map[string]int64{"id": id})
}
