package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/pkg/event"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// Admin messaging routes, mounted on /api/v2/admin behind requireAdmin. Every
// statement they run filters on kind = 'helpdesk': no admin route lists direct
// conversations or reads their messages. The one direct message an admin can
// see is the one attached to an abuse report.

// GET /admin/messaging/counts: the console's sidebar counters.
func (a *API) adminMessagingCountsHandler(c *echo.Context) error {
	ctx := c.Request().Context()
	tx, err := a.messagingReadTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	counts, err := a.store.AdminMessagingCounts(ctx, tx)
	if err != nil {
		return err
	}
	return messagingResult(c, http.StatusOK, map[string]int{
		"helpdeskNeedsReply": counts.HelpdeskNeedsReply,
		"abuseReportsOpen":   counts.AbuseReportsOpen,
	})
}

// GET /admin/helpdesk?status=&cursor=&limit=
func (a *API) adminListHelpdeskHandler(c *echo.Context) error {
	status := c.QueryParam("status")
	if status != "" && status != store.ConversationOpen && status != store.ConversationResolved {
		return messagingRespond(c, messagingInvalid("status must be open or resolved"))
	}
	after, err := decodeMessagingCursor(c.QueryParam("cursor"))
	if err != nil {
		return messagingRespond(c, err)
	}
	limit, err := parseMessagingLimit(c.QueryParam("limit"), conversationsLimit)
	if err != nil {
		return messagingRespond(c, err)
	}
	ctx := c.Request().Context()
	tx, err := a.messagingReadTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	rows, err := a.store.ListAdminHelpdesk(ctx, tx, status, after, limit+1)
	if err != nil {
		return err
	}
	var next *string
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1]
		cursor := encodeMessagingCursor(last.SortAt, last.ID)
		next = &cursor
	}
	items := make([]adminHelpdeskItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, adminHelpdeskWire(row))
	}
	return messagingResult(c, http.StatusOK, map[string]any{"items": items, "nextCursor": next})
}

// GET /admin/helpdesk/:id
func (a *API) adminGetHelpdeskHandler(c *echo.Context) error {
	id, err := messagingIDParam(c)
	if err != nil {
		return messagingRespond(c, err)
	}
	ctx := c.Request().Context()
	tx, err := a.messagingReadTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	row, err := a.store.GetAdminHelpdesk(ctx, tx, id)
	if err != nil {
		return messagingRespond(c, err)
	}
	return messagingResult(c, http.StatusOK, adminHelpdeskWire(row))
}

// GET /admin/helpdesk/:id/messages?before=&after=&limit=
func (a *API) adminListHelpdeskMessagesHandler(c *echo.Context) error {
	id, err := messagingIDParam(c)
	if err != nil {
		return messagingRespond(c, err)
	}
	page, err := parseMessagePage(c)
	if err != nil {
		return messagingRespond(c, err)
	}
	ctx := c.Request().Context()
	tx, err := a.messagingReadTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	conversation, err := a.store.AdminHelpdeskConversation(ctx, tx, id, false)
	if err != nil {
		return messagingRespond(c, err)
	}
	messages, more, err := a.store.ListMessages(ctx, tx, id, page)
	if err != nil {
		return err
	}
	items := make([]adminMessagingMessage, 0, len(messages))
	for _, message := range messages {
		items = append(items, adminMessageWire(conversation.UserAID, message))
	}
	return messagingResult(c, http.StatusOK, map[string]any{"items": items, "hasMore": more})
}

// POST /admin/helpdesk/:id/messages. An admin reply locks the helpdesk row like
// a user post and records the replying admin as the sender; the user sees
// "support" and gets a push.
func (a *API) adminPostHelpdeskMessageHandler(c *echo.Context) error {
	id, err := messagingIDParam(c)
	if err != nil {
		return messagingRespond(c, err)
	}
	input, err := parseMessageInput(c)
	if err != nil {
		return messagingRespond(c, err)
	}
	admin, adminAddress := messagingCaller(c)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	conversation, err := a.store.AdminHelpdeskConversation(ctx, tx, id, true)
	if err != nil {
		return messagingRespond(c, err)
	}
	if existing, found, err := storedMessage(ctx, a.store, tx, admin, input.ClientID, id); err != nil || found {
		if err != nil {
			return messagingRespond(c, err)
		}
		return messagingResult(c, http.StatusOK, adminMessageWire(conversation.UserAID, existing))
	}

	message, inserted, err := a.store.InsertMessage(ctx, tx, store.MessageWrite{
		ConversationID: id,
		SenderUserID:   admin,
		ClientID:       input.ClientID,
		Body:           input.Body,
		ImageURLs:      input.ImageURLs,
	})
	if err != nil {
		return err
	}
	if !inserted {
		existing, _, err := storedMessage(ctx, a.store, tx, admin, input.ClientID, id)
		if err != nil {
			return messagingRespond(c, err)
		}
		return messagingResult(c, http.StatusOK, adminMessageWire(conversation.UserAID, existing))
	}
	message.SenderAddress = adminAddress
	if err := a.store.TouchConversation(ctx, tx, id, message.CreatedAt); err != nil {
		return err
	}

	ev := newMessagingEvent(event.MessageCreated, id, store.ConversationHelpdesk, admin, adminAddress, message.CreatedAt)
	ev.MessageID = &message.ID
	ev.Audience = membersAudience(conversation, otherMember(conversation, admin))
	if err := a.enqueueMessagingEvent(c, tx, ev); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return messagingResult(c, http.StatusCreated, adminMessageWire(conversation.UserAID, message))
}

// PUT /admin/helpdesk/:id/status marks a helpdesk conversation open or
// resolved. Setting the state it already has changes nothing and sends no event.
func (a *API) adminSetHelpdeskStatusHandler(c *echo.Context) error {
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
	if req.Status != store.ConversationOpen && req.Status != store.ConversationResolved {
		return messagingRespond(c, messagingInvalid("status must be open or resolved"))
	}
	admin, adminAddress := messagingCaller(c)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	conversation, err := a.store.AdminHelpdeskConversation(ctx, tx, id, true)
	if err != nil {
		return messagingRespond(c, err)
	}
	changed, err := a.store.SetAdminHelpdeskStatus(ctx, tx, id, req.Status, admin)
	if err != nil {
		return err
	}
	if changed {
		ev := newMessagingEvent(event.HelpdeskStatusChanged, id, store.ConversationHelpdesk, admin, adminAddress, time.Now())
		ev.Audience = membersAudience(conversation, 0)
		if err := a.enqueueMessagingEvent(c, tx, ev); err != nil {
			return err
		}
	}
	row, err := a.store.GetAdminHelpdesk(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return messagingResult(c, http.StatusOK, adminHelpdeskWire(row))
}

// GET /admin/abuse-reports?status=&cursor=&limit=
func (a *API) adminListAbuseReportsHandler(c *echo.Context) error {
	status := c.QueryParam("status")
	switch status {
	case "", store.AbuseReportOpen, store.AbuseReportDismissed, store.AbuseReportActioned:
	default:
		return messagingRespond(c, messagingInvalid("status must be open, dismissed or actioned"))
	}
	after, err := decodeMessagingCursor(c.QueryParam("cursor"))
	if err != nil {
		return messagingRespond(c, err)
	}
	limit, err := parseMessagingLimit(c.QueryParam("limit"), conversationsLimit)
	if err != nil {
		return messagingRespond(c, err)
	}
	ctx := c.Request().Context()
	tx, err := a.messagingReadTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	rows, err := a.store.ListAbuseReports(ctx, tx, status, after, limit+1)
	if err != nil {
		return err
	}
	var next *string
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1]
		cursor := encodeMessagingCursor(last.CreatedAt, last.ID)
		next = &cursor
	}
	items := make([]adminAbuseReport, 0, len(rows))
	for _, row := range rows {
		items = append(items, abuseReportWire(row))
	}
	return messagingResult(c, http.StatusOK, map[string]any{"items": items, "nextCursor": next})
}

// PUT /admin/abuse-reports/:id decides an open report once: 404 for a missing
// report, 409 for one already decided.
func (a *API) adminResolveAbuseReportHandler(c *echo.Context) error {
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
	if req.Status != store.AbuseReportDismissed && req.Status != store.AbuseReportActioned {
		return messagingRespond(c, messagingInvalid("status must be dismissed or actioned"))
	}
	admin, _ := messagingCaller(c)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	resolved, err := a.store.ResolveAbuseReport(ctx, tx, id, req.Status, admin)
	if err != nil {
		return err
	}
	row, err := a.store.GetAbuseReport(ctx, tx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return messagingRespond(c, errMessagingNotFound)
	}
	if err != nil {
		return err
	}
	if !resolved {
		return messagingRespond(c, messagingConflict("the report was already decided"))
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return messagingResult(c, http.StatusOK, abuseReportWire(row))
}
