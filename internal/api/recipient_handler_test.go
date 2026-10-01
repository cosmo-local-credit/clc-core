package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUsernameSuggestionIsPublicAndRateLimited(t *testing.T) {
	a := newTestRouter(t)
	send := func(remote, hint string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v2/recipients/suggest?hint="+hint, nil)
		req.RemoteAddr = remote
		res := httptest.NewRecorder()
		a.router.ServeHTTP(res, req)
		return res
	}
	// Invalid hints exercise public validation without needing a database.
	for range marketRateBurst {
		if res := send("198.51.100.41:1234", strings.Repeat("a", 121)); res.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 without authentication", res.Code)
		}
	}
	// A valid hint would reach the absent test store if throttling did not run first.
	limited := send("198.51.100.41:1234", "alice")
	if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") == "" {
		t.Fatalf("expected 429 with Retry-After, got %d: %s", limited.Code, limited.Body.String())
	}
	if res := send("198.51.100.42:1234", strings.Repeat("a", 121)); res.Code != http.StatusBadRequest {
		t.Fatalf("another client was limited: status %d", res.Code)
	}
}
