package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestMessagingDeleteOnlyForCallerDB(t *testing.T) {
	f := newMessagingFixture(t)
	alice, bob, outsider := f.approved(), f.approved(), f.approved()
	thread := f.connect(alice, bob)
	first := f.sent(alice, thread, "Before deletion")
	last := f.sent(bob, thread, "Private reply")
	path := fmt.Sprintf("/api/v2/conversations/%d", thread)
	require.Equal(t, http.StatusUnauthorized, f.do(msgUser{}, http.MethodDelete, path, nil).Code)
	require.Equal(t, http.StatusNotFound, f.do(outsider, http.MethodDelete, path, nil).Code)
	for range 2 {
		req := httptest.NewRequest(http.MethodDelete, path, nil)
		req.AddCookie(&http.Cookie{Name: userAuthCookieName, Value: alice.Token})
		deleted := httptest.NewRecorder()
		f.a.router.ServeHTTP(deleted, req)
		require.Equal(t, http.StatusNoContent, deleted.Code)
		require.Equal(t, "private, no-store", deleted.Header().Get("Cache-Control"))
	}
	jobs := f.jobs(thread)
	require.Equal(t, "conversation.revoked", jobs[len(jobs)-1].Kind)
	require.Equal(t, map[int64]bool{alice.ID: false, bob.ID: false}, jobs[len(jobs)-1].Audience)
	require.Equal(t, http.StatusConflict, f.send(alice, thread, first.ClientID, "replayed").Code)
	page := func(user msgUser, suffix string) (ids []int64, cleared int64) {
		res := f.do(user, http.MethodGet, path+"/messages"+suffix, nil)
		require.Equal(t, http.StatusOK, res.Code, res.Raw)
		var page struct {
			Items   []messagingMessage `json:"items"`
			Cleared int64              `json:"clearedThrough"`
		}
		res.decode(t, &page)
		for _, item := range page.Items {
			ids = append(ids, item.ID)
		}
		return ids, page.Cleared
	}
	ids, cleared := page(alice, "")
	require.Empty(t, ids)
	require.Equal(t, last.ID, cleared)
	ids, _ = page(alice, "?before="+fmt.Sprint(last.ID+1))
	require.Empty(t, ids, "older pagination cannot restore deleted history")
	ids, _ = page(bob, "")
	require.Equal(t, []int64{first.ID, last.ID}, ids)
	var listed struct {
		Items []messagingConversation `json:"items"`
	}
	f.do(alice, http.MethodGet, "/api/v2/conversations", nil).decode(t, &listed)
	require.Empty(t, listed.Items)
	require.Zero(t, f.unread(alice)["direct"])
	require.Equal(t, http.StatusConflict, f.send(bob, thread, uuid.NewString(), "stale send").Code)
	var view messagingConversation
	f.do(bob, http.MethodGet, path, nil).decode(t, &view)
	require.Equal(t, "revoked", view.Status)
	require.True(t, view.CanRequest)
	request := f.do(bob, http.MethodPost, "/api/v2/conversations", map[string]string{
		"recipientAddress": alice.Address, "intro": "May we talk again?",
	})
	require.Equal(t, http.StatusCreated, request.Code, request.Raw)
	request.decode(t, &view)
	require.Equal(t, thread, view.ID)
	f.do(alice, http.MethodGet, path, nil).decode(t, &view)
	require.True(t, view.CanAccept)
	require.Equal(t, 1, f.unread(alice)["requests"])
	require.Equal(t, http.StatusConflict, f.send(bob, thread, uuid.NewString(), "too soon").Code)
	ids, _ = page(alice, "")
	require.Len(t, ids, 1)
	require.Greater(t, ids[0], last.ID)
	require.Equal(t, http.StatusOK, f.setStatus(alice, thread, "accepted").Code)
	require.Equal(t, http.StatusCreated, f.send(bob, thread, uuid.NewString(), "new reply").Code)
	ids, _ = page(alice, "")
	require.Len(t, ids, 2)
	ids, _ = page(bob, "")
	require.Len(t, ids, 4)
}

func TestMessagingDeletePendingAndHelpdeskDB(t *testing.T) {
	f := newMessagingFixture(t)
	alice, bob := f.approved(), f.approved()
	thread := f.request(alice, bob).ID
	path := fmt.Sprintf("/api/v2/conversations/%d", thread)
	require.Equal(t, http.StatusNoContent, f.do(bob, http.MethodDelete, path, nil).Code)
	require.Zero(t, f.unread(bob)["requests"])
	require.Equal(t, http.StatusConflict, f.setStatus(bob, thread, "accepted").Code)
	res := f.do(alice, http.MethodPost, "/api/v2/conversations", map[string]string{"recipientAddress": bob.Address, "intro": "New request"})
	require.Equal(t, http.StatusCreated, res.Code, res.Raw)
	require.Equal(t, http.StatusOK, f.setStatus(bob, thread, "accepted").Code)
	desk := f.do(alice, http.MethodPost, "/api/v2/helpdesk", nil)
	require.Equal(t, http.StatusCreated, desk.Code, desk.Raw)
	var help messagingConversation
	desk.decode(t, &help)
	require.Equal(t, http.StatusNotFound, f.do(alice, http.MethodDelete, fmt.Sprintf("/api/v2/conversations/%d", help.ID), nil).Code)
	require.Equal(t, http.StatusBadRequest, f.do(alice, http.MethodDelete, "/api/v2/conversations/invalid", nil).Code)
}

func TestMessagingDeleteAndConcurrentSendDB(t *testing.T) {
	f := newMessagingFixture(t)
	alice, bob := f.approved(), f.approved()
	thread := f.connect(alice, bob)
	f.sent(bob, thread, "old")
	responses := make(chan msgResponse, 2)
	var group sync.WaitGroup
	group.Go(func() { responses <- f.send(bob, thread, uuid.NewString(), "concurrent") })
	group.Go(func() {
		responses <- f.do(alice, http.MethodDelete, fmt.Sprintf("/api/v2/conversations/%d", thread), nil)
	})
	group.Wait()
	close(responses)
	for res := range responses {
		require.Contains(t, []int{http.StatusNoContent, http.StatusCreated, http.StatusConflict}, res.Code, res.Raw)
	}
	res := f.do(alice, http.MethodGet, fmt.Sprintf("/api/v2/conversations/%d/messages", thread), nil)
	require.Equal(t, http.StatusOK, res.Code, res.Raw)
	var page struct {
		Items []messagingMessage `json:"items"`
	}
	res.decode(t, &page)
	require.Empty(t, page.Items)
}

func TestMessagingDeleteHidesOwnPostReceiptDB(t *testing.T) {
	f := newMessagingFixture(t)
	reader, author := f.approved(), f.approved()
	post := f.postForContact(author.Address, "APPROVED")
	ask := f.postContactRequest(reader, post, "Original introduction", http.StatusCreated)
	res := f.answerContact(author, ask.ID, "accepted")
	require.Equal(t, http.StatusOK, res.Code, res.Raw)
	var accepted postContactWire
	res.decode(t, &accepted)
	require.NotNil(t, accepted.ConversationID)
	require.Equal(t, http.StatusNoContent, f.do(reader, http.MethodDelete, fmt.Sprintf("/api/v2/conversations/%d", *accepted.ConversationID), nil).Code)
	require.Empty(t, f.postContactPage(reader, "").Items)
	require.Len(t, f.postContactPage(author, "").Items, 1)
	// Repeating the original Post request cannot reveal the cleared intro or
	// restore messaging consent. The returned thread offers a fresh request.
	repeated := f.do(reader, http.MethodPost, "/api/v2/messaging/post-requests", map[string]string{
		"postId": post, "intro": "Try again",
	})
	require.Equal(t, http.StatusOK, repeated.Code, repeated.Raw)
	var receipt postContactWire
	repeated.decode(t, &receipt)
	require.Nil(t, receipt.Intro)
	require.Equal(t, accepted.ConversationID, receipt.ConversationID)
	require.Equal(t, http.StatusConflict, f.send(reader, *receipt.ConversationID, uuid.NewString(), "blocked by consent").Code)
}

func TestMessagingDeleteBothCopiesAndBlockDB(t *testing.T) {
	f := newMessagingFixture(t)
	alice, bob := f.approved(), f.approved()
	// A request without an introduction has no messages. It is still deletable.
	res := f.do(alice, http.MethodPost, "/api/v2/conversations", map[string]string{"recipientAddress": bob.Address})
	require.Equal(t, http.StatusCreated, res.Code, res.Raw)
	var thread messagingConversation
	res.decode(t, &thread)
	path := fmt.Sprintf("/api/v2/conversations/%d", thread.ID)
	for _, caller := range []msgUser{alice, bob} {
		require.Equal(t, http.StatusNoContent, f.do(caller, http.MethodDelete, path, nil).Code)
		require.Zero(t, f.unread(caller)["requests"])
	}
	blocked := f.do(alice, http.MethodPut, "/api/v2/blocks/"+bob.Address, nil)
	require.Equal(t, http.StatusNoContent, blocked.Code, blocked.Raw)
	request := func() msgResponse {
		return f.do(bob, http.MethodPost, "/api/v2/conversations", map[string]string{"recipientAddress": alice.Address, "intro": "Fresh request"})
	}
	require.Equal(t, http.StatusNotFound, request().Code, "delete must not bypass a block")
	require.Equal(t, http.StatusNoContent, f.do(alice, http.MethodDelete, "/api/v2/blocks/"+bob.Address, nil).Code)
	require.Equal(t, http.StatusCreated, request().Code)
	require.Equal(t, 1, f.unread(alice)["requests"])
	require.Equal(t, http.StatusConflict, f.send(bob, thread.ID, uuid.NewString(), "not yet").Code)
	require.Equal(t, http.StatusOK, f.setStatus(alice, thread.ID, "accepted").Code)
}

func TestMessagingDeleteRequiresFreshPostConsentDB(t *testing.T) {
	f := newMessagingFixture(t)
	reader, author := f.approved(), f.approved()
	thread := f.connect(reader, author)
	f.sent(author, thread, "Old conversation")
	oldPost := f.postForContact(author.Address, "APPROVED")
	oldRequest := f.postContactRequest(reader, oldPost, "Old pending request", http.StatusCreated)
	require.Equal(t, http.StatusNoContent, f.do(reader, http.MethodDelete, fmt.Sprintf("/api/v2/conversations/%d", thread), nil).Code)
	require.Equal(t, http.StatusConflict, f.answerContact(author, oldRequest.ID, "accepted").Code)
	// A genuinely new Post request after deletion expresses fresh consent.
	newPost := f.postForContact(author.Address, "APPROVED")
	newRequest := f.postContactRequest(reader, newPost, "New request", http.StatusCreated)
	require.Equal(t, http.StatusOK, f.answerContact(author, newRequest.ID, "accepted").Code)
	var page struct {
		Items []messagingMessage `json:"items"`
	}
	f.do(reader, http.MethodGet, fmt.Sprintf("/api/v2/conversations/%d/messages", thread), nil).decode(t, &page)
	require.Len(t, page.Items, 1)
	require.Equal(t, "New request", *page.Items[0].Body)
}
