package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/labstack/echo/v5"
)

func TestPoolVouchersRejectsInvalidAddressOnce(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v2/pools/not-an-address/vouchers?perPage=20", nil)
	res := httptest.NewRecorder()
	ctx := e.NewContext(req, res)
	ctx.SetPathValues(echo.PathValues{{Name: "address", Value: "not-an-address"}})

	a := &API{}
	if err := a.poolVouchersListHandler(ctx); err != nil {
		t.Fatalf("poolVouchersListHandler() error = %v", err)
	}
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusBadRequest)
	}

	var body apiresp.ErrResponse
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("expected exactly one JSON response: %v", err)
	}
	if body.ErrCode != apiresp.ErrCodeValidationFailed {
		t.Fatalf("code = %q, want %q", body.ErrCode, apiresp.ErrCodeValidationFailed)
	}
}

func TestVoucherOfferingsRouteIsPublicRateLimitedAndDoesNotCacheErrors(t *testing.T) {
	a := New(APIOpts{
		Logg: slog.New(slog.DiscardHandler),
		CORS: []string{"https://app.example"},
	})

	for i := range marketRateBurst {
		req := httptest.NewRequest(http.MethodGet, "/api/v2/vouchers/not-an-address/offerings?perPage=20", nil)
		req.RemoteAddr = "198.51.100.80:1234"
		res := httptest.NewRecorder()
		a.router.ServeHTTP(res, req)

		// A protected registration would return 401 before address validation.
		if res.Code != http.StatusBadRequest {
			t.Fatalf("request %d: status = %d, want public handler status %d", i+1, res.Code, http.StatusBadRequest)
		}
		if got := res.Header().Get("Cache-Control"); got != "" {
			t.Fatalf("request %d: error Cache-Control = %q, want empty", i+1, got)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v2/vouchers/not-an-address/offerings?perPage=20", nil)
	req.RemoteAddr = "198.51.100.80:1234"
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, req)
	if res.Code != http.StatusTooManyRequests {
		t.Fatalf("request after burst: status = %d, want %d", res.Code, http.StatusTooManyRequests)
	}
	if res.Header().Get("Retry-After") == "" {
		t.Fatal("rate-limited voucher offerings response must include Retry-After")
	}
}

func TestPublicCatalogServerErrorsAreNotCached(t *testing.T) {
	a := New(APIOpts{
		Logg: slog.New(slog.DiscardHandler),
		CORS: []string{"https://app.example"},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v2/vouchers?perPage=20", nil)
	req.RemoteAddr = "198.51.100.81:1234"
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, req)

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusInternalServerError)
	}
	if got := res.Header().Get("Cache-Control"); got != "" {
		t.Fatalf("server error Cache-Control = %q, want empty", got)
	}
}

// The pool offerings route is deliberately authenticated, unlike its
// voucher-scoped sibling: the pool screen requires a session, so this route sits
// on the API group with every other /api/v2/pools/* route.
func TestPoolOfferingsRouteRequiresAuthenticationAndIsNotPubliclyCached(t *testing.T) {
	a := New(APIOpts{
		Logg: slog.New(slog.DiscardHandler),
		CORS: []string{"https://app.example"},
	})

	// A malformed address would be a 400 from the handler itself, so a 401 here
	// proves the request never reached it.
	req := httptest.NewRequest(http.MethodGet, "/api/v2/pools/not-an-address/offerings?perPage=20", nil)
	req.RemoteAddr = "198.51.100.90:1234"
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, req)

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d for an unauthenticated pool request", res.Code, http.StatusUnauthorized)
	}
	if got := res.Header().Get("Cache-Control"); strings.Contains(got, "public") {
		t.Fatalf("Cache-Control = %q, want no public caching on an authenticated route", got)
	}
}
