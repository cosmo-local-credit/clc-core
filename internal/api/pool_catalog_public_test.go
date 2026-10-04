package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
)

func newPoolCatalogTestAPI(t *testing.T) *API {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return New(APIOpts{
		Logg:         slog.New(slog.DiscardHandler),
		CORS:         []string{"https://app.example"},
		SigningKey:   priv,
		VerifyingKey: pub,
	})
}

func servePoolCatalog(a *API, method, path, remoteAddr string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = remoteAddr
	if cookie != nil {
		req.AddCookie(cookie)
	}
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, req)
	return res
}

// A protected registration answers 401 before the handler runs. Each path
// carries input the handler itself rejects, so any other status proves a
// signed-out request reached it. `/pools/:address` validates nothing before
// it reads the store, which is nil here, so it reaches the handler as a 500.
func TestPoolReadRoutesArePublic(t *testing.T) {
	a := newPoolCatalogTestAPI(t)

	for _, testCase := range []struct {
		path string
		want int
	}{
		{path: "/api/v2/pools?perPage=notanumber", want: http.StatusBadRequest},
		{path: "/api/v2/pools/not-an-address", want: http.StatusInternalServerError},
		{path: "/api/v2/pools/not-an-address/vouchers?perPage=20", want: http.StatusBadRequest},
		{path: "/api/v2/pools/not-an-address/offerings?perPage=20", want: http.StatusBadRequest},
	} {
		res := servePoolCatalog(a, http.MethodGet, testCase.path, "198.51.100.120:1234", nil)
		if res.Code != testCase.want {
			t.Fatalf("%s: status = %d, want public handler status %d", testCase.path, res.Code, testCase.want)
		}
	}
}

func TestPoolProfileWriteStillRequiresAuthentication(t *testing.T) {
	a := newPoolCatalogTestAPI(t)

	res := servePoolCatalog(a, http.MethodPut, "/api/v2/pools/not-an-address/profile", "198.51.100.121:1234", nil)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusUnauthorized)
	}
}

func TestSignedOutPoolReadsAreRateLimited(t *testing.T) {
	a := newPoolCatalogTestAPI(t)
	const path = "/api/v2/pools/not-an-address/vouchers?perPage=20"
	const remote = "198.51.100.122:1234"

	for i := range marketRateBurst {
		if res := servePoolCatalog(a, http.MethodGet, path, remote, nil); res.Code != http.StatusBadRequest {
			t.Fatalf("request %d: status = %d, want %d", i+1, res.Code, http.StatusBadRequest)
		}
	}

	res := servePoolCatalog(a, http.MethodGet, path, remote, nil)
	if res.Code != http.StatusTooManyRequests {
		t.Fatalf("request after burst: status = %d, want %d", res.Code, http.StatusTooManyRequests)
	}
	if res.Header().Get("Retry-After") == "" {
		t.Fatal("rate-limited pool response must include Retry-After")
	}
}

// Signed-in callers were never throttled on these routes, and the burst is
// sized for a signed-out visitor, not for the pool screens.
func TestSignedInPoolReadsAreNotRateLimited(t *testing.T) {
	a := newPoolCatalogTestAPI(t)
	cookie := sessionCookie(t, a)
	const path = "/api/v2/pools/not-an-address/vouchers?perPage=20"

	for i := range marketRateBurst * 2 {
		res := servePoolCatalog(a, http.MethodGet, path, "198.51.100.123:1234", cookie)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("request %d: status = %d, want %d", i+1, res.Code, http.StatusBadRequest)
		}
	}
}

// An expired or forged session must not lock a visitor out of a public route.
func TestPoolReadsServeAnInvalidSessionAsSignedOut(t *testing.T) {
	a := newPoolCatalogTestAPI(t)
	cookie := &http.Cookie{Name: userAuthCookieName, Value: "not-a-jwt"}

	res := servePoolCatalog(a, http.MethodGet, "/api/v2/pools/not-an-address/vouchers?perPage=20", "198.51.100.124:1234", cookie)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want public handler status %d", res.Code, http.StatusBadRequest)
	}
}

// The Market tab's burst is enumerated against its own requests, so pool
// lookups must spend from a separate budget.
func TestPoolReadsDoNotSpendTheMarketBudget(t *testing.T) {
	a := newPoolCatalogTestAPI(t)
	const remote = "198.51.100.125:1234"

	for range marketRateBurst {
		servePoolCatalog(a, http.MethodGet, "/api/v2/vouchers/not-an-address/offerings?perPage=20", remote, nil)
	}
	if res := servePoolCatalog(a, http.MethodGet, "/api/v2/vouchers/not-an-address/offerings?perPage=20", remote, nil); res.Code != http.StatusTooManyRequests {
		t.Fatalf("market status after burst = %d, want %d", res.Code, http.StatusTooManyRequests)
	}

	res := servePoolCatalog(a, http.MethodGet, "/api/v2/pools/not-an-address/vouchers?perPage=20", remote, nil)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("pool status after market burst = %d, want %d", res.Code, http.StatusBadRequest)
	}
}

func TestPoolCatalogMiddlewareKeepsTheCallerContext(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := &API{
		signingKey:   priv,
		verifyingKey: pub,
		poolLimiter:  newMarketRateLimiter(marketRateLimit, marketRateBurst, time.Now),
	}

	var callerType, callerAddress any
	e := echo.New()
	e.GET("/pools", func(c *echo.Context) error {
		callerType = c.Get("callerType")
		callerAddress = c.Get("callerAddress")
		return c.NoContent(http.StatusNoContent)
	}, a.poolCatalogMiddleware())

	req := httptest.NewRequest(http.MethodGet, "/pools", nil)
	req.AddCookie(sessionCookie(t, a))
	res := httptest.NewRecorder()
	e.ServeHTTP(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusNoContent)
	}
	if callerType != "user" || callerAddress != "0x1111111111111111111111111111111111111111" {
		t.Fatalf("caller = %v %v, want the session's user", callerType, callerAddress)
	}
}
