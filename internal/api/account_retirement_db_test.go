package api

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// These tests need a live database (CORE_TEST_DSN); newAdminAPI skips without one.
// Fixtures are committed and removed through cleanupRoleTestUsers.

const (
	retireUserAddress  = "0x33c573149db22e759fB9A38Bcc461C12855c2622"
	retireAdminAddress = "0x44d573149db22e759fB9A38Bcc461C12855c2633"
	retireOtherAdmin   = "0x55e573149db22e759fB9A38Bcc461C12855c2644"
)

func retireSigningKey(a *API) ed25519.PrivateKey {
	key, _ := a.signingKey.(ed25519.PrivateKey)
	return key
}

func sendJSON(t *testing.T, a *API, method, target, token, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.RemoteAddr = "198.51.100.77:5000"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, req)

	var envelope struct {
		Result map[string]any `json:"result"`
	}
	_ = json.Unmarshal(res.Body.Bytes(), &envelope)
	return res, envelope.Result
}

func countRows(t *testing.T, a *API, query string, args ...any) int {
	t.Helper()
	var n int
	if err := a.store.Pool().QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func TestAccountBanHierarchyAndHistory(t *testing.T) {
	a, superToken := newAdminAPI(t)
	t.Cleanup(func() { cleanupRoleTestUsers(t, a) })
	cleanupRoleTestUsers(t, a)

	user := seedRoleTestUser(t, a, retireUserAddress, "USER", "APPROVED")
	admin := seedRoleTestUser(t, a, retireAdminAddress, "ADMIN", "APPROVED")
	otherAdmin := seedRoleTestUser(t, a, retireOtherAdmin, "ADMIN", "APPROVED")
	userToken := mintTokenForUser(t, retireSigningKey(a), user.ID, retireUserAddress, "USER")
	adminToken := mintTokenForUser(t, retireSigningKey(a), admin.ID, retireAdminAddress, "ADMIN")

	t.Run("admin cannot ban another admin or a superadmin", func(t *testing.T) {
		res, _ := sendJSON(t, a, http.MethodPut, "/api/v2/admin/users/"+otherAdmin.EthereumAddress+"/reject", adminToken, "")
		if res.Code != http.StatusForbidden {
			t.Fatalf("admin banning admin = %d, want 403", res.Code)
		}
		res, _ = sendJSON(t, a, http.MethodPut, "/api/v2/admin/users/"+accountAddress+"/reject", adminToken, "")
		if res.Code != http.StatusForbidden {
			t.Fatalf("admin banning superadmin = %d, want 403", res.Code)
		}
	})

	t.Run("superadmin cannot ban a superadmin or themselves", func(t *testing.T) {
		res, _ := sendJSON(t, a, http.MethodPut, "/api/v2/admin/users/"+accountAddress+"/reject", superToken, "")
		if res.Code != http.StatusForbidden {
			t.Fatalf("superadmin self-ban = %d, want 403", res.Code)
		}
	})

	t.Run("ban records one event, is idempotent and ends the session at once", func(t *testing.T) {
		if res, _ := sendJSON(t, a, http.MethodGet, "/api/v2/users/me", userToken, ""); res.Code != http.StatusOK {
			t.Fatalf("user before ban = %d, want 200: %s", res.Code, res.Body.String())
		}
		target := "/api/v2/admin/users/" + user.EthereumAddress + "/reject"
		for i := range 2 {
			res, _ := sendJSON(t, a, http.MethodPut, target, adminToken, `{"reason":"spam listings"}`)
			if res.Code != http.StatusOK {
				t.Fatalf("ban %d = %d: %s", i, res.Code, res.Body.String())
			}
		}
		if n := countRows(t, a, `SELECT count(*) FROM user_moderation_events WHERE user_id=$1 AND action='ban'`, user.ID); n != 1 {
			t.Fatalf("ban events = %d, want 1", n)
		}
		if res, _ := sendJSON(t, a, http.MethodGet, "/api/v2/users/me", userToken, ""); res.Code != http.StatusUnauthorized {
			t.Fatalf("banned user's old session = %d, want 401", res.Code)
		}
		res, result := sendJSON(t, a, http.MethodGet, "/api/v2/admin/users/"+user.EthereumAddress+"/moderation-history", adminToken, "")
		if res.Code != http.StatusOK {
			t.Fatalf("history = %d: %s", res.Code, res.Body.String())
		}
		events, _ := result["events"].([]any)
		first, _ := events[0].(map[string]any)
		if len(events) != 1 || first["reason"] != "spam listings" {
			t.Fatalf("history = %#v", events)
		}
	})

	t.Run("unban records one event", func(t *testing.T) {
		target := "/api/v2/admin/users/" + user.EthereumAddress + "/approve"
		for i := range 2 {
			if res, _ := sendJSON(t, a, http.MethodPut, target, adminToken, ""); res.Code != http.StatusOK {
				t.Fatalf("unban %d = %d", i, res.Code)
			}
		}
		if n := countRows(t, a, `SELECT count(*) FROM user_moderation_events WHERE user_id=$1 AND action='unban'`, user.ID); n != 1 {
			t.Fatalf("unban events = %d, want 1", n)
		}
		if res, _ := sendJSON(t, a, http.MethodGet, "/api/v2/users/me", userToken, ""); res.Code != http.StatusOK {
			t.Fatalf("unbanned user = %d, want 200", res.Code)
		}
	})
}

func TestAccountRetirementLifecycle(t *testing.T) {
	a, superToken := newAdminAPI(t)
	t.Cleanup(func() { cleanupRoleTestUsers(t, a) })
	cleanupRoleTestUsers(t, a)

	user := seedRoleTestUser(t, a, retireUserAddress, "USER", "APPROVED")
	admin := seedRoleTestUser(t, a, retireAdminAddress, "ADMIN", "APPROVED")
	userToken := mintTokenForUser(t, retireSigningKey(a), user.ID, retireUserAddress, "USER")
	adminToken := mintTokenForUser(t, retireSigningKey(a), admin.ID, retireAdminAddress, "ADMIN")

	t.Run("retirement needs the acknowledgement", func(t *testing.T) {
		res, _ := sendJSON(t, a, http.MethodPost, "/api/v2/users/me/retirement", userToken, `{"reason":"leaving"}`)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("without acknowledgement = %d, want 400", res.Code)
		}
	})

	t.Run("reason is capped", func(t *testing.T) {
		body := `{"acknowledged":true,"reason":"` + strings.Repeat("x", 2001) + `"}`
		res, _ := sendJSON(t, a, http.MethodPost, "/api/v2/users/me/retirement", userToken, body)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("long reason = %d, want 400", res.Code)
		}
	})

	t.Run("retire is immediate, idempotent and six months long", func(t *testing.T) {
		var closes string
		for i := range 2 {
			res, result := sendJSON(t, a, http.MethodPost, "/api/v2/users/me/retirement", userToken, `{"acknowledged":true}`)
			if res.Code != http.StatusOK {
				t.Fatalf("retire %d = %d: %s", i, res.Code, res.Body.String())
			}
			if result["accountLifecycle"] != "RETIREMENT_SCHEDULED" {
				t.Fatalf("lifecycle = %v", result["accountLifecycle"])
			}
			closes, _ = result["closesAt"].(string)
		}
		closesAt, err := time.Parse(time.RFC3339Nano, closes)
		if err != nil {
			t.Fatalf("closesAt %q: %v", closes, err)
		}
		want := time.Now().AddDate(0, 6, 0)
		if d := closesAt.Sub(want); d > time.Minute || d < -time.Minute {
			t.Fatalf("closesAt = %s, want about %s", closesAt, want)
		}
		if n := countRows(t, a, `SELECT count(*) FROM account_lifecycle_events WHERE user_id=$1 AND action='retired'`, user.ID); n != 1 {
			t.Fatalf("retired events = %d, want 1", n)
		}
	})

	t.Run("recovery session blocks normal writes and keeps reads", func(t *testing.T) {
		res, _ := sendJSON(t, a, http.MethodPost, "/api/v2/users/me/wants", userToken, `{}`)
		if res.Code != http.StatusForbidden || !strings.Contains(res.Body.String(), "E23") {
			t.Fatalf("want create while retired = %d %s, want 403 E23", res.Code, res.Body.String())
		}
		if res, _ := sendJSON(t, a, http.MethodGet, "/api/v2/users/me", userToken, ""); res.Code != http.StatusOK {
			t.Fatalf("read while retired = %d, want 200", res.Code)
		}
		if res, _ := sendJSON(t, a, http.MethodGet, "/api/v2/conversations", userToken, ""); res.Code != http.StatusForbidden {
			t.Fatalf("messaging while retired = %d, want 403", res.Code)
		}
	})

	t.Run("a retiring operator loses admin access", func(t *testing.T) {
		if res, _ := sendJSON(t, a, http.MethodGet, "/api/v2/admin/users", adminToken, ""); res.Code != http.StatusOK {
			t.Fatalf("admin before retirement = %d, want 200", res.Code)
		}
		res, _ := sendJSON(t, a, http.MethodPost, "/api/v2/admin/users/"+admin.EthereumAddress+"/retirement", adminToken, "")
		if res.Code != http.StatusForbidden {
			t.Fatalf("admin retiring themselves via admin route = %d, want 403", res.Code)
		}
		if res, _ := sendJSON(t, a, http.MethodPost, "/api/v2/users/me/retirement", adminToken, `{"acknowledged":true}`); res.Code != http.StatusOK {
			t.Fatalf("admin self-retire = %d: %s", res.Code, res.Body.String())
		}
		if res, _ := sendJSON(t, a, http.MethodGet, "/api/v2/admin/users", adminToken, ""); res.Code != http.StatusForbidden {
			t.Fatalf("retiring admin on admin route = %d, want 403", res.Code)
		}
	})

	t.Run("restore reopens the account and is idempotent", func(t *testing.T) {
		for i := range 2 {
			res, result := sendJSON(t, a, http.MethodPost, "/api/v2/users/me/retirement/restore", userToken, "")
			if res.Code != http.StatusOK {
				t.Fatalf("restore %d = %d: %s", i, res.Code, res.Body.String())
			}
			if result["accountLifecycle"] != "ACTIVE" {
				t.Fatalf("lifecycle = %v", result["accountLifecycle"])
			}
		}
		if n := countRows(t, a, `SELECT count(*) FROM account_lifecycle_events WHERE user_id=$1 AND action='restored'`, user.ID); n != 1 {
			t.Fatalf("restored events = %d, want 1", n)
		}
	})

	t.Run("an operator retires and restores a user, and restore never unbans", func(t *testing.T) {
		target := "/api/v2/admin/users/" + user.EthereumAddress
		res, result := sendJSON(t, a, http.MethodPost, target+"/retirement", superToken, `{"reason":"requested by phone"}`)
		if res.Code != http.StatusOK || result["accountLifecycle"] != "RETIREMENT_SCHEDULED" {
			t.Fatalf("admin retire = %d %s", res.Code, res.Body.String())
		}
		if res, _ := sendJSON(t, a, http.MethodPut, target+"/reject", superToken, `{"reason":"abuse"}`); res.Code != http.StatusOK {
			t.Fatalf("ban retiring account = %d", res.Code)
		}
		if res, _ := sendJSON(t, a, http.MethodPost, target+"/retirement/restore", superToken, ""); res.Code != http.StatusOK {
			t.Fatalf("admin restore = %d", res.Code)
		}
		var status string
		if err := a.store.Pool().QueryRow(context.Background(), `SELECT status::text FROM users WHERE id=$1`, user.ID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != "REJECTED" {
			t.Fatalf("status after restore = %s, want REJECTED", status)
		}
		res, result = sendJSON(t, a, http.MethodGet, target+"/retirement", superToken, "")
		if res.Code != http.StatusOK || result["accountLifecycle"] != "ACTIVE" {
			t.Fatalf("admin read = %d %s", res.Code, res.Body.String())
		}
	})

	t.Run("restore after closing is refused", func(t *testing.T) {
		target := "/api/v2/admin/users/" + user.EthereumAddress
		if res, _ := sendJSON(t, a, http.MethodPut, target+"/approve", superToken, ""); res.Code != http.StatusOK {
			t.Fatalf("unban = %d", res.Code)
		}
		if res, _ := sendJSON(t, a, http.MethodPost, target+"/retirement", superToken, ""); res.Code != http.StatusOK {
			t.Fatalf("retire = %d", res.Code)
		}
		if _, err := a.store.Pool().Exec(context.Background(),
			`UPDATE account_retirements SET requested_at = now() - interval '7 months', closes_at = now() - interval '1 day' WHERE user_id=$1 AND restored_at IS NULL`, user.ID); err != nil {
			t.Fatal(err)
		}
		if res, _ := sendJSON(t, a, http.MethodPost, target+"/retirement/restore", superToken, ""); res.Code != http.StatusConflict {
			t.Fatalf("late restore = %d, want 409", res.Code)
		}
	})
}

func TestAccountRetirementRegressions(t *testing.T) {
	a, _ := newAdminAPI(t)
	t.Cleanup(func() { cleanupRoleTestUsers(t, a) })
	cleanupRoleTestUsers(t, a)

	user := seedRoleTestUser(t, a, retireUserAddress, "USER", "APPROVED")
	userToken := mintTokenForUser(t, retireSigningKey(a), user.ID, retireUserAddress, "USER")

	t.Run("a promoted superadmin cannot retire with an old user token", func(t *testing.T) {
		if _, err := a.store.Pool().Exec(context.Background(), `UPDATE users SET role='SUPERADMIN' WHERE id=$1`, user.ID); err != nil {
			t.Fatal(err)
		}
		res, _ := sendJSON(t, a, http.MethodPost, "/api/v2/users/me/retirement", userToken, `{"acknowledged":true}`)
		if res.Code != http.StatusForbidden {
			t.Fatalf("promoted superadmin retire = %d, want 403: %s", res.Code, res.Body.String())
		}
		if n := countRows(t, a, `SELECT count(*) FROM account_retirements WHERE user_id=$1`, user.ID); n != 0 {
			t.Fatalf("retirement rows = %d, want 0", n)
		}
		if _, err := a.store.Pool().Exec(context.Background(), `UPDATE users SET role='USER' WHERE id=$1`, user.ID); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("the preview and the stored closing date agree", func(t *testing.T) {
		res, result := sendJSON(t, a, http.MethodGet, "/api/v2/users/me/retirement/preview", userToken, "")
		if res.Code != http.StatusOK {
			t.Fatalf("preview = %d: %s", res.Code, res.Body.String())
		}
		preview, _ := result["preview"].(map[string]any)
		previewed, err := time.Parse(time.RFC3339Nano, preview["closesAt"].(string))
		if err != nil {
			t.Fatal(err)
		}
		_, retired := sendJSON(t, a, http.MethodPost, "/api/v2/users/me/retirement", userToken, `{"acknowledged":true}`)
		stored, err := time.Parse(time.RFC3339Nano, retired["closesAt"].(string))
		if err != nil {
			t.Fatal(err)
		}
		if d := stored.Sub(previewed); d > time.Minute || d < -time.Minute {
			t.Fatalf("preview %s and stored %s differ", previewed, stored)
		}
	})

	t.Run("a retired account cannot edit its profile or fetch a storage token", func(t *testing.T) {
		if res, _ := sendJSON(t, a, http.MethodPost, "/auth/profile", userToken, `{"name":"Changed"}`); res.Code != http.StatusForbidden {
			t.Fatalf("profile write while retired = %d, want 403: %s", res.Code, res.Body.String())
		}
		if res, _ := sendJSON(t, a, http.MethodPost, "/auth/storage-token", userToken, ""); res.Code != http.StatusForbidden {
			t.Fatalf("storage token while retired = %d, want 403", res.Code)
		}
		if res, _ := sendJSON(t, a, http.MethodGet, "/auth/profile", userToken, ""); res.Code != http.StatusOK {
			t.Fatalf("profile read while retired = %d, want 200", res.Code)
		}
	})
}
