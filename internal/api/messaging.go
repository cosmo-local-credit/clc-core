package api

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/internal/worker"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/cosmo-local-credit/clc-core/pkg/event"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
	"golang.org/x/time/rate"
)

// Messaging (clc-app docs/MESSAGING.md). Storage refers to people by users.id;
// every response names them by checksummed address only.
//
// Limits:
//   - messagingDailyRequestQuota counts POST /conversations, the message
//     request, in messaging_request_quota inside the request's transaction. It
//     is the only route that spends it: sends have the per-minute limiter
//     below, and blocks, reports, read markers and helpdesk posts are not
//     requests to a stranger.
//   - messageRateLimit/messageRateBurst allow 30 messages per user per minute
//     through POST /conversations/:id/messages and the admin helpdesk reply,
//     per instance.
const (
	messagingDailyRequestQuota = 100

	messageRateLimit = rate.Limit(0.5) // one token every two seconds == 30/min
	messageRateBurst = 30

	messageBodyMax     = 2000
	messageImagesMax   = 4
	introMax           = 200
	reportCommentMax   = 1000
	conversationsLimit = 30
	messagesLimit      = 50
	messagingPageMax   = 100
)

type (
	messagingPeer struct {
		Address  string  `json:"address"`
		Username *string `json:"username"`
		Avatar   *string `json:"avatar"`
	}

	messagingMessage struct {
		ID             int64     `json:"id"`
		ConversationID int64     `json:"conversationId"`
		ClientID       string    `json:"clientId"`
		Sender         string    `json:"sender"`
		Body           *string   `json:"body"`
		ImageURLs      []string  `json:"imageUrls"`
		CreatedAt      time.Time `json:"createdAt"`
	}

	messagingConversation struct {
		ID                int64             `json:"id"`
		Kind              string            `json:"kind"`
		Status            string            `json:"status"`
		Peer              *messagingPeer    `json:"peer"`
		Intro             *string           `json:"intro"`
		RequestedBySelf   bool              `json:"requestedBySelf"`
		CanAccept         bool              `json:"canAccept"`
		CanRequest        bool              `json:"canRequest"`
		Blocked           bool              `json:"blocked"`
		LastMessage       *messagingMessage `json:"lastMessage"`
		LastMessageAt     *time.Time        `json:"lastMessageAt"`
		LastReadMessageID *int64            `json:"lastReadMessageId"`
		UnreadCount       int               `json:"unreadCount"`
		CreatedAt         time.Time         `json:"createdAt"`
		UpdatedAt         time.Time         `json:"updatedAt"`
	}

	adminMessagingMessage struct {
		ID             int64     `json:"id"`
		ConversationID int64     `json:"conversationId"`
		ClientID       string    `json:"clientId"`
		Sender         string    `json:"sender"`
		AdminAddress   *string   `json:"adminAddress"`
		Body           *string   `json:"body"`
		ImageURLs      []string  `json:"imageUrls"`
		CreatedAt      time.Time `json:"createdAt"`
	}

	adminHelpdeskItem struct {
		ID            int64                  `json:"id"`
		Status        string                 `json:"status"`
		User          messagingPeer          `json:"user"`
		LastMessage   *adminMessagingMessage `json:"lastMessage"`
		LastMessageAt *time.Time             `json:"lastMessageAt"`
		NeedsReply    bool                   `json:"needsReply"`
		CreatedAt     time.Time              `json:"createdAt"`
		UpdatedAt     time.Time              `json:"updatedAt"`
	}

	abuseReportMessage struct {
		ID        int64     `json:"id"`
		Body      *string   `json:"body"`
		ImageURLs []string  `json:"imageUrls"`
		CreatedAt time.Time `json:"createdAt"`
	}

	adminAbuseReport struct {
		ID         int64               `json:"id"`
		Status     string              `json:"status"`
		Reporter   messagingPeer       `json:"reporter"`
		Reported   messagingPeer       `json:"reported"`
		Message    *abuseReportMessage `json:"message"`
		Comment    *string             `json:"comment"`
		ResolvedBy *string             `json:"resolvedBy"`
		ResolvedAt *time.Time          `json:"resolvedAt"`
		CreatedAt  time.Time           `json:"createdAt"`
	}

	// messageInput is the validated body of a send, user or admin.
	messageInput struct {
		ClientID  string
		Body      *string
		ImageURLs []string
	}

	// messagingError is a response already decided by validation.
	messagingError struct {
		status      int
		code        string
		description string
	}
)

func (e *messagingError) Error() string { return e.description }

func messagingFailure(status int, code, description string) *messagingError {
	return &messagingError{status: status, code: code, description: description}
}

var (
	// errMessagingNotFound is the one answer for a missing id, a conversation
	// the caller is not in, an unknown or unapproved address, the caller's own
	// address, and an action refused because of a block. Nobody can tell which.
	errMessagingNotFound = messagingFailure(http.StatusNotFound, apiresp.ErrNoRecordFound, "not found")
)

// messagingRespond writes err when it is a decided messaging failure and
// otherwise passes it on to the error handler.
func messagingRespond(c *echo.Context, err error) error {
	if failure, ok := errors.AsType[*messagingError](err); ok {
		return c.JSON(failure.status, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     failure.code,
			Description: failure.description,
		})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return messagingRespond(c, errMessagingNotFound)
	}
	return err
}

func messagingInvalid(description string) *messagingError {
	return messagingFailure(http.StatusBadRequest, apiresp.ErrCodeValidationFailed, description)
}

func messagingConflict(description string) *messagingError {
	return messagingFailure(http.StatusConflict, apiresp.ErrCodeConflict, description)
}

func messagingResult(c *echo.Context, status int, result any) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(status, map[string]any{"ok": true, "result": result})
}

// messagingQuotaExceeded is the 429 for a spent daily request quota. The
// allowance returns at the next UTC midnight.
func messagingQuotaExceeded(c *echo.Context, now time.Time) error {
	now = now.UTC()
	midnight := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	seconds := max(int(math.Ceil(midnight.Sub(now).Seconds())), 1)
	c.Response().Header().Set("Retry-After", strconv.Itoa(seconds))
	return c.JSON(http.StatusTooManyRequests, apiresp.ErrResponse{
		Ok:          false,
		ErrCode:     apiresp.ErrCodeLimitExceeded,
		Description: "daily message request limit reached",
	})
}

// messageRateLimitMiddleware spends one of the caller's 30-per-minute send
// tokens. It runs after requireUserAuth or requireAdmin, so the key is always
// a user id.
func (a *API) messageRateLimitMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			userID, _, _ := getUserClaims(c)
			return a.throttleKey(c, a.messageLimiter, "user:"+strconv.FormatUint(userID, 10), next)
		}
	}
}

// messagingCaller is the session's user id and checksummed address.
func messagingCaller(c *echo.Context) (int64, string) {
	userID, address, _ := getUserClaims(c)
	return int64(userID), common.HexToAddress(address).Hex()
}

func checksum(address string) string {
	return common.HexToAddress(address).Hex()
}

func messagingIDParam(c *echo.Context) (int64, error) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, messagingInvalid("id must be a positive integer")
	}
	return id, nil
}

func messagingAddress(raw string, field string) (string, error) {
	raw = strings.TrimSpace(raw)
	if !common.IsHexAddress(raw) {
		return "", messagingInvalid(field + " must be a hex address")
	}
	return raw, nil
}

// optionalText trims a free-text field, treats empty as absent, and enforces
// a character limit.
func optionalMessagingText(raw *string, limit int, field string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*raw)
	if trimmed == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(trimmed) > limit {
		return nil, messagingInvalid(fmt.Sprintf("%s must be at most %d characters", field, limit))
	}
	return &trimmed, nil
}

func parseMessageInput(c *echo.Context) (messageInput, error) {
	var req struct {
		ClientID  string   `json:"clientId"`
		Body      *string  `json:"body"`
		ImageURLs []string `json:"imageUrls"`
	}
	if err := c.Bind(&req); err != nil {
		return messageInput{}, messagingFailure(http.StatusBadRequest, apiresp.ErrCodeInvalidJSON, "Invalid or malformed request")
	}
	clientID, err := uuid.Parse(strings.TrimSpace(req.ClientID))
	if err != nil {
		return messageInput{}, messagingInvalid("clientId must be a UUID")
	}
	input := messageInput{ClientID: clientID.String(), ImageURLs: []string{}}

	// The body is stored as written; whitespace alone counts as no text.
	if req.Body != nil && strings.TrimSpace(*req.Body) != "" {
		if utf8.RuneCountInString(*req.Body) > messageBodyMax {
			return messageInput{}, messagingInvalid(fmt.Sprintf("body must be at most %d characters", messageBodyMax))
		}
		input.Body = req.Body
	}
	if len(req.ImageURLs) > messageImagesMax {
		return messageInput{}, messagingInvalid(fmt.Sprintf("at most %d images", messageImagesMax))
	}
	for index, raw := range req.ImageURLs {
		imageURL, err := requiredHTTPURL(raw, fmt.Sprintf("imageUrls[%d]", index))
		if err != nil {
			return messageInput{}, messagingInvalid(err.Error())
		}
		input.ImageURLs = append(input.ImageURLs, imageURL)
	}
	if input.Body == nil && len(input.ImageURLs) == 0 {
		return messageInput{}, messagingInvalid("a message needs text or an image")
	}
	return input, nil
}

func parseMessagingLimit(raw string, fallback int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 {
		return 0, messagingInvalid("limit must be a positive integer")
	}
	return min(limit, messagingPageMax), nil
}

// parseMessagePage reads before/after/limit. Both cursors together are a 400.
func parseMessagePage(c *echo.Context) (store.MessagePage, error) {
	page := store.MessagePage{}
	parseID := func(name string) (int64, error) {
		raw := c.QueryParam(name)
		if raw == "" {
			return 0, nil
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return 0, messagingInvalid(name + " must be a positive message id")
		}
		return id, nil
	}
	var err error
	if page.Before, err = parseID("before"); err != nil {
		return page, err
	}
	if page.After, err = parseID("after"); err != nil {
		return page, err
	}
	if page.Before > 0 && page.After > 0 {
		return page, messagingInvalid("before and after cannot be combined")
	}
	page.Limit, err = parseMessagingLimit(c.QueryParam("limit"), messagesLimit)
	return page, err
}

// Cursors are opaque to clients: the last row's sort time and id.
func encodeMessagingCursor(at time.Time, id int64) string {
	return base64.RawURLEncoding.EncodeToString(fmt.Appendf(nil, "%d.%d", at.UnixMicro(), id))
}

func decodeMessagingCursor(raw string) (*store.Keyset, error) {
	if raw == "" {
		return nil, nil
	}
	invalid := messagingInvalid("invalid cursor")
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, invalid
	}
	micros, id, ok := strings.Cut(string(decoded), ".")
	if !ok {
		return nil, invalid
	}
	at, err := strconv.ParseInt(micros, 10, 64)
	if err != nil {
		return nil, invalid
	}
	keyID, err := strconv.ParseInt(id, 10, 64)
	if err != nil || keyID <= 0 {
		return nil, invalid
	}
	return &store.Keyset{At: time.UnixMicro(at).UTC(), ID: keyID}, nil
}

func nonNilImages(images []string) []string {
	if images == nil {
		return []string{}
	}
	return images
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	utc := t.UTC()
	return &utc
}

// userMessageSender names a message's sender relative to the caller.
func userMessageSender(kind string, sender, caller int64) string {
	switch {
	case sender == caller:
		return "self"
	case kind == store.ConversationHelpdesk:
		return "support"
	default:
		return "peer"
	}
}

func userMessageWire(kind string, caller int64, m store.Message) messagingMessage {
	return messagingMessage{
		ID:             m.ID,
		ConversationID: m.ConversationID,
		ClientID:       m.ClientID,
		Sender:         userMessageSender(kind, m.SenderUserID, caller),
		Body:           m.Body,
		ImageURLs:      nonNilImages(m.ImageURLs),
		CreatedAt:      m.CreatedAt.UTC(),
	}
}

func conversationWire(v store.ConversationView, caller int64) messagingConversation {
	direct := v.Kind == store.ConversationDirect
	byCaller := direct && v.ActorID != nil && *v.ActorID == caller
	out := messagingConversation{
		ID:                v.ID,
		Kind:              v.Kind,
		Status:            v.Status,
		Intro:             v.Intro,
		RequestedBySelf:   byCaller,
		CanAccept:         direct && v.Status == store.ConversationPending && !byCaller,
		CanRequest:        byCaller && (v.Status == store.ConversationDeclined || v.Status == store.ConversationRevoked),
		Blocked:           direct && v.Blocked,
		LastMessageAt:     utcPtr(v.LastMessageAt),
		LastReadMessageID: v.LastReadMessageID,
		UnreadCount:       v.UnreadCount,
		CreatedAt:         v.CreatedAt.UTC(),
		UpdatedAt:         v.UpdatedAt.UTC(),
	}
	if direct && v.PeerAddress != nil {
		out.Peer = &messagingPeer{Address: checksum(*v.PeerAddress), Username: v.PeerUsername, Avatar: v.PeerAvatar}
	}
	if v.LastMessageID != nil {
		last := userMessageWire(v.Kind, caller, store.Message{
			ID:             *v.LastMessageID,
			ConversationID: v.ID,
			SenderUserID:   derefInt64(v.LastMessageSenderID),
			ClientID:       derefString(v.LastMessageClientID),
			Body:           v.LastMessageBody,
			ImageURLs:      v.LastMessageImageURLs,
			CreatedAt:      derefTime(v.LastMessageCreatedAt),
		})
		out.LastMessage = &last
	}
	return out
}

func adminMessageWire(userID int64, m store.Message) adminMessagingMessage {
	out := adminMessagingMessage{
		ID:             m.ID,
		ConversationID: m.ConversationID,
		ClientID:       m.ClientID,
		Sender:         "user",
		Body:           m.Body,
		ImageURLs:      nonNilImages(m.ImageURLs),
		CreatedAt:      m.CreatedAt.UTC(),
	}
	if m.SenderUserID != userID {
		out.Sender = "support"
		admin := checksum(m.SenderAddress)
		out.AdminAddress = &admin
	}
	return out
}

func adminHelpdeskWire(row store.AdminHelpdeskRow) adminHelpdeskItem {
	out := adminHelpdeskItem{
		ID:            row.ID,
		Status:        row.Status,
		User:          messagingPeer{Address: checksum(row.UserAddress), Username: row.UserUsername, Avatar: row.UserAvatar},
		LastMessageAt: utcPtr(row.LastMessageAt),
		CreatedAt:     row.CreatedAt.UTC(),
		UpdatedAt:     row.UpdatedAt.UTC(),
	}
	if row.LastMessageID != nil {
		last := adminMessageWire(row.UserID, store.Message{
			ID:             *row.LastMessageID,
			ConversationID: row.ID,
			SenderUserID:   derefInt64(row.LastMessageSenderID),
			SenderAddress:  derefString(row.LastMessageSenderAddress),
			ClientID:       derefString(row.LastMessageClientID),
			Body:           row.LastMessageBody,
			ImageURLs:      row.LastMessageImageURLs,
			CreatedAt:      derefTime(row.LastMessageCreatedAt),
		})
		out.LastMessage = &last
		out.NeedsReply = row.Status == store.ConversationOpen && last.Sender == "user"
	}
	return out
}

func abuseReportWire(row store.AbuseReportRow) adminAbuseReport {
	out := adminAbuseReport{
		ID:         row.ID,
		Status:     row.Status,
		Reporter:   messagingPeer{Address: checksum(row.ReporterAddress), Username: row.ReporterUsername, Avatar: row.ReporterAvatar},
		Reported:   messagingPeer{Address: checksum(row.ReportedAddress), Username: row.ReportedUsername, Avatar: row.ReportedAvatar},
		Comment:    row.Comment,
		ResolvedAt: utcPtr(row.ResolvedAt),
		CreatedAt:  row.CreatedAt.UTC(),
	}
	if row.ResolvedByAddress != nil {
		resolvedBy := checksum(*row.ResolvedByAddress)
		out.ResolvedBy = &resolvedBy
	}
	if row.MessageID != nil {
		out.Message = &abuseReportMessage{
			ID:        *row.MessageID,
			Body:      row.MessageBody,
			ImageURLs: nonNilImages(row.MessageImageURLs),
			CreatedAt: derefTime(row.MessageCreatedAt).UTC(),
		}
	}
	return out
}

func derefInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func derefString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func derefTime(v *time.Time) time.Time {
	if v == nil {
		return time.Time{}
	}
	return *v
}

// newMessagingEvent starts an event with a fresh eventId, generated before the
// outbox insert so every publish attempt carries the same one.
func newMessagingEvent(kind string, conversationID int64, conversationKind string, actorID int64, actorAddress string, occurredAt time.Time) event.MessagingEvent {
	return event.MessagingEvent{
		EventID:          uuid.NewString(),
		Kind:             kind,
		OccurredAt:       occurredAt.UTC().Format(time.RFC3339),
		ConversationID:   conversationID,
		ConversationKind: conversationKind,
		Actor:            event.MessagingActor{UserID: actorID, Address: checksum(actorAddress)},
	}
}

func audienceMember(id int64, address string, push bool) event.MessagingAudience {
	return event.MessagingAudience{UserID: id, Address: checksum(address), Push: push}
}

// membersAudience lists every member of a conversation, pushing to pushTo (0
// pushes to nobody).
func membersAudience(members store.ConversationMembers, pushTo int64) []event.MessagingAudience {
	audience := []event.MessagingAudience{
		audienceMember(members.UserAID, members.UserAAddress, members.UserAID == pushTo),
	}
	if members.UserBID != nil && members.UserBAddress != nil {
		audience = append(audience, audienceMember(*members.UserBID, *members.UserBAddress, *members.UserBID == pushTo))
	}
	return audience
}

// otherMember is the member of a direct conversation who is not caller.
func otherMember(members store.ConversationMembers, caller int64) int64 {
	if members.UserAID == caller {
		return derefInt64(members.UserBID)
	}
	return members.UserAID
}

func (a *API) enqueueMessagingEvent(c *echo.Context, tx pgx.Tx, ev event.MessagingEvent) error {
	return worker.EnqueueMessagingEvent(c.Request().Context(), a.queueClient, tx, ev)
}
