package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
)

const (
	roleTestMarker = "zq-admin-role-test"
)

func seedRoleTestUser(t *testing.T, a *API, address, role, status string) store.User {
	t.Helper()
	ctx := context.Background()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin role fixture: %v", err)
	}
	defer tx.Rollback(ctx)

	var user store.User
	row := tx.QueryRow(ctx, `
		INSERT INTO users (blockchain_address, name, email, role, status)
		VALUES ($1, $2, $3, $4::user_role, $5::user_status)
		RETURNING id, blockchain_address AS ethereum_address, username, name,
			COALESCE(email, '') AS email, COALESCE(phone, '') AS phone,
			COALESCE(country, '') AS country, preferred_currency, bio, website,
			location_name, geo_lat, geo_lng, avatar_url, role, status,
			max_pools, max_tokens, max_catalogs, max_offers, max_wants,
			created_at, updated_at`,
		address, roleTestMarker, strings.ToLower(address)+"@example.invalid", role, status)
	if err := row.Scan(
		&user.ID, &user.EthereumAddress, &user.Username, &user.Name, &user.Email,
		&user.Phone, &user.Country, &user.PreferredCurrency, &user.Bio, &user.Website,
		&user.LocationName, &user.GeoLat, &user.GeoLng, &user.AvatarURL, &user.Role,
		&user.Status, &user.MaxPools, &user.MaxTokens, &user.MaxCatalogs,
		&user.MaxOffers, &user.MaxWants, &user.CreatedAt, &user.UpdatedAt,
	); err != nil {
		t.Fatalf("insert role fixture: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit role fixture: %v", err)
	}
	return user
}

func cleanupRoleTestUsers(t *testing.T, a *API) {
	t.Helper()
	ctx := context.Background()
	if _, err := a.store.Pool().Exec(ctx, `DELETE FROM users WHERE name = $1`, roleTestMarker); err != nil {
		t.Errorf("cleanup role fixtures: %v", err)
	}
}

func setRoleDirectly(t *testing.T, a *API, id uint64, role string) store.User {
	t.Helper()
	ctx := context.Background()
	var user store.User
	row := a.store.Pool().QueryRow(ctx, `
		UPDATE users SET role = $2::user_role WHERE id = $1
		RETURNING id, blockchain_address AS ethereum_address, username, name,
			COALESCE(email, '') AS email, COALESCE(phone, '') AS phone,
			COALESCE(country, '') AS country, preferred_currency, bio, website,
			location_name, geo_lat, geo_lng, avatar_url, role, status,
			max_pools, max_tokens, max_catalogs, max_offers, max_wants,
			created_at, updated_at`, id, role)
	if err := row.Scan(
		&user.ID, &user.EthereumAddress, &user.Username, &user.Name, &user.Email,
		&user.Phone, &user.Country, &user.PreferredCurrency, &user.Bio, &user.Website,
		&user.LocationName, &user.GeoLat, &user.GeoLng, &user.AvatarURL, &user.Role,
		&user.Status, &user.MaxPools, &user.MaxTokens, &user.MaxCatalogs,
		&user.MaxOffers, &user.MaxWants, &user.CreatedAt, &user.UpdatedAt,
	); err != nil {
		t.Fatalf("set role directly: %v", err)
	}
	return user
}

func roleHTTP(t *testing.T, a *API, token, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: userAuthCookieName, Value: token})
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, req)
	return res
}

func TestAdminRoleManagementAndSessionRefreshDB(t *testing.T) {
	a, _ := newAdminAPI(t)
	cleanupRoleTestUsers(t, a)
	t.Cleanup(func() { cleanupRoleTestUsers(t, a) })

	superadmin := seedRoleTestUser(t, a, "0x6100000000000000000000000000000000000001", "SUPERADMIN", "APPROVED")
	admin := seedRoleTestUser(t, a, "0x6100000000000000000000000000000000000002", "ADMIN", "APPROVED")
	target := seedRoleTestUser(t, a, "0x6100000000000000000000000000000000000003", "USER", "APPROVED")
	pending := seedRoleTestUser(t, a, "0x6100000000000000000000000000000000000004", "USER", "PENDING")
	superTarget := seedRoleTestUser(t, a, "0x6100000000000000000000000000000000000005", "SUPERADMIN", "APPROVED")
	rejectedAdmin := seedRoleTestUser(t, a, "0x6100000000000000000000000000000000000006", "ADMIN", "REJECTED")

	superToken, err := a.generateUserToken(superadmin)
	if err != nil {
		t.Fatalf("sign superadmin token: %v", err)
	}
	adminToken, err := a.generateUserToken(admin)
	if err != nil {
		t.Fatalf("sign admin token: %v", err)
	}
	rejectedToken, err := a.generateUserToken(rejectedAdmin)
	if err != nil {
		t.Fatalf("sign rejected admin token: %v", err)
	}

	res := roleHTTP(t, a, adminToken, http.MethodGet, "/api/v2/admin/users?perPage=1", "")
	if res.Code != http.StatusOK {
		t.Fatalf("admin list access status/body = %d %s", res.Code, res.Body.String())
	}

	res = roleHTTP(t, a, superToken, http.MethodPut,
		"/api/v2/admin/users/"+target.EthereumAddress+"/role", `{"role":"ADMIN"}`)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"role":"ADMIN"`) {
		t.Fatalf("promote status/body = %d %s", res.Code, res.Body.String())
	}

	res = roleHTTP(t, a, superToken, http.MethodPut,
		"/api/v2/admin/users/"+target.EthereumAddress+"/role", `{"role":"USER"}`)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"role":"USER"`) {
		t.Fatalf("demote status/body = %d %s", res.Code, res.Body.String())
	}

	res = roleHTTP(t, a, superToken, http.MethodGet,
		"/api/v2/admin/users?perPage=100&status=APPROVED&roles=ADMIN&roles=SUPERADMIN", "")
	if res.Code != http.StatusOK {
		t.Fatalf("filtered admins status/body = %d %s", res.Code, res.Body.String())
	}
	if !strings.Contains(strings.ToLower(res.Body.String()), strings.ToLower(admin.EthereumAddress)) ||
		!strings.Contains(strings.ToLower(res.Body.String()), strings.ToLower(superTarget.EthereumAddress)) ||
		strings.Contains(strings.ToLower(res.Body.String()), strings.ToLower(target.EthereumAddress)) ||
		strings.Contains(strings.ToLower(res.Body.String()), strings.ToLower(rejectedAdmin.EthereumAddress)) {
		t.Fatalf("filtered admins returned the wrong membership: %s", res.Body.String())
	}

	res = roleHTTP(t, a, adminToken, http.MethodPut,
		"/api/v2/admin/users/"+target.EthereumAddress+"/role", `{"role":"ADMIN"}`)
	if res.Code != http.StatusForbidden {
		t.Fatalf("admin role mutation status = %d, want 403", res.Code)
	}

	res = roleHTTP(t, a, superToken, http.MethodPut,
		"/api/v2/admin/users/"+superTarget.EthereumAddress+"/role", `{"role":"USER"}`)
	if res.Code != http.StatusConflict {
		t.Fatalf("superadmin target status = %d, want 409", res.Code)
	}

	res = roleHTTP(t, a, superToken, http.MethodPut,
		"/api/v2/admin/users/"+target.EthereumAddress+"/role", `{"role":"SUPERADMIN"}`)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("unsupported role status = %d, want 400", res.Code)
	}

	res = roleHTTP(t, a, superToken, http.MethodPut,
		"/api/v2/admin/users/0x6100000000000000000000000000000000000099/role", `{"role":"ADMIN"}`)
	if res.Code != http.StatusNotFound {
		t.Fatalf("unknown target status = %d, want 404", res.Code)
	}

	res = roleHTTP(t, a, superToken, http.MethodPut,
		"/api/v2/admin/users/"+pending.EthereumAddress+"/role", `{"role":"ADMIN"}`)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"status":"PENDING"`) {
		t.Fatalf("pending promotion status/body = %d %s", res.Code, res.Body.String())
	}

	res = roleHTTP(t, a, rejectedToken, http.MethodGet, "/api/v2/admin/users?perPage=1", "")
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("rejected admin status = %d, want 401", res.Code)
	}

	staleAdminToken := adminToken
	setRoleDirectly(t, a, admin.ID, "USER")
	res = roleHTTP(t, a, staleAdminToken, http.MethodGet, "/api/v2/admin/users?perPage=1", "")
	if res.Code != http.StatusForbidden {
		t.Fatalf("demoted stale admin status = %d, want 403", res.Code)
	}

	staleUserToken, err := a.generateUserToken(target)
	if err != nil {
		t.Fatalf("sign stale user token: %v", err)
	}
	setRoleDirectly(t, a, target.ID, "ADMIN")
	res = roleHTTP(t, a, staleUserToken, http.MethodGet, "/auth/session", "")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"role":"ADMIN"`) {
		t.Fatalf("promoted session status/body = %d %s", res.Code, res.Body.String())
	}
	if len(res.Result().Cookies()) == 0 {
		t.Fatal("promoted session did not refresh its cookie")
	}

	currentAdmin := setRoleDirectly(t, a, target.ID, "ADMIN")
	stalePromotedToken, err := a.generateUserToken(currentAdmin)
	if err != nil {
		t.Fatalf("sign promoted token: %v", err)
	}
	setRoleDirectly(t, a, target.ID, "USER")
	res = roleHTTP(t, a, stalePromotedToken, http.MethodGet, "/auth/session", "")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"role":"USER"`) {
		t.Fatalf("demoted session status/body = %d %s", res.Code, res.Body.String())
	}
	if len(res.Result().Cookies()) == 0 {
		t.Fatal("demoted session did not refresh its cookie")
	}

}

func TestPrivilegedRoleRevalidationReleasesConnectionBeforeHandlerDB(t *testing.T) {
	a, _ := newAdminAPI(t)
	cleanupRoleTestUsers(t, a)
	t.Cleanup(func() { cleanupRoleTestUsers(t, a) })

	admin := seedRoleTestUser(
		t,
		a,
		"0x6100000000000000000000000000000000000010",
		"SUPERADMIN",
		"APPROVED",
	)
	token, err := a.generateUserToken(admin)
	if err != nil {
		t.Fatalf("sign superadmin token: %v", err)
	}

	// Leave exactly one connection available. The role check must release it
	// before resource-usage begins its own transaction. Keeping the middleware
	// transaction open until next returns deadlocks this request until the API's
	// deadline, which is the production Wallet regression this test guards.
	pool := a.store.Pool()
	held := make([]interface{ Release() }, 0, int(pool.Stat().MaxConns())-1)
	for range int(pool.Stat().MaxConns()) - 1 {
		conn, acquireErr := pool.Acquire(context.Background())
		if acquireErr != nil {
			t.Fatalf("reserve database connection: %v", acquireErr)
		}
		held = append(held, conn)
	}
	t.Cleanup(func() {
		for _, conn := range held {
			conn.Release()
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v2/users/me/resource-usage", nil).WithContext(ctx)
	req.AddCookie(&http.Cookie{Name: userAuthCookieName, Value: token})
	res := httptest.NewRecorder()
	started := time.Now()
	a.router.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("resource usage status/body = %d %s", res.Code, res.Body.String())
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("privileged request held the only connection for %s", elapsed)
	}
	if got, want := pool.Stat().AcquiredConns(), int32(len(held)); got != want {
		t.Fatalf("acquired connections after request = %d, want %d", got, want)
	}
}

func TestPrivilegedWalletReadsCompleteConcurrentlyDB(t *testing.T) {
	a, _ := newAdminAPI(t)
	cleanupRoleTestUsers(t, a)
	t.Cleanup(func() { cleanupRoleTestUsers(t, a) })

	admin := seedRoleTestUser(
		t,
		a,
		"0x6100000000000000000000000000000000000011",
		"SUPERADMIN",
		"APPROVED",
	)
	token, err := a.generateUserToken(admin)
	if err != nil {
		t.Fatalf("sign superadmin token: %v", err)
	}

	requests := []struct {
		path       string
		wantStatus int
	}{
		{"/api/v2/users/me/resource-usage", http.StatusOK},
		{"/api/v2/users/me/catalogs", http.StatusOK},
		{"/api/v2/users/me/wants", http.StatusOK},
		{"/api/v2/users/me/owned-pools", http.StatusOK},
		{"/api/v2/users/me/retirement-requests", http.StatusOK},
		// The isolated core database has no federation source, so account-token
		// reads return Not Found here. They still exercise privileged session
		// revalidation concurrently and must complete before the deadline.
		{"/api/v2/accounts/" + admin.EthereumAddress + "/tokens?perPage=20", http.StatusNotFound},
		{"/api/v2/accounts/" + admin.EthereumAddress + "/tokens/issued?perPage=100", http.StatusNotFound},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	responses := make([]*httptest.ResponseRecorder, len(requests))
	var wg sync.WaitGroup
	for i, request := range requests {
		wg.Go(func() {
			req := httptest.NewRequest(http.MethodGet, request.path, nil).WithContext(ctx)
			req.AddCookie(&http.Cookie{Name: userAuthCookieName, Value: token})
			responses[i] = httptest.NewRecorder()
			a.router.ServeHTTP(responses[i], req)
		})
	}
	wg.Wait()

	for i, response := range responses {
		if response.Code != requests[i].wantStatus {
			t.Errorf(
				"%s status/body = %d %s, want %d",
				requests[i].path,
				response.Code,
				response.Body.String(),
				requests[i].wantStatus,
			)
		}
	}
	if acquired := a.store.Pool().Stat().AcquiredConns(); acquired != 0 {
		t.Fatalf("acquired connections after concurrent Wallet reads = %d, want 0", acquired)
	}
}
