package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// The middleware normalizes the claim to its checksummed form before any handler
// sees it, and update-user-profile matches blockchain_address exactly, so the
// fixture has to be stored in that same form.
var (
	avatarTestAddress = common.HexToAddress(accountAddress).Hex()
)

// A write followed by a read on a cold cache, through the real route table.
//
// The write projection and the read projection are built by hand in two
// different handlers, so nothing but a round trip proves they agree — and when
// they did not, a saved avatar survived exactly until the client refetched.
func TestProfileAvatarSurvivesAReRead(t *testing.T) {
	a, token := newAdminAPI(t)
	defer seedProfileAccount(t, a)()

	avatar := "https://content.example/profile/abc_400_a3f91c2e.webp"
	res, result := postJSON(t, a, token, "/auth/profile", map[string]any{
		"name":      "Avatar Owner",
		"email":     "avatar-roundtrip@example.com",
		"avatarUrl": avatar,
	})
	if res.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", res.Code, res.Body.String())
	}
	if got := result["avatarUrl"]; got != avatar {
		t.Fatalf("update returned avatarUrl = %v, want %q", got, avatar)
	}

	res, result = getJSON(t, a, token, "/auth/profile")
	if res.Code != http.StatusOK {
		t.Fatalf("read status = %d body=%s", res.Code, res.Body.String())
	}
	if got := result["avatarUrl"]; got != avatar {
		t.Fatalf("read returned avatarUrl = %v, want %q", got, avatar)
	}
}

// Clearing is an explicit flag, matching clearLocation in the same handler, and it
// must survive the same round trip.
func TestProfileAvatarClearsWithAnExplicitFlag(t *testing.T) {
	a, token := newAdminAPI(t)
	defer seedProfileAccount(t, a)()

	base := map[string]any{"name": "Avatar Owner", "email": "avatar-clear@example.com"}
	set := map[string]any{"name": base["name"], "email": base["email"], "avatarUrl": "https://content.example/profile/a_400_aaaaaaaa.webp"}
	if res, _ := postJSON(t, a, token, "/auth/profile", set); res.Code != http.StatusOK {
		t.Fatalf("set status = %d", res.Code)
	}

	// Omitted: the stored avatar must survive a client that knows nothing about it.
	if res, result := postJSON(t, a, token, "/auth/profile", base); res.Code != http.StatusOK {
		t.Fatalf("omit status = %d", res.Code)
	} else if result["avatarUrl"] == nil {
		t.Fatal("an omitted avatarUrl cleared the stored one")
	}

	cleared := map[string]any{"name": base["name"], "email": base["email"], "clearAvatar": true}
	if res, result := postJSON(t, a, token, "/auth/profile", cleared); res.Code != http.StatusOK {
		t.Fatalf("clear status = %d body=%s", res.Code, res.Body.String())
	} else if result["avatarUrl"] != nil {
		t.Fatalf("avatarUrl = %v after clearing, want null", result["avatarUrl"])
	}

	if _, result := getJSON(t, a, token, "/auth/profile"); result["avatarUrl"] != nil {
		t.Fatalf("avatarUrl = %v on re-read, want null", result["avatarUrl"])
	}
}

// The database refuses a non-HTTP URL, so the request must be refused first —
// with a 400 naming the field rather than a 500 from a constraint.
func TestProfileAvatarRejectsANonHttpScheme(t *testing.T) {
	a, token := newAdminAPI(t)
	defer seedProfileAccount(t, a)()

	for _, avatar := range []string{
		"data:image/jpeg;base64,/9j/4AAQSkZJRg",
		"ftp://content.example/a.webp",
		"javascript:alert(1)",
	} {
		res, _ := postJSON(t, a, token, "/auth/profile", map[string]any{
			"name":      "Avatar Owner",
			"email":     "avatar-scheme@example.com",
			"avatarUrl": avatar,
		})
		if res.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400 body=%s", avatar, res.Code, res.Body.String())
		}
	}
}

// One approved account at the address the test token claims, removed afterwards.
func seedProfileAccount(t *testing.T, a *API) func() {
	t.Helper()
	ctx := context.Background()

	if _, err := a.store.Pool().Exec(ctx, `
		INSERT INTO users (blockchain_address, name, email, country, role, status)
		VALUES ($1, 'Avatar Owner', $2, 'KE', 'SUPERADMIN', 'APPROVED')
		ON CONFLICT (blockchain_address) DO UPDATE SET avatar_url = NULL`,
		avatarTestAddress, strings.ToLower(avatarTestAddress)+"@example.com"); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	return func() {
		if _, err := a.store.Pool().Exec(context.Background(),
			`DELETE FROM users WHERE blockchain_address = $1`, avatarTestAddress); err != nil {
			t.Errorf("cleanup account: %v", err)
		}
	}
}

func postJSON(t *testing.T, a *API, token, target string, body map[string]any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(string(encoded)))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "198.51.100.9:5000"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, req)

	var envelope struct {
		Ok     bool           `json:"ok"`
		Result map[string]any `json:"result"`
	}
	if res.Code == http.StatusOK {
		if err := json.Unmarshal(res.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode %s: %v", target, err)
		}
	}
	return res, envelope.Result
}

// Both at once is a contradiction, not a precedence question.
func TestProfileAvatarRejectsSettingAndClearingTogether(t *testing.T) {
	a, token := newAdminAPI(t)
	defer seedProfileAccount(t, a)()

	res, _ := postJSON(t, a, token, "/auth/profile", map[string]any{
		"name":        "Avatar Owner",
		"email":       "avatar-both@example.com",
		"avatarUrl":   "https://content.example/profile/a_400_aaaaaaaa.webp",
		"clearAvatar": true,
	})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 body=%s", res.Code, res.Body.String())
	}
}
