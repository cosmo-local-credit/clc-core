package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// Messaging through the real router, store and an insert-only River client
// against CORE_TEST_DSN (skipped when unset):
//
//	CORE_TEST_DSN=postgres://... go test -p 1 ./internal/api/ -run Messaging
//
// Every fixture user is new to the test and named messagingTestMarker; cleanup
// removes their messaging rows, their outbox jobs and the users.

const (
	messagingTestMarker = "zq-messaging-test"
)

type (
	msgUser struct {
		ID      int64
		Address string
		Token   string
	}

	msgResponse struct {
		Code        int
		Ok          bool            `json:"ok"`
		ErrCode     string          `json:"code"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
		Raw         string
	}

	msgJob struct {
		Kind           string
		MaxAttempts    int
		EventID        string
		ConversationID int64
		MessageID      *int64
		Actor          int64
		Audience       map[int64]bool
	}

	msgFixture struct {
		t     *testing.T
		a     *API
		users []int64
	}
)

func newMessagingFixture(t *testing.T) *msgFixture {
	t.Helper()
	// The relay-operation harness is the one with an insert-only River client:
	// queued outbox jobs stay queued for the assertions.
	a := newRelayOperationAPI(t)
	f := &msgFixture{t: t, a: a}
	t.Cleanup(f.cleanup)
	return f
}

func (f *msgFixture) cleanup() {
	if len(f.users) == 0 {
		return
	}
	ctx := context.Background()
	pool := f.a.store.Pool()
	for _, stmt := range []string{
		`DELETE FROM river_job WHERE kind = 'MESSAGING_EVENT' AND (args->'actor'->>'userId')::INT = ANY($1)`,
		`DELETE FROM abuse_reports WHERE reporter_id = ANY($1) OR reported_user_id = ANY($1) OR resolved_by = ANY($1)`,
		`DELETE FROM conversations WHERE user_a_id = ANY($1) OR user_b_id = ANY($1)`,
		`DELETE FROM users WHERE id = ANY($1)`,
	} {
		if _, err := pool.Exec(ctx, stmt, f.users); err != nil {
			f.t.Errorf("messaging cleanup %q: %v", stmt, err)
		}
	}
}

func randomAddress(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 20)
	_, err := rand.Read(raw)
	require.NoError(t, err)
	return common.BytesToAddress(raw).Hex()
}

func (f *msgFixture) user(role, status string) msgUser {
	f.t.Helper()
	address := randomAddress(f.t)
	var id int64
	err := f.a.store.Pool().QueryRow(context.Background(), `
		INSERT INTO users (blockchain_address, name, email, role, status)
		VALUES ($1, $2, $3, $4::user_role, $5::user_status)
		RETURNING id`,
		address, messagingTestMarker, strings.ToLower(address)+"@example.invalid", role, status).Scan(&id)
	require.NoError(f.t, err)
	f.users = append(f.users, id)
	token, err := f.a.generateUserToken(store.User{ID: uint64(id), EthereumAddress: address, Role: role})
	require.NoError(f.t, err)
	return msgUser{ID: id, Address: address, Token: token}
}

func (f *msgFixture) approved() msgUser { return f.user("USER", "APPROVED") }

func (f *msgFixture) do(u msgUser, method, target string, body any) msgResponse {
	f.t.Helper()
	var payload string
	if body != nil {
		encoded, err := json.Marshal(body)
		require.NoError(f.t, err)
		payload = string(encoded)
	}
	req := httptest.NewRequest(method, target, strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: userAuthCookieName, Value: u.Token})
	res := httptest.NewRecorder()
	f.a.router.ServeHTTP(res, req)
	out := msgResponse{Code: res.Code, Raw: res.Body.String()}
	if res.Body.Len() > 0 {
		require.NoError(f.t, json.Unmarshal(res.Body.Bytes(), &out), res.Body.String())
	}
	return out
}

func (r msgResponse) decode(t *testing.T, into any) {
	t.Helper()
	require.NoError(t, json.Unmarshal(r.Result, into), r.Raw)
}

func (f *msgFixture) request(from, to msgUser) messagingConversation {
	f.t.Helper()
	res := f.do(from, http.MethodPost, "/api/v2/conversations", map[string]any{"recipientAddress": to.Address})
	require.Equal(f.t, http.StatusCreated, res.Code, res.Raw)
	var conv messagingConversation
	res.decode(f.t, &conv)
	return conv
}

func (f *msgFixture) setStatus(u msgUser, id int64, status string) msgResponse {
	f.t.Helper()
	return f.do(u, http.MethodPut, fmt.Sprintf("/api/v2/conversations/%d/status", id), map[string]string{"status": status})
}

func (f *msgFixture) connect(a, b msgUser) int64 {
	f.t.Helper()
	conv := f.request(a, b)
	res := f.setStatus(b, conv.ID, "accepted")
	require.Equal(f.t, http.StatusOK, res.Code, res.Raw)
	return conv.ID
}

func (f *msgFixture) send(u msgUser, conversation int64, clientID, body string) msgResponse {
	f.t.Helper()
	return f.do(u, http.MethodPost, fmt.Sprintf("/api/v2/conversations/%d/messages", conversation),
		map[string]any{"clientId": clientID, "body": body})
}

func (f *msgFixture) sent(u msgUser, conversation int64, body string) messagingMessage {
	f.t.Helper()
	res := f.send(u, conversation, uuid.NewString(), body)
	require.Equal(f.t, http.StatusCreated, res.Code, res.Raw)
	var message messagingMessage
	res.decode(f.t, &message)
	return message
}

func (f *msgFixture) unread(u msgUser) map[string]int {
	f.t.Helper()
	res := f.do(u, http.MethodGet, "/api/v2/conversations/unread", nil)
	require.Equal(f.t, http.StatusOK, res.Code, res.Raw)
	var counts map[string]int
	res.decode(f.t, &counts)
	return counts
}

// jobs returns the outbox jobs queued for a conversation, oldest first.
func (f *msgFixture) jobs(conversation int64) []msgJob {
	f.t.Helper()
	rows, err := f.a.store.Pool().Query(context.Background(), `
		SELECT max_attempts, args FROM river_job
		WHERE kind = 'MESSAGING_EVENT' AND (args->>'conversationId')::BIGINT = $1
		  AND (args->'actor'->>'userId')::INT = ANY($2)
		ORDER BY id`, conversation, f.users)
	require.NoError(f.t, err)
	defer rows.Close()
	var out []msgJob
	for rows.Next() {
		var (
			attempts int
			raw      []byte
			args     struct {
				EventID        string `json:"eventId"`
				Kind           string `json:"kind"`
				ConversationID int64  `json:"conversationId"`
				MessageID      *int64 `json:"messageId"`
				Actor          struct {
					UserID  int64  `json:"userId"`
					Address string `json:"address"`
				} `json:"actor"`
				Audience []struct {
					UserID  int64  `json:"userId"`
					Address string `json:"address"`
					Push    bool   `json:"push"`
				} `json:"audience"`
			}
		)
		require.NoError(f.t, rows.Scan(&attempts, &raw))
		require.NoError(f.t, json.Unmarshal(raw, &args))
		job := msgJob{
			Kind: args.Kind, MaxAttempts: attempts, EventID: args.EventID,
			ConversationID: args.ConversationID, MessageID: args.MessageID,
			Actor: args.Actor.UserID, Audience: map[int64]bool{},
		}
		for _, member := range args.Audience {
			require.Equal(f.t, common.HexToAddress(member.Address).Hex(), member.Address, "audience address is checksummed")
			job.Audience[member.UserID] = member.Push
		}
		out = append(out, job)
	}
	require.NoError(f.t, rows.Err())
	return out
}

func jobKinds(jobs []msgJob) []string {
	kinds := make([]string, 0, len(jobs))
	for _, job := range jobs {
		kinds = append(kinds, job.Kind)
	}
	return kinds
}

func TestMessagingRequestAcceptSendReadDB(t *testing.T) {
	f := newMessagingFixture(t)
	alice, bob := f.approved(), f.approved()

	// Alice asks with an intro; the address is matched case-insensitively.
	res := f.do(alice, http.MethodPost, "/api/v2/conversations", map[string]any{
		"recipientAddress": strings.ToLower(bob.Address), "intro": "  hi from the market  ",
	})
	require.Equal(t, http.StatusCreated, res.Code, res.Raw)
	var conv messagingConversation
	res.decode(t, &conv)
	require.Equal(t, "direct", conv.Kind)
	require.Equal(t, "pending", conv.Status)
	require.True(t, conv.RequestedBySelf)
	require.False(t, conv.CanAccept)
	require.Equal(t, bob.Address, conv.Peer.Address)
	require.Equal(t, "hi from the market", *conv.Intro)
	require.NotContains(t, res.Raw, `"userId"`, "user ids never leave the server")

	jobs := f.jobs(conv.ID)
	require.Len(t, jobs, 1)
	require.Equal(t, "conversation.requested", jobs[0].Kind)
	require.Equal(t, 10, jobs[0].MaxAttempts)
	require.NoError(t, uuid.Validate(jobs[0].EventID))
	require.Equal(t, map[int64]bool{bob.ID: true, alice.ID: false}, jobs[0].Audience)

	// Bob sees the request as acceptable and counts it.
	res = f.do(bob, http.MethodGet, fmt.Sprintf("/api/v2/conversations/%d", conv.ID), nil)
	require.Equal(t, http.StatusOK, res.Code, res.Raw)
	var seen messagingConversation
	res.decode(t, &seen)
	require.True(t, seen.CanAccept)
	require.False(t, seen.RequestedBySelf)
	require.Equal(t, alice.Address, seen.Peer.Address)
	require.Equal(t, map[string]int{"direct": 0, "requests": 1, "helpdesk": 0}, f.unread(bob))

	// A pending conversation takes no messages, and a requester cannot accept.
	require.Equal(t, http.StatusConflict, f.send(alice, conv.ID, uuid.NewString(), "early").Code)
	require.Equal(t, http.StatusConflict, f.setStatus(alice, conv.ID, "accepted").Code)

	res = f.setStatus(bob, conv.ID, "accepted")
	require.Equal(t, http.StatusOK, res.Code, res.Raw)
	res.decode(t, &seen)
	require.Equal(t, "accepted", seen.Status)
	jobs = f.jobs(conv.ID)
	require.Equal(t, "conversation.accepted", jobs[len(jobs)-1].Kind)
	require.Equal(t, map[int64]bool{alice.ID: true, bob.ID: false}, jobs[len(jobs)-1].Audience, "the requester is pushed")

	// Send, then retry the same clientId: the stored message, no second event.
	clientID := uuid.NewString()
	res = f.send(alice, conv.ID, clientID, "hello bob")
	require.Equal(t, http.StatusCreated, res.Code, res.Raw)
	var first messagingMessage
	res.decode(t, &first)
	require.Equal(t, "self", first.Sender)
	require.Equal(t, []string{}, first.ImageURLs)
	before := len(f.jobs(conv.ID))
	require.Equal(t, "message.created", f.jobs(conv.ID)[before-1].Kind)
	require.Equal(t, first.ID, *f.jobs(conv.ID)[before-1].MessageID)
	require.Equal(t, map[int64]bool{bob.ID: true, alice.ID: false}, f.jobs(conv.ID)[before-1].Audience)

	res = f.send(alice, conv.ID, strings.ToUpper(clientID), "hello bob, again")
	require.Equal(t, http.StatusOK, res.Code, res.Raw)
	var retried messagingMessage
	res.decode(t, &retried)
	require.Equal(t, first.ID, retried.ID)
	require.Equal(t, "hello bob", *retried.Body)
	require.Len(t, f.jobs(conv.ID), before, "a retried send enqueues nothing")

	// The same clientId cannot name a message in another conversation.
	carol := f.approved()
	other := f.connect(alice, carol)
	require.Equal(t, http.StatusConflict, f.send(alice, other, clientID, "elsewhere").Code)

	reply := f.sent(bob, conv.ID, "hi alice")
	third := f.sent(bob, conv.ID, "are you there?")

	// Bob's view names Alice's message as the peer's.
	res = f.do(bob, http.MethodGet, fmt.Sprintf("/api/v2/conversations/%d/messages", conv.ID), nil)
	require.Equal(t, http.StatusOK, res.Code, res.Raw)
	var page struct {
		Items   []messagingMessage `json:"items"`
		HasMore bool               `json:"hasMore"`
	}
	res.decode(t, &page)
	require.Len(t, page.Items, 3)
	require.Equal(t, []string{"peer", "self", "self"}, []string{page.Items[0].Sender, page.Items[1].Sender, page.Items[2].Sender})
	require.False(t, page.HasMore)

	// Unread for Alice: Bob's two messages; her own never count.
	require.Equal(t, map[string]int{"direct": 2, "requests": 0, "helpdesk": 0}, f.unread(alice))

	read := func(id int64) msgResponse {
		return f.do(alice, http.MethodPut, fmt.Sprintf("/api/v2/conversations/%d/read", conv.ID),
			map[string]int64{"lastReadMessageId": id})
	}
	require.Equal(t, http.StatusNoContent, read(reply.ID).Code)
	require.Equal(t, 1, f.unread(alice)["direct"])
	require.Equal(t, "conversation.read", f.jobs(conv.ID)[len(f.jobs(conv.ID))-1].Kind)
	require.Equal(t, map[int64]bool{alice.ID: false}, f.jobs(conv.ID)[len(f.jobs(conv.ID))-1].Audience)

	require.Equal(t, http.StatusNoContent, read(third.ID).Code)
	require.Equal(t, 0, f.unread(alice)["direct"])
	afterRead := len(f.jobs(conv.ID))

	// Forward only: an older id leaves the marker and sends nothing.
	require.Equal(t, http.StatusNoContent, read(reply.ID).Code)
	require.Len(t, f.jobs(conv.ID), afterRead)
	res = f.do(alice, http.MethodGet, fmt.Sprintf("/api/v2/conversations/%d", conv.ID), nil)
	res.decode(t, &seen)
	require.Equal(t, third.ID, *seen.LastReadMessageID)
	require.Equal(t, 0, seen.UnreadCount)
	require.Equal(t, third.ID, seen.LastMessage.ID)
	require.Equal(t, "peer", seen.LastMessage.Sender)

	// A message of another conversation, or one that does not exist, is a 404.
	foreign := f.sent(carol, other, "from carol")
	require.Equal(t, http.StatusNotFound, read(foreign.ID).Code)
	require.Equal(t, http.StatusNotFound, read(third.ID+1_000_000).Code)
	require.Equal(t, http.StatusBadRequest, read(0).Code)
}

func TestMessagingAuthorizationDB(t *testing.T) {
	f := newMessagingFixture(t)
	alice, bob, mallory := f.approved(), f.approved(), f.approved()
	admin := f.user("ADMIN", "APPROVED")
	conv := f.connect(alice, bob)
	message := f.sent(alice, conv, "private")

	missing := f.do(mallory, http.MethodGet, "/api/v2/conversations/999999999", nil)
	require.Equal(t, http.StatusNotFound, missing.Code)

	// A non-member, and an admin, get exactly the answer a missing id gets.
	for _, outsider := range []msgUser{mallory, admin} {
		for _, call := range []struct {
			method, path string
			body         any
		}{
			{http.MethodGet, fmt.Sprintf("/api/v2/conversations/%d", conv), nil},
			{http.MethodGet, fmt.Sprintf("/api/v2/conversations/%d/messages", conv), nil},
			{http.MethodPut, fmt.Sprintf("/api/v2/conversations/%d/status", conv), map[string]string{"status": "revoked"}},
			{http.MethodPost, fmt.Sprintf("/api/v2/conversations/%d/messages", conv), map[string]string{"clientId": uuid.NewString(), "body": "x"}},
			{http.MethodPut, fmt.Sprintf("/api/v2/conversations/%d/read", conv), map[string]int64{"lastReadMessageId": message.ID}},
		} {
			res := f.do(outsider, call.method, call.path, call.body)
			require.Equal(t, http.StatusNotFound, res.Code, "%s %s: %s", call.method, call.path, res.Raw)
			require.Equal(t, missing.Raw, res.Raw)
		}
	}

	// Admin routes read helpdesk rows only.
	require.Equal(t, http.StatusNotFound, f.do(admin, http.MethodGet, fmt.Sprintf("/api/v2/admin/helpdesk/%d", conv), nil).Code)
	require.Equal(t, http.StatusNotFound, f.do(admin, http.MethodGet, fmt.Sprintf("/api/v2/admin/helpdesk/%d/messages", conv), nil).Code)
	require.Equal(t, http.StatusNotFound, f.do(admin, http.MethodPost, fmt.Sprintf("/api/v2/admin/helpdesk/%d/messages", conv),
		map[string]string{"clientId": uuid.NewString(), "body": "x"}).Code)
	require.Equal(t, http.StatusNotFound, f.do(admin, http.MethodPut, fmt.Sprintf("/api/v2/admin/helpdesk/%d/status", conv),
		map[string]string{"status": "resolved"}).Code)
	require.Equal(t, http.StatusForbidden, f.do(alice, http.MethodGet, "/api/v2/admin/helpdesk", nil).Code)
	require.Equal(t, http.StatusForbidden, f.do(alice, http.MethodGet, "/api/v2/admin/abuse-reports", nil).Code)

	// Neither sees the other's list.
	res := f.do(mallory, http.MethodGet, "/api/v2/conversations", nil)
	require.Equal(t, http.StatusOK, res.Code, res.Raw)
	require.Contains(t, res.Raw, `"items":[]`)
}

func TestMessagingRequestTargetsAndRerequestDB(t *testing.T) {
	f := newMessagingFixture(t)
	alice, bob := f.approved(), f.approved()
	pending := f.user("USER", "PENDING")

	unknown := f.do(alice, http.MethodPost, "/api/v2/conversations", map[string]any{"recipientAddress": randomAddress(t)})
	require.Equal(t, http.StatusNotFound, unknown.Code)
	for _, target := range []string{pending.Address, alice.Address} {
		res := f.do(alice, http.MethodPost, "/api/v2/conversations", map[string]any{"recipientAddress": target})
		require.Equal(t, http.StatusNotFound, res.Code, res.Raw)
		require.Equal(t, unknown.Raw, res.Raw)
	}
	require.Equal(t, http.StatusBadRequest, f.do(alice, http.MethodPost, "/api/v2/conversations",
		map[string]any{"recipientAddress": "not-an-address"}).Code)
	require.Equal(t, http.StatusBadRequest, f.do(alice, http.MethodPost, "/api/v2/conversations",
		map[string]any{"recipientAddress": bob.Address, "intro": strings.Repeat("é", introMax+1)}).Code)

	conv := f.request(alice, bob)
	// Asking again, from either side, while it is pending.
	require.Equal(t, http.StatusConflict, f.do(alice, http.MethodPost, "/api/v2/conversations", map[string]any{"recipientAddress": bob.Address}).Code)
	require.Equal(t, http.StatusConflict, f.do(bob, http.MethodPost, "/api/v2/conversations", map[string]any{"recipientAddress": alice.Address}).Code)
	require.Equal(t, http.StatusConflict, f.setStatus(bob, conv.ID, "revoked").Code, "revoke needs accepted")

	res := f.setStatus(bob, conv.ID, "declined")
	require.Equal(t, http.StatusOK, res.Code, res.Raw)
	var declined messagingConversation
	res.decode(t, &declined)
	require.Equal(t, "declined", declined.Status)
	require.True(t, declined.CanRequest)
	jobs := f.jobs(conv.ID)
	require.Equal(t, "conversation.declined", jobs[len(jobs)-1].Kind)
	require.Equal(t, map[int64]bool{alice.ID: false, bob.ID: false}, jobs[len(jobs)-1].Audience)

	// Only the decliner can reopen it.
	require.Equal(t, http.StatusConflict, f.do(alice, http.MethodPost, "/api/v2/conversations", map[string]any{"recipientAddress": bob.Address}).Code)
	reopened := f.request(bob, alice)
	require.Equal(t, conv.ID, reopened.ID)
	require.Equal(t, "pending", reopened.Status)
	require.True(t, reopened.RequestedBySelf)

	require.Equal(t, http.StatusOK, f.setStatus(alice, conv.ID, "accepted").Code)
	res = f.setStatus(alice, conv.ID, "revoked")
	require.Equal(t, http.StatusOK, res.Code, res.Raw)
	jobs = f.jobs(conv.ID)
	require.Equal(t, "conversation.revoked", jobs[len(jobs)-1].Kind)
	require.Equal(t, map[int64]bool{alice.ID: false, bob.ID: false}, jobs[len(jobs)-1].Audience)
	require.Equal(t, http.StatusConflict, f.send(bob, conv.ID, uuid.NewString(), "after revoke").Code)
	require.Equal(t, http.StatusOK, f.do(bob, http.MethodGet, fmt.Sprintf("/api/v2/conversations/%d/messages", conv.ID), nil).Code,
		"history stays readable after a revoke")
	require.Equal(t, http.StatusConflict, f.do(bob, http.MethodPost, "/api/v2/conversations", map[string]any{"recipientAddress": alice.Address}).Code)
	require.Equal(t, http.StatusCreated, f.do(alice, http.MethodPost, "/api/v2/conversations", map[string]any{"recipientAddress": bob.Address}).Code)
}

func TestMessagingConcurrentTransitionsDB(t *testing.T) {
	f := newMessagingFixture(t)
	alice, bob := f.approved(), f.approved()

	for round := range 5 {
		carol := f.approved()
		conv := f.request(carol, bob)

		// Two of Bob's devices answer at once: exactly one decision lands.
		var wg sync.WaitGroup
		codes := make([]int, 2)
		for i, status := range []string{"accepted", "declined"} {
			wg.Go(func() { codes[i] = f.setStatus(bob, conv.ID, status).Code })
		}
		wg.Wait()
		require.ElementsMatch(t, []int{http.StatusOK, http.StatusConflict}, codes, "round %d", round)
		decisions := 0
		for _, job := range f.jobs(conv.ID) {
			if job.Kind == "conversation.accepted" || job.Kind == "conversation.declined" {
				decisions++
			}
		}
		require.Equal(t, 1, decisions, "round %d", round)
	}

	// Both members revoke from two devices each at once: one revoke applies,
	// the rest find the conversation already revoked.
	conv := f.connect(alice, bob)
	var wg sync.WaitGroup
	codes := make([]int, 4)
	for i := range codes {
		who := alice
		if i%2 == 1 {
			who = bob
		}
		wg.Go(func() { codes[i] = f.setStatus(who, conv, "revoked").Code })
	}
	wg.Wait()
	ok := 0
	for _, code := range codes {
		if code == http.StatusOK {
			ok++
		} else {
			require.Equal(t, http.StatusConflict, code)
		}
	}
	require.Equal(t, 1, ok)
	require.Equal(t, []string{"conversation.requested", "conversation.accepted", "conversation.revoked"}, jobKinds(f.jobs(conv)))
}

func TestMessagingBlocksDB(t *testing.T) {
	f := newMessagingFixture(t)
	alice, bob, dave := f.approved(), f.approved(), f.approved()
	conv := f.connect(alice, bob)
	f.sent(alice, conv, "before the block")
	unknown := f.do(alice, http.MethodPost, "/api/v2/conversations", map[string]any{"recipientAddress": randomAddress(t)})

	require.Equal(t, 1, f.unread(bob)["direct"])

	res := f.do(bob, http.MethodPut, "/api/v2/blocks/"+alice.Address, nil)
	require.Equal(t, http.StatusNoContent, res.Code, res.Raw)
	require.Equal(t, 0, f.unread(bob)["direct"], "a blocked peer's messages leave the count")
	jobs := f.jobs(conv)
	require.Equal(t, "block.changed", jobs[len(jobs)-1].Kind)
	require.Equal(t, map[int64]bool{bob.ID: false}, jobs[len(jobs)-1].Audience, "the blocked person hears nothing")
	before := len(jobs)
	require.Equal(t, http.StatusNoContent, f.do(bob, http.MethodPut, "/api/v2/blocks/"+alice.Address, nil).Code)
	require.Len(t, f.jobs(conv), before, "a repeated block changes nothing")

	// Sends fail both ways with the generic not-found; the status is untouched.
	for _, sender := range []msgUser{alice, bob} {
		res := f.send(sender, conv, uuid.NewString(), "blocked")
		require.Equal(t, http.StatusNotFound, res.Code, res.Raw)
		require.Equal(t, unknown.Raw, res.Raw)
	}
	var view messagingConversation
	f.do(alice, http.MethodGet, fmt.Sprintf("/api/v2/conversations/%d", conv), nil).decode(t, &view)
	require.Equal(t, "accepted", view.Status)
	require.False(t, view.Blocked, "Alice cannot see that Bob blocked her")
	f.do(bob, http.MethodGet, fmt.Sprintf("/api/v2/conversations/%d", conv), nil).decode(t, &view)
	require.True(t, view.Blocked)
	require.Equal(t, http.StatusOK, f.do(alice, http.MethodGet, fmt.Sprintf("/api/v2/conversations/%d/messages", conv), nil).Code)

	// A request to or from a blocked person is the same 404 as an unknown one.
	res = f.do(bob, http.MethodPut, "/api/v2/blocks/"+dave.Address, nil)
	require.Equal(t, http.StatusNoContent, res.Code)
	for _, pair := range [][2]msgUser{{dave, bob}, {bob, dave}} {
		res := f.do(pair[0], http.MethodPost, "/api/v2/conversations", map[string]any{"recipientAddress": pair[1].Address})
		require.Equal(t, http.StatusNotFound, res.Code)
		require.Equal(t, unknown.Raw, res.Raw)
	}
	require.Len(t, f.jobs(0), 1, "a block with no conversation is published with conversationId 0")

	// A pending request from someone Bob then blocks is hidden from his list and counts.
	erin := f.approved()
	erinConv := f.request(erin, bob)
	require.Equal(t, 1, f.unread(bob)["requests"])
	require.Equal(t, http.StatusNoContent, f.do(bob, http.MethodPut, "/api/v2/blocks/"+erin.Address, nil).Code)
	require.Equal(t, 0, f.unread(bob)["requests"])
	listIDs := func() []int64 {
		res := f.do(bob, http.MethodGet, "/api/v2/conversations?kind=direct", nil)
		require.Equal(t, http.StatusOK, res.Code, res.Raw)
		var page struct {
			Items []messagingConversation `json:"items"`
		}
		res.decode(t, &page)
		ids := []int64{}
		for _, item := range page.Items {
			ids = append(ids, item.ID)
		}
		return ids
	}
	require.NotContains(t, listIDs(), erinConv.ID)
	require.Contains(t, listIDs(), conv, "an accepted conversation stays listed")

	res = f.do(bob, http.MethodGet, "/api/v2/blocks", nil)
	require.Equal(t, http.StatusOK, res.Code)
	var blocks struct {
		Items []struct {
			Address string `json:"address"`
		} `json:"items"`
	}
	res.decode(t, &blocks)
	require.Len(t, blocks.Items, 3)
	require.Equal(t, erin.Address, blocks.Items[0].Address)

	// Unblocking restores the conversation as it was.
	require.Equal(t, http.StatusNoContent, f.do(bob, http.MethodDelete, "/api/v2/blocks/"+erin.Address, nil).Code)
	require.Contains(t, listIDs(), erinConv.ID)
	require.Equal(t, http.StatusNoContent, f.do(bob, http.MethodDelete, "/api/v2/blocks/"+alice.Address, nil).Code)
	f.sent(alice, conv, "after the unblock")
	require.Equal(t, 2, f.unread(bob)["direct"])
	require.Equal(t, http.StatusNotFound, f.do(bob, http.MethodPut, "/api/v2/blocks/"+bob.Address, nil).Code)
	require.Equal(t, http.StatusNotFound, f.do(bob, http.MethodPut, "/api/v2/blocks/"+randomAddress(t), nil).Code)
}

// waitForLockWaiters polls until n backends of this database wait on a lock,
// or reports false if that never happens.
func (f *msgFixture) waitForLockWaiters(n int) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		if err := f.a.store.Pool().QueryRow(context.Background(), `
			SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&waiting); err != nil {
			f.t.Errorf("count lock waiters: %v", err)
			return false
		}
		if waiting >= n {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// A request racing a block must not get through once the block commits, with
// or without a conversation row to lock. The schedule is forced: a held
// user_blocks insert parks PUT /blocks after it has taken its locks, the
// request starts behind it, and only then is the block let through. The block
// was first, so the request must see it.
func TestMessagingRequestRacingBlockDB(t *testing.T) {
	for _, tc := range []struct {
		name            string
		existing        bool // the pair has a declined row the requester may reopen
		requesterBlocks bool
	}{
		{"rerequest/recipient blocks", true, false},
		{"rerequest/requester blocks", true, true},
		{"first request/recipient blocks", false, false},
		{"first request/requester blocks", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMessagingFixture(t)
			requester, recipient := f.approved(), f.approved()
			ctx := context.Background()

			if tc.existing {
				// The recipient asked and the requester declined, so only the
				// requester may reopen it.
				conv := f.request(recipient, requester)
				require.Equal(t, http.StatusOK, f.setStatus(requester, conv.ID, "declined").Code)
			}
			blocker, blocked := recipient, requester
			if tc.requesterBlocks {
				blocker, blocked = requester, recipient
			}

			hold, err := f.a.store.Pool().Begin(ctx)
			require.NoError(t, err)
			defer hold.Rollback(ctx)
			_, err = hold.Exec(ctx, `INSERT INTO user_blocks (blocker_id, blocked_id) VALUES ($1, $2)`, blocker.ID, blocked.ID)
			require.NoError(t, err)

			var wg sync.WaitGroup
			var blockRes, requestRes msgResponse
			wg.Go(func() { blockRes = f.do(blocker, http.MethodPut, "/api/v2/blocks/"+blocked.Address, nil) })
			require.True(t, f.waitForLockWaiters(1), "the block parks on the held insert")
			requestDone := make(chan struct{})
			wg.Go(func() {
				defer close(requestDone)
				requestRes = f.do(requester, http.MethodPost, "/api/v2/conversations", map[string]any{"recipientAddress": recipient.Address})
			})
			// The request either finishes on its own or waits behind the block.
			parked := make(chan bool, 1)
			go func() { parked <- f.waitForLockWaiters(2) }()
			select {
			case <-requestDone:
			case <-parked:
			}
			require.NoError(t, hold.Rollback(ctx))
			wg.Wait()

			require.Equal(t, http.StatusNoContent, blockRes.Code, blockRes.Raw)
			unknown := f.do(requester, http.MethodPost, "/api/v2/conversations", map[string]any{"recipientAddress": randomAddress(t)})
			require.Equal(t, http.StatusNotFound, requestRes.Code, "a request behind a committed block: %s", requestRes.Raw)
			require.Equal(t, unknown.Raw, requestRes.Raw, "the refusal is the generic not-found")

			var requested int
			require.NoError(t, f.a.store.Pool().QueryRow(ctx, `
				SELECT count(*) FROM river_job
				WHERE kind = 'MESSAGING_EVENT' AND args->>'kind' = 'conversation.requested'
				  AND (args->'actor'->>'userId')::INT = $1`, requester.ID).Scan(&requested))
			require.Zero(t, requested, "a refused request enqueues nothing")
			var status string
			err = f.a.store.Pool().QueryRow(ctx, `
				SELECT status FROM conversations
				WHERE kind = 'direct' AND user_a_id = $1 AND user_b_id = $2`,
				min(requester.ID, recipient.ID), max(requester.ID, recipient.ID)).Scan(&status)
			if tc.existing {
				require.NoError(t, err)
				require.Equal(t, "declined", status)
			} else {
				require.ErrorIs(t, err, pgx.ErrNoRows)
			}
		})
	}
}

func TestMessagingDailyRequestQuotaDB(t *testing.T) {
	f := newMessagingFixture(t)
	alice, bob, carol := f.approved(), f.approved(), f.approved()
	ctx := context.Background()
	used := func() int {
		var n int
		err := f.a.store.Pool().QueryRow(ctx, `SELECT COALESCE((SELECT used FROM messaging_request_quota
			WHERE user_id = $1 AND day = (CURRENT_TIMESTAMP AT TIME ZONE 'UTC')::date), 0)`, alice.ID).Scan(&n)
		require.NoError(t, err)
		return n
	}

	// A failed request rolls its increment back.
	require.Equal(t, http.StatusNotFound, f.do(alice, http.MethodPost, "/api/v2/conversations", map[string]any{"recipientAddress": randomAddress(t)}).Code)
	require.Equal(t, 0, used())
	pending := f.request(alice, bob)
	require.Equal(t, 1, used())
	require.Equal(t, http.StatusConflict, f.do(alice, http.MethodPost, "/api/v2/conversations", map[string]any{"recipientAddress": bob.Address}).Code)
	require.Equal(t, 1, used())

	_, err := f.a.store.Pool().Exec(ctx, `UPDATE messaging_request_quota SET used = $2 WHERE user_id = $1`, alice.ID, messagingDailyRequestQuota-1)
	require.NoError(t, err)
	f.request(alice, carol)
	require.Equal(t, messagingDailyRequestQuota, used())

	dave := f.approved()
	res := f.do(alice, http.MethodPost, "/api/v2/conversations", map[string]any{"recipientAddress": dave.Address})
	require.Equal(t, http.StatusTooManyRequests, res.Code, res.Raw)
	require.Equal(t, "E11", res.ErrCode)
	require.Equal(t, messagingDailyRequestQuota, used())

	// Answering and sending do not spend the request quota.
	require.Equal(t, http.StatusOK, f.setStatus(bob, pending.ID, "accepted").Code)
	f.sent(alice, pending.ID, "still allowed")
	require.Equal(t, messagingDailyRequestQuota, used())
}

func TestMessagingHelpdeskDB(t *testing.T) {
	f := newMessagingFixture(t)
	user := f.approved()
	admin := f.user("ADMIN", "APPROVED")
	super := f.user("SUPERADMIN", "APPROVED")

	res := f.do(user, http.MethodPost, "/api/v2/helpdesk", map[string]any{})
	require.Equal(t, http.StatusCreated, res.Code, res.Raw)
	var desk messagingConversation
	res.decode(t, &desk)
	require.Equal(t, "helpdesk", desk.Kind)
	require.Equal(t, "open", desk.Status)
	require.Nil(t, desk.Peer)
	require.Contains(t, res.Raw, `"peer":null`)
	res = f.do(user, http.MethodPost, "/api/v2/helpdesk", map[string]any{})
	require.Equal(t, http.StatusOK, res.Code)
	var again messagingConversation
	res.decode(t, &again)
	require.Equal(t, desk.ID, again.ID)

	question := f.sent(user, desk.ID, "how do I cash out?")
	require.Equal(t, "self", question.Sender)
	jobs := f.jobs(desk.ID)
	require.Equal(t, "message.created", jobs[len(jobs)-1].Kind)
	require.Equal(t, map[int64]bool{user.ID: false}, jobs[len(jobs)-1].Audience, "no admin push in v1")

	// Any admin sees it in the queue as needing a reply.
	findItem := func(caller msgUser, query string) *adminHelpdeskItem {
		res := f.do(caller, http.MethodGet, "/api/v2/admin/helpdesk"+query, nil)
		require.Equal(t, http.StatusOK, res.Code, res.Raw)
		var page struct {
			Items []adminHelpdeskItem `json:"items"`
		}
		res.decode(t, &page)
		for _, item := range page.Items {
			if item.ID == desk.ID {
				return &item
			}
		}
		return nil
	}
	item := findItem(admin, "?status=open&limit=100")
	require.NotNil(t, item)
	require.True(t, item.NeedsReply)
	require.Equal(t, user.Address, item.User.Address)
	require.Equal(t, "user", item.LastMessage.Sender)
	require.Nil(t, findItem(admin, "?status=resolved&limit=100"))

	res = f.do(super, http.MethodPost, fmt.Sprintf("/api/v2/admin/helpdesk/%d/messages", desk.ID),
		map[string]any{"clientId": uuid.NewString(), "body": "Open Wallet, then Cash.", "imageUrls": []string{"https://storage.example/a.webp"}})
	require.Equal(t, http.StatusCreated, res.Code, res.Raw)
	var answer adminMessagingMessage
	res.decode(t, &answer)
	require.Equal(t, "support", answer.Sender)
	require.Equal(t, super.Address, *answer.AdminAddress)
	jobs = f.jobs(desk.ID)
	require.Equal(t, "message.created", jobs[len(jobs)-1].Kind)
	require.Equal(t, super.ID, jobs[len(jobs)-1].Actor)
	require.Equal(t, map[int64]bool{user.ID: true}, jobs[len(jobs)-1].Audience)

	require.False(t, findItem(admin, "?limit=100").NeedsReply)
	require.Equal(t, 1, f.unread(user)["helpdesk"])
	res = f.do(user, http.MethodGet, fmt.Sprintf("/api/v2/conversations/%d/messages", desk.ID), nil)
	var page struct {
		Items []messagingMessage `json:"items"`
	}
	res.decode(t, &page)
	require.Equal(t, []string{"self", "support"}, []string{page.Items[0].Sender, page.Items[1].Sender})
	require.NotContains(t, res.Raw, super.Address, "the user never learns which admin replied")
	require.Equal(t, http.StatusConflict, f.setStatus(user, desk.ID, "accepted").Code)

	res = f.do(admin, http.MethodGet, fmt.Sprintf("/api/v2/admin/helpdesk/%d/messages?limit=1", desk.ID), nil)
	require.Equal(t, http.StatusOK, res.Code, res.Raw)
	var adminPage struct {
		Items   []adminMessagingMessage `json:"items"`
		HasMore bool                    `json:"hasMore"`
	}
	res.decode(t, &adminPage)
	require.Len(t, adminPage.Items, 1)
	require.Equal(t, answer.ID, adminPage.Items[0].ID)
	require.True(t, adminPage.HasMore)

	// Resolve; the same state twice changes nothing.
	res = f.do(admin, http.MethodPut, fmt.Sprintf("/api/v2/admin/helpdesk/%d/status", desk.ID), map[string]string{"status": "resolved"})
	require.Equal(t, http.StatusOK, res.Code, res.Raw)
	var resolved adminHelpdeskItem
	res.decode(t, &resolved)
	require.Equal(t, "resolved", resolved.Status)
	jobs = f.jobs(desk.ID)
	require.Equal(t, "helpdesk.status_changed", jobs[len(jobs)-1].Kind)
	require.Equal(t, admin.ID, jobs[len(jobs)-1].Actor)
	require.Equal(t, map[int64]bool{user.ID: false}, jobs[len(jobs)-1].Audience)
	count := len(jobs)
	require.Equal(t, http.StatusOK, f.do(admin, http.MethodPut, fmt.Sprintf("/api/v2/admin/helpdesk/%d/status", desk.ID), map[string]string{"status": "resolved"}).Code)
	require.Len(t, f.jobs(desk.ID), count)

	// A user post reopens a resolved conversation.
	f.sent(user, desk.ID, "one more thing")
	jobs = f.jobs(desk.ID)
	require.Equal(t, []string{"message.created", "helpdesk.status_changed"}, jobKinds(jobs[len(jobs)-2:]))
	var reopened messagingConversation
	f.do(user, http.MethodGet, fmt.Sprintf("/api/v2/conversations/%d", desk.ID), nil).decode(t, &reopened)
	require.Equal(t, "open", reopened.Status)
	require.True(t, findItem(admin, "?status=open&limit=100").NeedsReply)
}

func TestMessagingAbuseReportsDB(t *testing.T) {
	f := newMessagingFixture(t)
	alice, bob, carol := f.approved(), f.approved(), f.approved()
	admin := f.user("ADMIN", "APPROVED")
	ab := f.connect(alice, bob)
	bc := f.connect(bob, carol)
	fromBob := f.sent(bob, ab, "rude words")
	fromAlice := f.sent(alice, ab, "please stop")
	bobToCarol := f.sent(bob, bc, "elsewhere")

	report := func(reporter msgUser, body map[string]any) msgResponse {
		return f.do(reporter, http.MethodPost, "/api/v2/abuse-reports", body)
	}
	res := report(alice, map[string]any{"reportedAddress": bob.Address, "messageId": fromBob.ID, "comment": " harassment "})
	require.Equal(t, http.StatusCreated, res.Code, res.Raw)
	var created struct {
		ID int64 `json:"id"`
	}
	res.decode(t, &created)

	// The message must be the reported user's, in a conversation the reporter is in.
	require.Equal(t, http.StatusNotFound, report(alice, map[string]any{"reportedAddress": bob.Address, "messageId": fromAlice.ID}).Code)
	require.Equal(t, http.StatusNotFound, report(alice, map[string]any{"reportedAddress": bob.Address, "messageId": bobToCarol.ID}).Code)
	require.Equal(t, http.StatusNotFound, report(alice, map[string]any{"reportedAddress": alice.Address}).Code)
	require.Equal(t, http.StatusNotFound, report(alice, map[string]any{"reportedAddress": randomAddress(t)}).Code)
	require.Equal(t, http.StatusBadRequest, report(alice, map[string]any{"reportedAddress": bob.Address, "comment": strings.Repeat("x", reportCommentMax+1)}).Code)
	require.Equal(t, http.StatusCreated, report(carol, map[string]any{"reportedAddress": bob.Address}).Code)

	res = f.do(admin, http.MethodGet, "/api/v2/admin/abuse-reports?status=open&limit=100", nil)
	require.Equal(t, http.StatusOK, res.Code, res.Raw)
	var page struct {
		Items []adminAbuseReport `json:"items"`
	}
	res.decode(t, &page)
	var found *adminAbuseReport
	for i := range page.Items {
		if page.Items[i].ID == created.ID {
			found = &page.Items[i]
		}
	}
	require.NotNil(t, found)
	require.Equal(t, alice.Address, found.Reporter.Address)
	require.Equal(t, bob.Address, found.Reported.Address)
	require.Equal(t, "rude words", *found.Message.Body)
	require.Equal(t, "harassment", *found.Comment)
	require.Nil(t, found.ResolvedBy)

	resolve := func(id int64, status string) msgResponse {
		return f.do(admin, http.MethodPut, "/api/v2/admin/abuse-reports/"+strconv.FormatInt(id, 10), map[string]string{"status": status})
	}
	res = resolve(created.ID, "actioned")
	require.Equal(t, http.StatusOK, res.Code, res.Raw)
	var decided adminAbuseReport
	res.decode(t, &decided)
	require.Equal(t, "actioned", decided.Status)
	require.Equal(t, admin.Address, *decided.ResolvedBy)
	require.NotNil(t, decided.ResolvedAt)
	require.Equal(t, http.StatusConflict, resolve(created.ID, "dismissed").Code)
	require.Equal(t, http.StatusNotFound, resolve(999999999, "dismissed").Code)
	require.Equal(t, http.StatusBadRequest, resolve(created.ID, "open").Code)
}

func TestMessagingPagingAndValidationDB(t *testing.T) {
	f := newMessagingFixture(t)
	alice := f.approved()
	peers := []msgUser{f.approved(), f.approved(), f.approved()}
	var convs []int64
	for _, peer := range peers {
		convs = append(convs, f.connect(alice, peer))
	}
	// Activity order: the first conversation gets the newest message.
	f.sent(peers[0], convs[0], "newest")

	var ids []int64
	cursor := ""
	for range 3 {
		target := "/api/v2/conversations?kind=direct&limit=2"
		if cursor != "" {
			target += "&cursor=" + cursor
		}
		res := f.do(alice, http.MethodGet, target, nil)
		require.Equal(t, http.StatusOK, res.Code, res.Raw)
		var page struct {
			Items      []messagingConversation `json:"items"`
			NextCursor *string                 `json:"nextCursor"`
		}
		res.decode(t, &page)
		for _, item := range page.Items {
			ids = append(ids, item.ID)
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	require.Equal(t, []int64{convs[0], convs[2], convs[1]}, ids)
	require.Equal(t, http.StatusBadRequest, f.do(alice, http.MethodGet, "/api/v2/conversations?cursor=%21%21", nil).Code)
	require.Equal(t, http.StatusBadRequest, f.do(alice, http.MethodGet, "/api/v2/conversations?kind=group", nil).Code)

	conv := convs[1]
	var sent []int64
	for i := range 5 {
		sent = append(sent, f.sent(alice, conv, fmt.Sprintf("m%d", i)).ID)
	}
	type page struct {
		Items   []messagingMessage `json:"items"`
		HasMore bool               `json:"hasMore"`
	}
	list := func(query string) page {
		res := f.do(peers[1], http.MethodGet, fmt.Sprintf("/api/v2/conversations/%d/messages%s", conv, query), nil)
		require.Equal(t, http.StatusOK, res.Code, res.Raw)
		var p page
		res.decode(t, &p)
		return p
	}
	idsOf := func(p page) []int64 {
		out := []int64{}
		for _, item := range p.Items {
			out = append(out, item.ID)
		}
		return out
	}
	newest := list("?limit=2")
	require.Equal(t, sent[3:], idsOf(newest))
	require.True(t, newest.HasMore)
	older := list(fmt.Sprintf("?before=%d&limit=2", sent[3]))
	require.Equal(t, sent[1:3], idsOf(older))
	require.True(t, older.HasMore)
	oldest := list(fmt.Sprintf("?before=%d&limit=2", sent[1]))
	require.Equal(t, sent[:1], idsOf(oldest))
	require.False(t, oldest.HasMore)
	forward := list(fmt.Sprintf("?after=%d&limit=3", sent[0]))
	require.Equal(t, sent[1:4], idsOf(forward))
	require.True(t, forward.HasMore)
	caughtUp := list(fmt.Sprintf("?after=%d", sent[4]))
	require.Empty(t, caughtUp.Items)
	require.False(t, caughtUp.HasMore)
	require.Equal(t, http.StatusBadRequest, f.do(alice, http.MethodGet,
		fmt.Sprintf("/api/v2/conversations/%d/messages?before=%d&after=%d", conv, sent[4], sent[0]), nil).Code)

	post := func(body map[string]any) int {
		return f.do(alice, http.MethodPost, fmt.Sprintf("/api/v2/conversations/%d/messages", conv), body).Code
	}
	images := []string{"https://s.example/1.webp", "https://s.example/2.webp", "https://s.example/3.webp", "https://s.example/4.webp"}
	require.Equal(t, http.StatusCreated, post(map[string]any{"clientId": uuid.NewString(), "imageUrls": images}))
	require.Equal(t, http.StatusCreated, post(map[string]any{"clientId": uuid.NewString(), "body": strings.Repeat("ü", messageBodyMax)}))
	for name, body := range map[string]map[string]any{
		"long body":    {"clientId": uuid.NewString(), "body": strings.Repeat("ü", messageBodyMax+1)},
		"five images":  {"clientId": uuid.NewString(), "imageUrls": append(images, "https://s.example/5.webp")},
		"ftp image":    {"clientId": uuid.NewString(), "imageUrls": []string{"ftp://s.example/1.webp"}},
		"data image":   {"clientId": uuid.NewString(), "imageUrls": []string{"data:image/png;base64,AAAA"}},
		"no content":   {"clientId": uuid.NewString(), "body": "   "},
		"no client id": {"body": "x"},
		"bad clientId": {"clientId": "1234", "body": "x"},
	} {
		require.Equal(t, http.StatusBadRequest, post(body), name)
	}
}

func TestMessagingRequiresAUserSessionDB(t *testing.T) {
	f := newMessagingFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v2/conversations", nil)
	res := httptest.NewRecorder()
	f.a.router.ServeHTTP(res, req)
	require.Equal(t, http.StatusUnauthorized, res.Code)
}
