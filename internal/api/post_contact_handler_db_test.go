package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	postRequestsPath = "/api/v2/messaging/post-requests"
)

type (
	postContactPage struct {
		Items      []postContactWire `json:"items"`
		NextCursor *string           `json:"nextCursor"`
	}
)

func (f *msgFixture) postForContact(author, status string) string {
	f.t.Helper()
	ctx := f.t.Context()
	var id int64
	err := f.a.store.Pool().QueryRow(ctx, `
		INSERT INTO reports (title, description, author_address, status)
		VALUES ('Post contact test', 'test', $1, $2)
		RETURNING id`, author, status).Scan(&id)
	require.NoError(f.t, err)
	f.t.Cleanup(func() {
		_, err := f.a.store.Pool().Exec(context.Background(), "DELETE FROM reports WHERE id = $1", id)
		require.NoError(f.t, err)
	})
	return strconv.FormatInt(id, 10)
}

func (f *msgFixture) postContactRequest(user msgUser, postID, intro string, code int) postContactWire {
	f.t.Helper()
	res := f.do(user, http.MethodPost, postRequestsPath, map[string]string{"postId": postID, "intro": intro})
	require.Equal(f.t, code, res.Code, res.Raw)
	var item postContactWire
	res.decode(f.t, &item)
	return item
}

func (f *msgFixture) postContactPage(user msgUser, search string) postContactPage {
	f.t.Helper()
	res := f.do(user, http.MethodGet, postRequestsPath+search, nil)
	require.Equal(f.t, http.StatusOK, res.Code, res.Raw)
	var page postContactPage
	res.decode(f.t, &page)
	return page
}

func (f *msgFixture) answerContact(user msgUser, id, status string) msgResponse {
	f.t.Helper()
	return f.do(user, http.MethodPut, postRequestsPath+"/"+id+"/status", map[string]string{"status": status})
}

func TestPostContactHidesAuthorUntilAcceptanceDB(t *testing.T) {
	f := newMessagingFixture(t)
	reader, author, outsider := f.approved(), f.approved(), f.approved()
	postID := f.postForContact(author.Address, "APPROVED")
	existing := f.connect(reader, author)

	ask := f.postContactRequest(reader, postID, "Question about the Post", http.StatusCreated)
	require.Equal(t, "pending", ask.Status)
	require.Equal(t, "outgoing", ask.Direction)
	require.Nil(t, ask.Requester)
	require.Nil(t, ask.ConversationID)
	require.Equal(t, ask, f.postContactRequest(reader, postID, "Ignored retry text", http.StatusOK))
	require.Equal(t, []postContactWire{ask}, f.postContactPage(reader, "").Items)
	require.Empty(t, f.postContactPage(outsider, "").Items)
	require.Equal(t, http.StatusNotFound, f.answerContact(outsider, ask.ID, "accepted").Code)
	require.Equal(t, http.StatusNotFound, f.answerContact(reader, ask.ID, "accepted").Code)
	require.Equal(t, 0, f.unread(reader)["postRequests"])
	require.Equal(t, 1, f.unread(author)["postRequests"])
	require.Equal(t, 0, f.unread(author)["requests"])
	incoming := f.postContactPage(author, "").Items
	require.Len(t, incoming, 1)
	require.Equal(t, "incoming", incoming[0].Direction)
	require.NotNil(t, incoming[0].Requester)
	require.Equal(t, reader.Address, incoming[0].Requester.Address)
	require.Nil(t, incoming[0].ConversationID)

	var copies int
	require.NoError(t, f.a.store.Pool().QueryRow(t.Context(), `
		SELECT count(*) FROM messages WHERE conversation_id = $1 AND body = $2`, existing, *ask.Intro).Scan(&copies))
	require.Zero(t, copies, "the ordinary thread cannot reveal the Post link before consent")

	const attempts = 3
	responses := make(chan msgResponse, attempts)
	var group sync.WaitGroup
	for range attempts {
		group.Go(func() { responses <- f.answerContact(author, ask.ID, "accepted") })
	}
	group.Wait()
	close(responses)
	for res := range responses {
		require.Equal(t, http.StatusOK, res.Code, res.Raw)
		var accepted postContactWire
		res.decode(t, &accepted)
		require.Equal(t, "accepted", accepted.Status)
		require.Equal(t, &existing, accepted.ConversationID)
	}
	readerView := f.postContactPage(reader, "").Items
	require.Len(t, readerView, 1)
	require.Equal(t, &existing, readerView[0].ConversationID)
	require.Zero(t, f.unread(author)["postRequests"])
	require.NoError(t, f.a.store.Pool().QueryRow(t.Context(), `
		SELECT count(*) FROM messages WHERE conversation_id = $1 AND body = $2`, existing, *ask.Intro).Scan(&copies))
	require.Equal(t, 1, copies)
}

func TestPostContactSuppressesDeliveryWithoutExposingAuthorDB(t *testing.T) {
	f := newMessagingFixture(t)
	reader, author := f.approved(), f.approved()
	blockedPost := f.postForContact(author.Address, "APPROVED")
	unknownAuthorPost := f.postForContact(randomAddress(t), "APPROVED")
	hiddenPost := f.postForContact(author.Address, "DRAFT")
	require.Equal(t, http.StatusNoContent, f.do(reader, http.MethodPut, "/api/v2/blocks/"+author.Address, nil).Code)

	blocked := f.postContactRequest(reader, blockedPost, "", http.StatusCreated)
	unavailable := f.postContactRequest(reader, unknownAuthorPost, "", http.StatusCreated)
	for _, item := range []postContactWire{blocked, unavailable} {
		require.Equal(t, "pending", item.Status)
		require.Equal(t, "outgoing", item.Direction)
		require.Nil(t, item.Requester)
		require.Nil(t, item.ConversationID)
	}
	require.Equal(t, http.StatusNotFound, f.do(reader, http.MethodPost, postRequestsPath, map[string]string{"postId": hiddenPost}).Code)
	require.Equal(t, http.StatusNotFound, f.answerContact(author, blocked.ID, "accepted").Code)
	require.Empty(t, f.postContactPage(author, "").Items)
	require.Zero(t, f.unread(author)["postRequests"])

	first := f.postContactPage(reader, "?limit=1")
	require.Len(t, first.Items, 1)
	require.NotNil(t, first.NextCursor)
	second := f.postContactPage(reader, "?limit=1&cursor="+*first.NextCursor)
	require.Len(t, second.Items, 1)
	require.NotEqual(t, first.Items[0].ID, second.Items[0].ID)
	require.Nil(t, second.NextCursor)
	require.Empty(t, f.postContactPage(author, "?cursor="+*first.NextCursor).Items)
}

func TestPostContactCreatesAcceptedThreadAndRespectsClosedRulesDB(t *testing.T) {
	f := newMessagingFixture(t)
	reader, author := f.approved(), f.approved()
	firstPost := f.postForContact(author.Address, "APPROVED")
	ask := f.postContactRequest(reader, firstPost, "First introduction", http.StatusCreated)
	res := f.answerContact(author, ask.ID, "accepted")
	require.Equal(t, http.StatusOK, res.Code, res.Raw)
	var accepted postContactWire
	res.decode(t, &accepted)
	require.NotNil(t, accepted.ConversationID)
	res = f.do(reader, http.MethodGet, fmt.Sprintf("/api/v2/conversations/%d", *accepted.ConversationID), nil)
	require.Equal(t, http.StatusOK, res.Code, res.Raw)
	var thread messagingConversation
	res.decode(t, &thread)
	require.Equal(t, "accepted", thread.Status)
	require.Equal(t, author.Address, thread.Peer.Address)
	require.Equal(t, ask.Intro, thread.LastMessage.Body)
	require.Nil(t, thread.LastMessage.Context, "older inbox clients never receive a new message context kind")

	require.Equal(t, http.StatusOK, f.setStatus(reader, *accepted.ConversationID, "revoked").Code)
	secondPost := f.postForContact(author.Address, "APPROVED")
	second := f.postContactRequest(reader, secondPost, "", http.StatusCreated)
	require.Equal(t, http.StatusConflict, f.answerContact(author, second.ID, "accepted").Code,
		"an author cannot reopen a thread the requester closed")
	for range 2 {
		res := f.answerContact(author, second.ID, "declined")
		require.Equal(t, http.StatusOK, res.Code, res.Raw)
		var declined postContactWire
		res.decode(t, &declined)
		require.Equal(t, "declined", declined.Status)
		require.Nil(t, declined.ConversationID)
	}
	require.Equal(t, http.StatusConflict, f.answerContact(author, second.ID, "accepted").Code)
}

func TestPostContactValidationAndSessionDB(t *testing.T) {
	f := newMessagingFixture(t)
	reader := f.approved()
	for _, tc := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, postRequestsPath + "?cursor=invalid", nil},
		{http.MethodGet, postRequestsPath + "?limit=0", nil},
		{http.MethodPost, postRequestsPath, "invalid"},
		{http.MethodPost, postRequestsPath, map[string]string{"postId": "0"}},
		{http.MethodPost, postRequestsPath, map[string]string{"postId": "01"}},
		{http.MethodPost, postRequestsPath, map[string]string{"postId": "1", "intro": strings.Repeat("x", introMax+1)}},
		{http.MethodPut, postRequestsPath + "/invalid/status", map[string]string{"status": "accepted"}},
		{http.MethodPut, postRequestsPath + "/00000000-0000-4000-8000-000000000001/status", "invalid"},
		{http.MethodPut, postRequestsPath + "/00000000-0000-4000-8000-000000000001/status", map[string]string{"status": "open"}},
	} {
		res := f.do(reader, tc.method, tc.path, tc.body)
		require.Equal(t, http.StatusBadRequest, res.Code, res.Raw)
	}
	for _, token := range []string{"", "invalid-session"} {
		for _, tc := range []struct {
			method string
			path   string
			body   any
		}{
			{http.MethodGet, postRequestsPath, nil},
			{http.MethodPost, postRequestsPath, map[string]string{"postId": "1"}},
			{http.MethodPut, postRequestsPath + "/00000000-0000-4000-8000-000000000001/status", map[string]string{"status": "accepted"}},
			{http.MethodGet, resourceTargetsPath + "?kind=want&resourceId=1", nil},
		} {
			res := f.do(msgUser{Token: token}, tc.method, tc.path, tc.body)
			require.Equal(t, http.StatusUnauthorized, res.Code, res.Raw)
		}
	}
}

func TestPostContactRechecksDeliveryAndQuotaDB(t *testing.T) {
	f := newMessagingFixture(t)
	reader, author := f.approved(), f.approved()
	postID := f.postForContact(author.Address, "APPROVED")
	ask := f.postContactRequest(reader, postID, "", http.StatusCreated)
	require.Equal(t, http.StatusNoContent, f.do(author, http.MethodPut, "/api/v2/blocks/"+reader.Address, nil).Code)
	require.Empty(t, f.postContactPage(author, "").Items)
	require.Equal(t, http.StatusNotFound, f.answerContact(author, ask.ID, "accepted").Code)
	outgoing := f.postContactPage(reader, "").Items
	require.Len(t, outgoing, 1)
	require.Equal(t, ask, outgoing[0], "blocking cannot reveal the author through a request state change")

	ownPost := f.postForContact(reader.Address, "APPROVED")
	self := f.postContactRequest(reader, ownPost, "", http.StatusCreated)
	require.Nil(t, self.Requester)
	require.Nil(t, self.ConversationID)
	require.Equal(t, "pending", self.Status)

	_, err := f.a.store.Pool().Exec(t.Context(), "UPDATE messaging_request_quota SET used = $2 WHERE user_id = $1", reader.ID, messagingDailyRequestQuota)
	require.NoError(t, err)
	anotherPost := f.postForContact(author.Address, "APPROVED")
	res := f.do(reader, http.MethodPost, postRequestsPath, map[string]string{"postId": anotherPost})
	require.Equal(t, http.StatusTooManyRequests, res.Code, res.Raw)
	require.Len(t, f.postContactPage(reader, "").Items, 2, "quota failures roll back the new request")
	require.Equal(t, ask, f.postContactRequest(reader, postID, "", http.StatusOK), "retrying a saved request spends no further quota")
}

func TestPostContactAcceptsExistingPendingAndOwnerClosedThreadsDB(t *testing.T) {
	for _, state := range []string{"pending-from-author", "revoked-by-author"} {
		t.Run(state, func(t *testing.T) {
			f := newMessagingFixture(t)
			reader, author := f.approved(), f.approved()
			var threadID int64
			if state == "pending-from-author" {
				threadID = f.request(author, reader).ID
			} else {
				threadID = f.connect(reader, author)
				require.Equal(t, http.StatusOK, f.setStatus(author, threadID, "revoked").Code)
			}
			postID := f.postForContact(author.Address, "APPROVED")
			ask := f.postContactRequest(reader, postID, "", http.StatusCreated)
			res := f.answerContact(author, ask.ID, "accepted")
			require.Equal(t, http.StatusOK, res.Code, res.Raw)
			var accepted postContactWire
			res.decode(t, &accepted)
			require.Equal(t, &threadID, accepted.ConversationID)
			require.Equal(t, "accepted", accepted.Status)
			_, err := f.a.store.Pool().Exec(t.Context(), "DELETE FROM conversations WHERE id = $1", threadID)
			require.NoError(t, err)
			rows := f.postContactPage(reader, "").Items
			require.Len(t, rows, 1)
			require.Equal(t, "accepted", rows[0].Status)
			require.Nil(t, rows[0].ConversationID, "a removed thread retains the answered request state")
		})
	}
}

func TestPostContactConcurrentCreationDB(t *testing.T) {
	f := newMessagingFixture(t)
	reader, author := f.approved(), f.approved()
	postID := f.postForContact(author.Address, "APPROVED")
	const attempts = 4
	responses := make(chan msgResponse, attempts)
	var group sync.WaitGroup
	for range attempts {
		group.Go(func() {
			responses <- f.do(reader, http.MethodPost, postRequestsPath, map[string]string{"postId": postID})
		})
	}
	group.Wait()
	close(responses)
	var id string
	created := 0
	for res := range responses {
		require.Contains(t, []int{http.StatusOK, http.StatusCreated}, res.Code, res.Raw)
		if res.Code == http.StatusCreated {
			created++
		}
		var item postContactWire
		res.decode(t, &item)
		if id == "" {
			id = item.ID
		}
		require.Equal(t, id, item.ID)
	}
	require.Equal(t, 1, created)
	require.Len(t, f.postContactPage(reader, "").Items, 1)
	require.Len(t, f.postContactPage(author, "").Items, 1)
	var used int
	require.NoError(t, f.a.store.Pool().QueryRow(t.Context(), "SELECT used FROM messaging_request_quota WHERE user_id = $1", reader.ID).Scan(&used))
	require.Equal(t, 1, used, "simultaneous clicks spend request quota only once")
}
