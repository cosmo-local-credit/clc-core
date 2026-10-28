package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
)

type (
	availabilityField struct {
		Available bool `json:"available"`
	}
)

func TestIdentityAvailabilityIsPublicAndRateLimited(t *testing.T) {
	a := newTestRouter(t)
	send := func(remote, rawQuery string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v2/identity/availability?"+rawQuery, nil)
		req.RemoteAddr = remote
		res := httptest.NewRecorder()
		a.router.ServeHTTP(res, req)
		return res
	}

	for range marketRateBurst {
		res := send("198.51.100.77:1234", "")
		if res.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 without authentication: %s", res.Code, res.Body.String())
		}
		var body apiresp.ErrResponse
		if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body.ErrCode != apiresp.ErrCodeValidationFailed {
			t.Fatalf("code = %q, want %q", body.ErrCode, apiresp.ErrCodeValidationFailed)
		}
	}

	limited := send("198.51.100.77:1234", "")
	if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") == "" {
		t.Fatalf("expected 429 with Retry-After, got %d: %s", limited.Code, limited.Body.String())
	}
	if res := send("198.51.100.78:1234", "email=not-an-email"); res.Code != http.StatusBadRequest {
		t.Fatalf("another client was limited: status %d", res.Code)
	}
}

func TestIdentityAvailabilityRejectsInvalidFields(t *testing.T) {
	a := newTestRouter(t)
	cases := []string{
		"",
		"email=",
		"phone=",
		"username=",
		"email=not-an-email",
		"phone=0112283384",
		"phone=%2B012345678",
		"username=" + url.QueryEscape(strings.Repeat("a", 81)),
	}
	for _, rawQuery := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/v2/identity/availability?"+rawQuery, nil)
		req.RemoteAddr = "198.51.100.79:1234"
		res := httptest.NewRecorder()
		a.router.ServeHTTP(res, req)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("%q: status = %d, want 400: %s", rawQuery, res.Code, res.Body.String())
		}
	}
}

func TestIdentityAvailabilityReportsTakenEmailPhoneAndUsername(t *testing.T) {
	a, _ := newAdminAPI(t)
	ctx := context.Background()
	address := "0x" + strings.Repeat("c", 40)
	email := "zqavail-aude@example.test"
	phone := "+254700009184"
	username := "Aude-Péronne Zqavail"
	poolName := "Aude-Péronne Pool Zqavail"
	poolKey := "42220:0x" + strings.Repeat("d", 40)

	t.Cleanup(func() {
		_, _ = a.store.Pool().Exec(context.Background(), `DELETE FROM users WHERE blockchain_address = $1`, address)
		_, _ = a.store.Pool().Exec(context.Background(), `DELETE FROM identity_claims WHERE owner_key = $1`, poolKey)
	})
	if _, err := a.store.Pool().Exec(ctx, `DELETE FROM users WHERE blockchain_address = $1 OR email = $2 OR phone = $3`, address, email, phone); err != nil {
		t.Fatalf("clear previous user: %v", err)
	}
	if _, err := a.store.Pool().Exec(ctx, `DELETE FROM identity_claims WHERE owner_key = $1 OR name = normalize_identity_name($2)`, poolKey, poolName); err != nil {
		t.Fatalf("clear previous claim: %v", err)
	}
	if _, err := a.store.Pool().Exec(ctx, `
		INSERT INTO users (blockchain_address, name, email, phone, username, country, role, status)
		VALUES ($1, 'Aude Péronne', $2, $3, $4, 'KE', 'USER', 'APPROVED')`,
		address, email, phone, username); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := a.store.Pool().Exec(ctx, `
		INSERT INTO identity_claims (namespace, name, owner_kind, owner_key)
		VALUES ('recipient', normalize_identity_name($1), 'pool', $2)`, poolName, poolKey); err != nil {
		t.Fatalf("seed pool claim: %v", err)
	}

	takenQuery := url.Values{}
	takenQuery.Set("email", "Zqavail-Aude@Example.Test")
	takenQuery.Set("phone", phone)
	takenQuery.Set("username", "  AUDE-péronne   zqavail ")
	takenBody, taken := getAvailability(t, a, takenQuery)
	if taken["email"].Available || taken["phone"].Available || taken["username"].Available {
		t.Fatalf("taken identities reported free: %#v", taken)
	}
	lowerBody := strings.ToLower(takenBody)
	if strings.Contains(lowerBody, email) || strings.Contains(takenBody, username) || strings.Contains(takenBody, phone) {
		t.Fatalf("response echoed an identity: %s", takenBody)
	}

	freeQuery := url.Values{}
	freeQuery.Set("email", "zqavail-free@example.test")
	freeQuery.Set("phone", "+254700009185")
	freeQuery.Set("username", "Aude-Péronne Zqavail 2")
	_, free := getAvailability(t, a, freeQuery)
	if !free["email"].Available || !free["phone"].Available || !free["username"].Available {
		t.Fatalf("free identities reported taken: %#v", free)
	}

	poolQuery := url.Values{}
	poolQuery.Set("username", poolName)
	_, pool := getAvailability(t, a, poolQuery)
	if _, ok := pool["email"]; ok {
		t.Fatalf("unasked email was returned: %#v", pool)
	}
	if pool["username"].Available {
		t.Fatalf("pool display name was reported as a free username: %#v", pool)
	}
}

func getAvailability(t *testing.T, a *API, query url.Values) (string, map[string]availabilityField) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v2/identity/availability?"+query.Encode(), nil)
	req.RemoteAddr = "198.51.100.80:1234"
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, req)
	raw := res.Body.String()
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.Code, raw)
	}
	if res.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", res.Header().Get("Cache-Control"))
	}
	var body struct {
		Ok     bool                         `json:"ok"`
		Result map[string]availabilityField `json:"result"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Ok {
		t.Fatalf("ok = false: %s", raw)
	}
	return raw, body.Result
}
