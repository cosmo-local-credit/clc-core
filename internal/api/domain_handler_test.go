package api

import (
	"crypto/ed25519"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDomainHostnameNormalization(t *testing.T) {
	for raw, want := range map[string]string{"Kosta.Example.": "kosta.example", "bücher.example": "xn--bcher-kva.example", "sarafu.network": "sarafu.network"} {
		got, err := normalizeDomainHostname(raw)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
	for _, raw := range []string{"", "localhost", "127.0.0.1", "[::1]", "https://kosta.example", "kosta.example:443", "kosta.example/", "*.example", "user@kosta.example", "a..example", " kosta.example", "-a.example"} {
		_, err := normalizeDomainHostname(raw)
		assert.Error(t, err, raw)
	}
}

func TestDomainOriginValidation(t *testing.T) {
	for _, raw := range []string{"https://kosta.example", "https://kosta.example:443"} {
		host, err := domainOriginHostname(raw)
		require.NoError(t, err)
		assert.Equal(t, "kosta.example", host)
	}
	for _, raw := range []string{"", "null", "http://kosta.example", "https://kosta.example:8443", "https://kosta.example:", "https://user@kosta.example", "https://kosta.example/path", "https://kosta.example?", "https://kosta.example#", "https://kosta.example#fragment", "https://kosta.example https://other.example", "https://127.0.0.1"} {
		_, err := domainOriginHostname(raw)
		assert.Error(t, err, raw)
	}
}

func TestDomainRevisionHeader(t *testing.T) {
	e := echo.New()
	for raw, want := range map[string]uint64{`"0"`: 0, `"14"`: 14, `"9223372036854775807"`: 9223372036854775807} {
		req := httptest.NewRequest(http.MethodPut, "/", nil)
		req.Header.Set("If-Match", raw)
		revision, err := domainRevision(e.NewContext(req, httptest.NewRecorder()))
		require.NoError(t, err)
		assert.Equal(t, want, revision)
	}
	for _, raw := range []string{"", "0", "*", `W/"1"`, `"01"`, `"-1"`, `"+1"`, `"1", "2"`, `"9223372036854775808"`} {
		req := httptest.NewRequest(http.MethodPut, "/", nil)
		req.Header.Set("If-Match", raw)
		_, err := domainRevision(e.NewContext(req, httptest.NewRecorder()))
		assert.Error(t, err, raw)
	}
	req := httptest.NewRequest(http.MethodPut, "/", nil)
	req.Header.Add("If-Match", `"1"`)
	req.Header.Add("If-Match", `"2"`)
	_, err := domainRevision(e.NewContext(req, httptest.NewRecorder()))
	assert.Error(t, err)
}

func TestDomainRoutesRequireUserAuthentication(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	a := New(APIOpts{Logg: slog.New(slog.DiscardHandler), SigningKey: priv, VerifyingKey: pub})
	service, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, JWTCustomClaims{
		Service: true, PublicKey: "test-service", RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))},
	}).SignedString(priv)
	require.NoError(t, err)
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v2/users/me/domains"},
		{http.MethodPut, "/api/v2/users/me/domains/1"},
		{http.MethodDelete, "/api/v2/users/me/domains/1"},
		{http.MethodPost, "/auth/profile"},
	} {
		for _, token := range []string{"", service} {
			req := httptest.NewRequest(route.method, route.path, nil)
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			res := httptest.NewRecorder()
			a.router.ServeHTTP(res, req)
			want := http.StatusUnauthorized
			if token != "" {
				want = http.StatusForbidden
			}
			assert.Equal(t, want, res.Code, "%s %s: %s", route.method, route.path, res.Body.String())
		}
	}
}

func TestDomainErrorResponses(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{store.ErrDomainRevisionConflict, 409, "DOMAIN_REVISION_CONFLICT"},
		{store.ErrGlobalDomainRequired, 409, "GLOBAL_DOMAIN_REQUIRED"},
		{store.ErrDomainMembershipSuspended, 403, "E10"},
		{store.ErrDomainFallbackUnavailable, 503, "DOMAIN_FALLBACK_UNAVAILABLE"},
		{store.ErrDomainRepairRequired, 503, "DOMAIN_FALLBACK_UNAVAILABLE"},
		{store.ErrDomainUnavailable, 404, "DOMAIN_UNAVAILABLE"},
	} {
		res := httptest.NewRecorder()
		c := echo.New().NewContext(httptest.NewRequest(http.MethodPut, "/", nil), res)
		require.NoError(t, handleDomainError(c, tc.err))
		assert.Equal(t, tc.status, res.Code)
		assert.Contains(t, res.Body.String(), `"code":"`+tc.code+`"`)
	}
}

func TestDomainCORSHeaders(t *testing.T) {
	e := echo.New()
	e.Use(middleware.CORSWithConfig((&API{}).domainCORSConfig([]string{"https://kosta.example"})))
	e.PUT("/membership", func(c *echo.Context) error { return c.NoContent(http.StatusOK) })
	req := httptest.NewRequest(http.MethodOptions, "/membership", nil)
	req.Header.Set("Origin", "https://kosta.example")
	req.Header.Set("Access-Control-Request-Method", "PUT")
	req.Header.Set("Access-Control-Request-Headers", "X-CLC-Domain-ID,If-Match")
	res := httptest.NewRecorder()
	e.ServeHTTP(res, req)
	assert.Equal(t, "https://kosta.example", res.Header().Get("Access-Control-Allow-Origin"))
	assert.Contains(t, res.Header().Get("Access-Control-Allow-Headers"), domainIDHeader)
	assert.Contains(t, res.Header().Get("Access-Control-Allow-Headers"), "If-Match")
	assert.Equal(t, "300", res.Header().Get("Access-Control-Max-Age"))
	req = httptest.NewRequest(http.MethodPut, "/membership", nil)
	req.Header.Set("Origin", "https://kosta.example")
	res = httptest.NewRecorder()
	e.ServeHTTP(res, req)
	assert.Contains(t, res.Header().Get("Access-Control-Expose-Headers"), "ETag")
}
