package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

type (
	postContactWire struct {
		ID             string         `json:"id"`
		PostID         string         `json:"postId"`
		PostTitle      string         `json:"postTitle"`
		Direction      string         `json:"direction"`
		Status         string         `json:"status"`
		Intro          *string        `json:"intro"`
		ConversationID *int64         `json:"conversationId"`
		Requester      *messagingPeer `json:"requester"`
		CreatedAt      time.Time      `json:"createdAt"`
	}
)

func postContactResult(request store.PostContactRequest, viewer int64) postContactWire {
	item := postContactWire{
		ID: request.ID, PostID: strconv.FormatInt(request.PostID, 10), PostTitle: request.PostTitle,
		Status: request.Status, Intro: request.Intro, CreatedAt: request.CreatedAt,
		Direction: "outgoing",
	}
	if request.Status == store.ConversationAccepted {
		item.ConversationID = request.ConversationID
	}
	if request.AuthorID != nil && viewer == *request.AuthorID {
		item.Direction = "incoming"
		if request.RequesterAddress != nil {
			item.Requester = &messagingPeer{
				Address: checksum(*request.RequesterAddress), Username: request.RequesterUsername,
				Avatar: request.RequesterAvatar,
			}
		}
	}
	return item
}

func (a *API) listPostContactRequestsHandler(c *echo.Context) error {
	var after *string
	if cursor := c.QueryParam("cursor"); cursor != "" {
		id, err := uuid.Parse(cursor)
		if err != nil {
			return messagingRespond(c, messagingInvalid("invalid cursor"))
		}
		canonical := id.String()
		after = &canonical
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
	rows, err := a.store.ListPostContacts(ctx, tx, caller, after, limit+1)
	if err != nil {
		return err
	}
	var next *string
	if len(rows) > limit {
		rows = rows[:limit]
		next = &rows[limit-1].ID
	}
	items := make([]postContactWire, 0, len(rows))
	for _, row := range rows {
		items = append(items, postContactResult(row, caller))
	}
	return messagingResult(c, http.StatusOK, map[string]any{"items": items, "nextCursor": next})
}

func (a *API) createPostContactRequestHandler(c *echo.Context) error {
	var req struct {
		PostID string  `json:"postId"`
		Intro  *string `json:"intro"`
	}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	postID, err := strconv.ParseInt(req.PostID, 10, 32)
	if err != nil || postID <= 0 || strconv.FormatInt(postID, 10) != req.PostID {
		return messagingRespond(c, messagingInvalid("postId must be a positive integer string"))
	}
	intro, err := optionalMessagingText(req.Intro, introMax, "intro")
	if err != nil {
		return messagingRespond(c, err)
	}
	caller, address := messagingCaller(c)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	request, err := a.store.PostContactForRequester(ctx, tx, caller, postID)
	if err == nil {
		return messagingResult(c, http.StatusOK, postContactResult(request, caller))
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if _, err := a.store.MessagingApprovedUserByAddress(ctx, tx, address); err != nil {
		return messagingRespond(c, err)
	}
	target, err := a.store.PostContactTarget(ctx, tx, postID)
	if err != nil {
		return messagingRespond(c, err)
	}
	request, err = a.store.InsertPostContact(ctx, tx, uuid.NewString(), caller, postID, target.Title, intro)
	if errors.Is(err, pgx.ErrNoRows) {
		// A concurrent submission committed first. It spent quota and chose
		// delivery, so this attempt returns the saved request unchanged.
		request, err = a.store.PostContactForRequester(ctx, tx, caller, postID)
		if err != nil {
			return err
		}
		return messagingResult(c, http.StatusOK, postContactResult(request, caller))
	}
	if err != nil {
		return err
	}
	allowed, err := a.store.ConsumeMessagingRequestQuota(ctx, tx, caller, messagingDailyRequestQuota)
	if err != nil {
		return err
	}
	if !allowed {
		return messagingQuotaExceeded(c, time.Now())
	}
	// Both deliverable and suppressed requests produce the same response and
	// requester history. In particular a caller cannot detect an author by
	// blocking a guessed account and checking the outcome.
	if target.AuthorID != nil && *target.AuthorID != caller {
		author := *target.AuthorID
		if err := a.store.LockPair(ctx, tx, min(caller, author), max(caller, author)); err != nil {
			return err
		}
		blocked, err := a.store.BlockedEitherWay(ctx, tx, caller, author)
		if err != nil {
			return err
		}
		if !blocked {
			if err := a.store.AssignPostContactAuthor(ctx, tx, request.ID, author); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return messagingResult(c, http.StatusCreated, postContactResult(request, caller))
}

func (a *API) updatePostContactRequestHandler(c *echo.Context) error {
	requestID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return messagingRespond(c, messagingInvalid("id must be a UUID"))
	}
	var req struct {
		Status string `json:"status"`
	}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if req.Status != store.ConversationAccepted && req.Status != store.ConversationDeclined {
		return messagingRespond(c, messagingInvalid("status must be accepted or declined"))
	}
	caller, _ := messagingCaller(c)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	request, err := a.store.LockPostContactForAuthor(ctx, tx, requestID.String(), caller)
	if err != nil {
		return messagingRespond(c, err)
	}
	if request.Status == req.Status {
		return messagingResult(c, http.StatusOK, postContactResult(request, caller))
	}
	if request.Status != store.ConversationPending {
		return messagingRespond(c, messagingConflict("the request has already been answered"))
	}
	low, high := min(request.RequesterID, caller), max(request.RequesterID, caller)
	if err := a.store.LockPair(ctx, tx, low, high); err != nil {
		return err
	}
	if err := a.store.LockDeliverablePostContact(ctx, tx, request.ID); err != nil {
		return messagingRespond(c, err)
	}
	if req.Status == store.ConversationAccepted {
		conversationID, err := a.acceptPostContact(ctx, tx, request, caller, low, high)
		if err != nil {
			return messagingRespond(c, err)
		}
		request.ConversationID = &conversationID
	}
	if err := a.store.SetPostContactStatus(ctx, tx, request.ID, req.Status, request.ConversationID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	request.Status = req.Status
	return messagingResult(c, http.StatusOK, postContactResult(request, caller))
}

func (a *API) acceptPostContact(ctx context.Context, tx pgx.Tx, request store.PostContactRequest, author, low, high int64) (int64, error) {
	// The pair lock serializes this with ordinary direct requests and blocks.
	// A request does not touch the ordinary conversation until the author
	// explicitly approves the link between this Post and their account.
	thread, err := a.store.LockDirectPair(ctx, tx, low, high)
	if errors.Is(err, pgx.ErrNoRows) {
		id, inserted, insertErr := a.store.InsertDirectConversation(ctx, tx, low, high, request.RequesterID, nil)
		if insertErr != nil {
			return 0, insertErr
		}
		if inserted {
			thread = store.ConversationMembers{ID: id, Status: store.ConversationPending, ActorID: &request.RequesterID}
		} else {
			thread, err = a.store.LockDirectPair(ctx, tx, low, high)
			if err != nil {
				return 0, err
			}
		}
	} else if err != nil {
		return 0, err
	}
	current, err := a.store.PostContactConsentCurrent(ctx, tx, thread.ID, request.RequesterID, request.CreatedAt)
	if err != nil {
		return 0, err
	}
	if !current {
		return 0, messagingConflict("a fresh contact request is required")
	}
	if err := a.ensureAcceptedPostContactThread(ctx, tx, thread, request.RequesterID, author); err != nil {
		return 0, err
	}
	if request.Intro != nil {
		message, _, err := a.store.InsertMessage(ctx, tx, store.MessageWrite{
			ConversationID: thread.ID, SenderUserID: request.RequesterID,
			ClientID: uuid.NewString(), Body: request.Intro,
		})
		if err != nil {
			return 0, err
		}
		if err := a.store.TouchConversation(ctx, tx, thread.ID, message.CreatedAt); err != nil {
			return 0, err
		}
	}
	return thread.ID, nil
}

func (a *API) ensureAcceptedPostContactThread(ctx context.Context, tx pgx.Tx, thread store.ConversationMembers, requester, author int64) error {
	if thread.Status == store.ConversationAccepted {
		return nil
	}
	responder := author
	if thread.Status == store.ConversationDeclined || thread.Status == store.ConversationRevoked {
		// A fresh request and the author's acceptance express both parties'
		// consent after deletion. Ordinary revocation still permits only the
		// participant who closed the thread to reopen it.
		if _, err := a.rerequest(ctx, tx, thread, author, nil); err != nil {
			return err
		}
		responder = requester
	} else if thread.ActorID != nil && *thread.ActorID == author {
		// The previously invited requester consented by submitting about this Post.
		responder = requester
	}
	changed, err := a.store.RespondDirectRequest(ctx, tx, thread.ID, responder, store.ConversationAccepted)
	if err != nil {
		return err
	}
	if !changed {
		return messagingConflict("the conversation is not in a state that allows this")
	}
	return nil
}
