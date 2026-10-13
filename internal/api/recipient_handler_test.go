package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestRecipientUnicodeProfileAndResolve(t *testing.T) {
	a, token := newAdminAPI(t)
	defer seedProfileAccount(t, a)()
	name := "Ž / #?& 李"
	res, result := postJSON(t, a, token, "/auth/profile", map[string]any{"name": "Unicode Name", "email": "unicode-api@example.test", "username": name})
	if res.Code != http.StatusOK || result["username"] != name {
		t.Fatalf("save: %d %s", res.Code, res.Body.String())
	}
	res, result = getJSON(t, a, token, "/api/v2/recipients/resolve?"+url.Values{"name": {name}}.Encode())
	if res.Code != http.StatusOK {
		t.Fatalf("resolve: %d %s", res.Code, res.Body.String())
	}
	recipient, ok := result["recipient"].(map[string]any)
	if !ok || recipient["alias"] != name {
		t.Fatalf("recipient: %#v", result)
	}
}
