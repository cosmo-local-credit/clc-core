package api

import (
	"context"
	"crypto/ed25519"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/require"
)

type (
	sessionLookupFixture struct {
		store.Store

		lookup func(context.Context, uint64) (store.UserAccess, error)
	}
)

func (s sessionLookupFixture) GetUserAccessByID(ctx context.Context, id uint64) (store.UserAccess, error) {
	return s.lookup(ctx, id)
}

func recoveryRequest(a *API, ctx context.Context, method, path, token, origin, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(ctx, method, path, nil)
	if token != "" {
		req.AddCookie(&http.Cookie{Name: userAuthCookieName, Value: token})
	}
	if bearer != "" {
		req.Header.Set(echo.HeaderAuthorization, "Bearer "+bearer)
	}
	req.Header.Set(echo.HeaderOrigin, origin)
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, req)
	return res
}

func requireRetryableSessionFailure(t *testing.T, res *httptest.ResponseRecorder) {
	t.Helper()
	require.Equal(t, http.StatusServiceUnavailable, res.Code, res.Body.String())
	require.Equal(t, "private, no-store", res.Header().Get(echo.HeaderCacheControl))
	require.Equal(t, "5", res.Header().Get("Retry-After"))
	require.Empty(t, res.Result().Cookies())
}

func TestSessionRecoveryWhenDatabaseVerificationFailsDB(t *testing.T) {
	a, token := newAdminAPI(t)
	ctx := t.Context()
	req := func(ctx context.Context) *httptest.ResponseRecorder {
		return recoveryRequest(a, ctx, http.MethodGet, "/auth/session", token, "https://admin.example", "")
	}

	ready := req(ctx)
	require.Equal(t, http.StatusOK, ready.Code, ready.Body.String())
	require.Equal(t, "private, no-store", ready.Header().Get(echo.HeaderCacheControl))

	tx, err := a.store.Pool().Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(ctx)) })
	_, err = tx.Exec(ctx, "LOCK TABLE users IN ACCESS EXCLUSIVE MODE")
	require.NoError(t, err)

	bounded, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	unavailable := req(bounded)
	requireRetryableSessionFailure(t, unavailable)
	require.Equal(t, "https://admin.example", unavailable.Header().Get(echo.HeaderAccessControlAllowOrigin))
	require.Equal(t, "Retry-After", unavailable.Header().Get("Access-Control-Expose-Headers"))
	require.NoError(t, tx.Rollback(ctx))

	recovered := req(ctx)
	require.Equal(t, http.StatusOK, recovered.Code, recovered.Body.String())
	require.JSONEq(t, ready.Body.String(), recovered.Body.String())
	require.Empty(t, recovered.Result().Cookies())
}

func TestSessionVerificationPreservesAccessBoundaries(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	const allowedOrigin = "https://app.example"
	const address = "0x1111111111111111111111111111111111111111"
	const accessPath = "/fixture/user-or-service"

	a := New(APIOpts{Logg: slog.New(slog.DiscardHandler), CORS: []string{allowedOrigin}, SigningKey: priv, VerifyingKey: pub})
	downstream := 0
	a.router.GET(accessPath, func(c *echo.Context) error {
		downstream++
		callerType, _ := c.Get("callerType").(string)
		return c.String(http.StatusOK, callerType)
	}, a.flexibleAuthMiddleware())
	token := mintTokenForUser(t, priv, 42, address, "USER")
	adminToken := mintTokenForUser(t, priv, 42, address, "ADMIN")
	serviceToken := offeringServiceToken(t, priv)
	storeFailure := errors.New("fixture database unavailable")
	approved := store.UserAccess{EthereumAddress: address, Role: "ADMIN", Status: "APPROVED"}
	failLookup := func(context.Context, uint64) (store.UserAccess, error) { return store.UserAccess{}, storeFailure }
	req := func(method, path, cookie, origin, bearer string) *httptest.ResponseRecorder {
		return recoveryRequest(a, t.Context(), method, path, cookie, origin, bearer)
	}

	t.Run("temporary lookup failure blocks protected reads before the handler", func(t *testing.T) {
		a.store = sessionLookupFixture{lookup: failLookup}
		for _, path := range []string{
			accessPath,
			"/api/v2/vouchers?nearby=true",
			"/api/v2/market-map/facets?creditDepth=1",
		} {
			requireRetryableSessionFailure(t, req(http.MethodGet, path, token, allowedOrigin, ""))
		}
		require.Zero(t, downstream)
	})

	t.Run("genuine invalid credential remains unauthenticated", func(t *testing.T) {
		a.store = sessionLookupFixture{lookup: func(context.Context, uint64) (store.UserAccess, error) {
			return store.UserAccess{}, pgx.ErrNoRows
		}}
		require.Equal(t, http.StatusUnauthorized, req(http.MethodGet, accessPath, token, allowedOrigin, "").Code)
		stale := req(http.MethodGet, "/auth/session", token, allowedOrigin, "")
		require.Equal(t, http.StatusNoContent, stale.Code)
		require.Equal(t, "private, no-store", stale.Header().Get(echo.HeaderCacheControl))
		cookies := stale.Result().Cookies()
		require.Len(t, cookies, 1)
		require.Equal(t, userAuthCookieName, cookies[0].Name)
		require.Negative(t, cookies[0].MaxAge)
	})

	t.Run("public fallback, write failure, and service precedence are unchanged", func(t *testing.T) {
		a.store = sessionLookupFixture{lookup: failLookup}
		public := req(http.MethodGet, "/api/v2/vouchers?sort=nearest&bbox=not-a-box", token, allowedOrigin, "")
		require.Equal(t, http.StatusBadRequest, public.Code, public.Body.String())
		write := req(http.MethodPost, "/api/v2/conversations", token, allowedOrigin, "")
		require.Equal(t, http.StatusUnauthorized, write.Code, write.Body.String())
		shared := req(http.MethodGet, accessPath, token, allowedOrigin, serviceToken)
		require.Equal(t, http.StatusOK, shared.Code, shared.Body.String())
		require.Equal(t, "service", shared.Body.String())
		walletOnly := req(http.MethodGet, "/api/v2/wants/1/contact", token, allowedOrigin, serviceToken)
		require.Equal(t, http.StatusForbidden, walletOnly.Code, walletOnly.Body.String())
		cookieOnly := req(http.MethodGet, "/auth/session", "", allowedOrigin, serviceToken)
		require.Equal(t, http.StatusNoContent, cookieOnly.Code)
		require.Equal(t, "private, no-store", cookieOnly.Header().Get(echo.HeaderCacheControl))
	})

	t.Run("lost privileged revalidation blocks the operator handler", func(t *testing.T) {
		lookups := 0
		a.store = sessionLookupFixture{lookup: func(context.Context, uint64) (store.UserAccess, error) {
			lookups++
			if lookups == 1 {
				return approved, nil
			}
			return store.UserAccess{}, storeFailure
		}}
		requireRetryableSessionFailure(t, req(http.MethodGet, "/api/v2/admin/users", adminToken, allowedOrigin, ""))
		require.Equal(t, 2, lookups)
	})

	t.Run("CORS does not expose headers outside the allowlist", func(t *testing.T) {
		a.store = sessionLookupFixture{lookup: failLookup}
		res := req(http.MethodGet, "/auth/session", token, "https://unlisted.example", "")
		requireRetryableSessionFailure(t, res)
		require.Empty(t, res.Header().Get(echo.HeaderAccessControlAllowOrigin))
		require.Empty(t, res.Header().Get("Access-Control-Expose-Headers"))
	})
}
