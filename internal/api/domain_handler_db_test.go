package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type (
	domainAPIFixture struct {
		a                             *API
		user                          store.User
		token                         string
		kosta, other, pending, global uint64
		host, otherHost, pendingHost  string
	}
)

// Real routes and committed fixtures; use only a disposable CORE_TEST_DSN.
// Restore the global host after each test, and run packages serially (-p 1).
func newDomainAPIFixture(t *testing.T) domainAPIFixture {
	t.Helper()
	a, _ := newOfferingAPI(t)
	t.Cleanup(a.store.Pool().Close)
	ctx := context.Background()
	tx, err := a.store.Pool().Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	global, err := a.store.GetGlobalDomain(ctx, tx)
	require.NoError(t, err)
	var oldStatus string
	var oldVerified any
	require.NoError(t, tx.QueryRow(ctx, `SELECT status, verified_at FROM domain_hosts WHERE domain_id = $1`, global.ID).Scan(&oldStatus, &oldVerified))
	_, err = tx.Exec(ctx, `UPDATE domain_hosts SET status = 'active', verified_at = CURRENT_TIMESTAMP WHERE domain_id = $1`, global.ID)
	require.NoError(t, err)
	marker := strings.ReplaceAll(uuid.NewString(), "-", "")
	user, err := a.store.InsertUser(ctx, tx, store.User{EthereumAddress: common.HexToAddress("0x" + marker + "12345678").Hex(), Name: "Before", Email: marker + "@example.test", Role: "USER", Status: "APPROVED", MaxPools: 10, MaxTokens: 20})
	require.NoError(t, err)
	f := domainAPIFixture{a: a, user: user, global: global.ID, host: "kosta-" + marker + ".example", otherHost: "other-" + marker + ".example", pendingHost: "pending-" + marker + ".example"}
	for i, id := range []*uint64{&f.kosta, &f.other, &f.pending} {
		slug := fmt.Sprintf("domain-api-%d-%s", i, marker)
		require.NoError(t, tx.QueryRow(ctx, `INSERT INTO domains (slug, name) VALUES ($1, $1) RETURNING id`, slug).Scan(id))
		host := []string{f.host, f.otherHost, f.pendingHost}[i]
		if i == 2 {
			_, err = tx.Exec(ctx, `INSERT INTO domain_hosts (domain_id, hostname) VALUES ($1, $2)`, *id, host)
		} else {
			_, err = tx.Exec(ctx, `INSERT INTO domain_hosts (domain_id, hostname, status, verified_at) VALUES ($1, $2, 'active', CURRENT_TIMESTAMP)`, *id, host)
		}
		require.NoError(t, err)
	}
	f.token, err = a.generateUserToken(user)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
	t.Cleanup(func() {
		err := pgx.BeginFunc(ctx, a.store.Pool(), func(cleanup pgx.Tx) error {
			for _, s := range []struct {
				sql  string
				args []any
			}{
				{`DELETE FROM domain_audit_events WHERE actor_key = $1`, []any{fmt.Sprintf("user:%d", user.ID)}},
				{`DELETE FROM user_domains WHERE user_id = $1`, []any{user.ID}},
				{`DELETE FROM users WHERE id = $1`, []any{user.ID}},
				{`DELETE FROM domain_hosts WHERE domain_id IN ($1, $2, $3)`, []any{f.kosta, f.other, f.pending}},
				{`DELETE FROM domains WHERE id IN ($1, $2, $3)`, []any{f.kosta, f.other, f.pending}},
				{`UPDATE domain_hosts SET status = $2, verified_at = $3 WHERE domain_id = $1`, []any{global.ID, oldStatus, oldVerified}},
			} {
				if _, err := cleanup.Exec(ctx, s.sql, s.args...); err != nil {
					return err
				}
			}
			return nil
		})
		assert.NoError(t, err)
	})
	return f
}

func (f domainAPIFixture) request(t *testing.T, method, path, body, origin string, domainID uint64, revision string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if f.token != "" {
		req.Header.Set("Authorization", "Bearer "+f.token)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if domainID != 0 {
		req.Header.Set(domainIDHeader, strconv.FormatUint(domainID, 10))
	}
	if revision != "" {
		req.Header.Set("If-Match", revision)
	}
	res := httptest.NewRecorder()
	f.a.router.ServeHTTP(res, req)
	var response struct {
		Result map[string]any `json:"result"`
	}
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &response), res.Body.String())
	return res, response.Result
}

func (f domainAPIFixture) onboard(t *testing.T) {
	t.Helper()
	body := fmt.Sprintf(`{"domainOnboarding":true,"name":"Ana","email":%q}`, f.user.Email)
	res, result := f.request(t, http.MethodPost, "/auth/profile", body, "https://"+f.host, f.kosta, `"0"`)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	assert.EqualValues(t, f.kosta, result["registrationDomainId"])
	assert.Equal(t, `"1"`, res.Header().Get("ETag"))
}

func (f domainAPIFixture) state(t *testing.T) store.UserDomains {
	t.Helper()
	tx, err := f.a.store.Pool().Begin(context.Background())
	require.NoError(t, err)
	defer tx.Rollback(context.Background())
	state, err := f.a.store.GetUserDomains(context.Background(), tx, f.user.ID)
	require.NoError(t, err)
	return state
}

func TestDomainAPIOnboardingJoinAndFallback(t *testing.T) {
	f := newDomainAPIFixture(t)
	res, result := f.request(t, http.MethodGet, "/api/v2/users/me/domains", "", "", 0, "")
	require.Equal(t, 200, res.Code, res.Body.String())
	assert.Nil(t, result["registrationDomainId"])
	assert.Empty(t, result["memberships"])
	assert.Equal(t, `"0"`, res.Header().Get("ETag"))
	f.onboard(t)

	// A current-revision onboarding retry cannot replace the saved profile.
	res, _ = f.request(t, http.MethodPost, "/auth/profile", `{"domainOnboarding":true,"name":"Overwrite"}`, "https://"+f.host, f.kosta, `"1"`)
	require.Equal(t, 200, res.Code, res.Body.String())
	assert.Equal(t, `"1"`, res.Header().Get("ETag"))
	var name string
	require.NoError(t, f.a.store.Pool().QueryRow(context.Background(), `SELECT name FROM users WHERE id = $1`, f.user.ID).Scan(&name))
	assert.Equal(t, "Ana", name)

	otherPath := fmt.Sprintf("/api/v2/users/me/domains/%d", f.other)
	res, _ = f.request(t, http.MethodPut, otherPath, "", "https://"+f.otherHost, f.other, `"1"`)
	require.Equal(t, 200, res.Code, res.Body.String())
	assert.Equal(t, `"2"`, res.Header().Get("ETag"))
	res, _ = f.request(t, http.MethodPut, otherPath, "", "https://"+f.otherHost, f.other, `"2"`)
	require.Equal(t, 200, res.Code, res.Body.String())
	assert.Equal(t, `"2"`, res.Header().Get("ETag"))

	// Leave Kosta from another active space's account-management screen.
	path := fmt.Sprintf("/api/v2/users/me/domains/%d", f.kosta)
	res, result = f.request(t, http.MethodDelete, path, "", "https://"+f.otherHost, f.other, `"2"`)
	require.Equal(t, 200, res.Code, res.Body.String())
	assert.Equal(t, true, result["globalRequired"])
	assert.Equal(t, "https://sarafu.network", result["continueAt"])
	assert.EqualValues(t, f.kosta, result["deactivatedDomainId"])
	assert.Equal(t, `"3"`, res.Header().Get("ETag"))
	assert.Equal(t, "no-store", res.Header().Get("Cache-Control"))
	state := f.state(t)
	statuses := make(map[uint64]string)
	for _, m := range state.Memberships {
		statuses[m.DomainID] = m.Status
	}
	assert.Equal(t, map[uint64]string{f.kosta: "left", f.other: "active", f.global: "active"}, statuses)
	res, _ = f.request(t, http.MethodDelete, path, "", "https://"+f.otherHost, f.other, `"3"`)
	require.Equal(t, 200, res.Code, res.Body.String())
	assert.Equal(t, state, f.state(t), "retry preserves timestamps and revision")
	res, _ = f.request(t, http.MethodDelete, fmt.Sprintf("/api/v2/users/me/domains/%d", f.global), "", "https://"+f.otherHost, f.other, `"3"`)
	assert.Equal(t, 409, res.Code, res.Body.String())
	assert.Contains(t, res.Body.String(), "GLOBAL_DOMAIN_REQUIRED")
}

func TestDomainAPIRejectsInvalidContextAndStaleRevision(t *testing.T) {
	f := newDomainAPIFixture(t)
	f.onboard(t)
	before := f.state(t)
	path := fmt.Sprintf("/api/v2/users/me/domains/%d", f.other)
	for _, tc := range []struct {
		origin   string
		id       uint64
		revision string
		status   int
	}{
		{"", f.other, `"1"`, 400},
		{"https://" + f.otherHost, 0, `"1"`, 400},
		{"https://evil.example", f.other, `"1"`, 403},
		{"https://" + f.host, f.other, `"1"`, 403},
		{"https://" + f.host, f.kosta, `"1"`, 403},
		{"https://" + f.pendingHost, f.pending, `"1"`, 403},
		{"https://" + f.otherHost, f.other, "", 400},
		{"https://" + f.otherHost, f.other, `"0"`, 409},
	} {
		res, _ := f.request(t, http.MethodPut, path, "", tc.origin, tc.id, tc.revision)
		assert.Equal(t, tc.status, res.Code, res.Body.String())
		assert.Equal(t, before, f.state(t))
	}
	// Onboarding cannot switch registration provenance.
	res, _ := f.request(t, http.MethodPost, "/auth/profile", `{"domainOnboarding":true}`, "https://"+f.otherHost, f.other, `"1"`)
	assert.Equal(t, 409, res.Code, res.Body.String())
	assert.Contains(t, res.Body.String(), "DOMAIN_REGISTRATION_CONFLICT")
}

func TestDomainAPIFallbackFailureIsAtomic(t *testing.T) {
	f := newDomainAPIFixture(t)
	f.onboard(t)
	before := f.state(t)
	_, err := f.a.store.Pool().Exec(context.Background(), `UPDATE domain_hosts SET status = 'disabled' WHERE domain_id = $1`, f.global)
	require.NoError(t, err)
	res, _ := f.request(t, http.MethodDelete, fmt.Sprintf("/api/v2/users/me/domains/%d", f.kosta), "", "https://"+f.host, f.kosta, `"1"`)
	assert.Equal(t, 503, res.Code, res.Body.String())
	assert.Equal(t, before, f.state(t))
	var count int
	require.NoError(t, f.a.store.Pool().QueryRow(context.Background(), `SELECT count(*) FROM domain_audit_events WHERE actor_key = $1`, fmt.Sprintf("user:%d", f.user.ID)).Scan(&count))
	assert.Equal(t, 2, count, "only the onboarding registration and join audit remain")
}

func TestDomainAPIDiscoveryAndCORSReadiness(t *testing.T) {
	f := newDomainAPIFixture(t)
	f.token = ""
	preflight := httptest.NewRequest(http.MethodOptions, "/api/v2/domains/resolve", nil)
	preflight.Header.Set("Origin", "https://"+f.host)
	preflight.Header.Set("Access-Control-Request-Method", http.MethodGet)
	permission := httptest.NewRecorder()
	f.a.router.ServeHTTP(permission, preflight)
	require.Equal(t, http.StatusNoContent, permission.Code)
	assert.Equal(t, "https://"+f.host, permission.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "300", permission.Header().Get("Access-Control-Max-Age"))
	res, _ := f.request(t, http.MethodGet, "/api/v2/domains/resolve?hostname="+f.host, "", "https://"+f.host, 0, "")
	require.Equal(t, 200, res.Code, res.Body.String())
	assert.Equal(t, "https://"+f.host, res.Header().Get("Access-Control-Allow-Origin"))
	for _, host := range []string{f.pendingHost, "unknown.example"} {
		res, _ = f.request(t, http.MethodGet, "/api/v2/domains/resolve?hostname="+host, "", "", 0, "")
		assert.Equal(t, 404, res.Code, res.Body.String())
	}
	res, result := f.request(t, http.MethodGet, fmt.Sprintf("/api/v2/domains?after=%d", f.kosta-1), "", "", 0, "")
	require.Equal(t, 200, res.Code, res.Body.String())
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), f.host)
	assert.Contains(t, string(encoded), f.otherHost)
	assert.NotContains(t, string(encoded), f.pendingHost)
	assert.NotContains(t, string(encoded), f.user.Email)
	_, err = f.a.store.Pool().Exec(context.Background(), `UPDATE domain_hosts SET status = 'disabled' WHERE domain_id = $1`, f.kosta)
	require.NoError(t, err)
	// Skip a new preflight, as a browser with cached permission would. The actual
	// request must still lose its CORS allowance and fail hostname resolution.
	res, _ = f.request(t, http.MethodGet, "/api/v2/domains/resolve?hostname="+f.host, "", "https://"+f.host, 0, "")
	assert.Equal(t, 404, res.Code, res.Body.String())
	assert.Empty(t, res.Header().Get("Access-Control-Allow-Origin"))
}

func TestDomainAPIRejectsRestrictedAccountAndForgedIdentity(t *testing.T) {
	f := newDomainAPIFixture(t)
	f.onboard(t)
	originalToken := f.token
	for _, status := range []string{"PENDING", "REJECTED"} {
		_, err := f.a.store.Pool().Exec(context.Background(), `UPDATE users SET status = $2 WHERE id = $1`, f.user.ID, status)
		require.NoError(t, err)
		res, _ := f.request(t, http.MethodGet, "/api/v2/users/me/domains", "", "", 0, "")
		assert.Equal(t, 403, res.Code, res.Body.String())
	}
	_, err := f.a.store.Pool().Exec(context.Background(), `UPDATE users SET status = 'APPROVED' WHERE id = $1`, f.user.ID)
	require.NoError(t, err)
	forged := f.user
	forged.EthereumAddress = "0x1111111111111111111111111111111111111111"
	forged.Role = "SUPERADMIN"
	f.token, err = f.a.generateUserToken(forged)
	require.NoError(t, err)
	res, _ := f.request(t, http.MethodGet, "/api/v2/users/me/domains", "", "", 0, "")
	assert.Equal(t, 403, res.Code, res.Body.String())
	f.token = originalToken
	res, _ = f.request(t, http.MethodGet, "/api/v2/admin/users", "", "", 0, "")
	assert.Equal(t, 403, res.Code, "space membership does not grant admin access")
}

func TestDomainAPIProfileOnboardingFailureAndLegacyUpdate(t *testing.T) {
	f := newDomainAPIFixture(t)
	_, err := f.a.store.Pool().Exec(context.Background(), `UPDATE domain_hosts SET status = 'disabled' WHERE domain_id = $1`, f.global)
	require.NoError(t, err)
	res, _ := f.request(t, http.MethodPost, "/auth/profile", `{"domainOnboarding":true,"name":"Must roll back"}`, "https://"+f.host, f.kosta, `"0"`)
	assert.Equal(t, 503, res.Code, res.Body.String())
	state := f.state(t)
	assert.Nil(t, state.RegistrationDomainID)
	assert.Zero(t, state.DomainRevision)
	assert.Empty(t, state.Memberships)
	var name string
	require.NoError(t, f.a.store.Pool().QueryRow(context.Background(), `SELECT name FROM users WHERE id = $1`, f.user.ID).Scan(&name))
	assert.Equal(t, "Before", name)
	// Ordinary profile updates are still available without domain onboarding.
	body := fmt.Sprintf(`{"name":"Plain update","email":%q}`, f.user.Email)
	res, _ = f.request(t, http.MethodPost, "/auth/profile", body, "", 0, "")
	require.Equal(t, 200, res.Code, res.Body.String())
	assert.Equal(t, state, f.state(t))
}
