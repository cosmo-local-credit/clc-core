package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/labstack/echo/v5"
)

func TestMarketPaginationUsesFiveHundredRowCap(t *testing.T) {
	for name, tc := range map[string]struct {
		requested uint64
		want      uint64
	}{
		"zero uses the endpoint default": {requested: 0, want: 500},
		"under the cap is preserved":     {requested: 120, want: 120},
		"at the cap is preserved":        {requested: 500, want: 500},
		"above the cap clamps":           {requested: 501, want: 500},
		"far above the cap clamps":       {requested: 100_000, want: 500},
	} {
		got := normalizePaginationWithMax(apiresp.CursorPaginationRequest{PerPage: tc.requested}, marketMaxPerPage)
		if got.PerPage != tc.want {
			t.Errorf("%s: perPage = %d, want %d", name, got.PerPage, tc.want)
		}
	}
}

func TestUnrelatedCatalogEndpointsKeepHundredRowCap(t *testing.T) {
	for name, tc := range map[string]struct {
		requested uint64
		want      uint64
	}{
		"zero uses the catalog default": {requested: 0, want: 100},
		"above the catalog cap clamps":  {requested: 500, want: 100},
	} {
		got := normalizePagination(apiresp.CursorPaginationRequest{PerPage: tc.requested})
		if got.PerPage != tc.want {
			t.Errorf("%s: perPage = %d, want %d", name, got.PerPage, tc.want)
		}
	}
}

func TestMarketPaginationKeepsCursorSemantics(t *testing.T) {
	first := normalizePaginationWithMax(apiresp.CursorPaginationRequest{}, marketMaxPerPage)
	if !first.FirstPage {
		t.Error("a request without a cursor must be the first page")
	}

	next := normalizePaginationWithMax(apiresp.CursorPaginationRequest{Cursor: 2212, Next: true}, marketMaxPerPage)
	if next.FirstPage {
		t.Error("a cursored next request must not be the first page")
	}
}

func TestParseMarketBoundsAcceptsAValidViewport(t *testing.T) {
	bounds, err := parseMarketBounds("36.6,-1.5,37.1,-1.1")
	if err != nil {
		t.Fatalf("parseMarketBounds() error = %v", err)
	}
	if bounds == nil {
		t.Fatal("expected bounds for a supplied bbox")
	}
	if bounds.MinLng != 36.6 || bounds.MinLat != -1.5 || bounds.MaxLng != 37.1 || bounds.MaxLat != -1.1 {
		t.Fatalf("bounds = %+v, want minLng,minLat,maxLng,maxLat order", *bounds)
	}
}

func TestParseMarketBoundsTreatsAbsentBboxAsUnbounded(t *testing.T) {
	bounds, err := parseMarketBounds("")
	if err != nil {
		t.Fatalf("parseMarketBounds() error = %v", err)
	}
	if bounds != nil {
		t.Fatalf("expected nil bounds for an absent bbox, got %+v", *bounds)
	}
}

func TestParseMarketBoundsRejectsMalformedInput(t *testing.T) {
	for name, raw := range map[string]string{
		"too few values":         "36.6,-1.5,37.1",
		"too many values":        "36.6,-1.5,37.1,-1.1,0",
		"non numeric":            "36.6,-1.5,37.1,south",
		"empty component":        "36.6,,37.1,-1.1",
		"latitude out of range":  "36.6,-91,37.1,-1.1",
		"longitude out of range": "-181,-1.5,37.1,-1.1",
		"inverted latitude":      "36.6,-1.1,37.1,-1.5",
		// A viewport crossing the antimeridian must be split by the caller.
		"antimeridian crossing": "179,-1.5,-179,-1.1",
		"not a number":          "NaN,-1.5,37.1,-1.1",
		"infinite":              "Inf,-1.5,37.1,-1.1",
	} {
		if _, err := parseMarketBounds(raw); err == nil {
			t.Errorf("expected %s (%q) to be rejected", name, raw)
		}
	}
}

// newTestRouter builds the real route table so these tests prove what the
// deployed router does, not what a hand-wired context does.
func newTestRouter(t *testing.T) *API {
	t.Helper()
	return New(APIOpts{
		Logg: slog.New(slog.DiscardHandler),
		CORS: []string{"https://app.example"},
	})
}

func TestVouchersRouteIsPublic(t *testing.T) {
	a := newTestRouter(t)

	// A deliberately invalid bbox proves the request reached the handler:
	// 400 means validation ran, 401 would mean auth middleware intercepted.
	req := httptest.NewRequest(http.MethodGet, "/api/v2/vouchers?bbox=not-a-box", nil)
	req.RemoteAddr = "198.51.100.20:1234"
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, req)

	if res.Code == http.StatusUnauthorized {
		t.Fatal("the market catalog must not require a JWT")
	}
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusBadRequest)
	}

	var body apiresp.ErrResponse
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("expected a JSON error response: %v", err)
	}
	if body.ErrCode != apiresp.ErrCodeValidationFailed {
		t.Fatalf("code = %q, want %q", body.ErrCode, apiresp.ErrCodeValidationFailed)
	}
}

func TestVoucherDetailRouteIsPublic(t *testing.T) {
	a := newTestRouter(t)

	// A deliberately invalid address proves the request reached the handler:
	// 400 means validation ran, 401 would mean auth middleware intercepted.
	// It also stops short of the store, so no database is needed here.
	req := httptest.NewRequest(http.MethodGet, "/api/v2/vouchers/not-an-address", nil)
	req.RemoteAddr = "198.51.100.50:1234"
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, req)

	if res.Code == http.StatusUnauthorized {
		t.Fatal("a shared voucher link must open without a JWT")
	}
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusBadRequest)
	}

	var body apiresp.ErrResponse
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("expected a JSON error response: %v", err)
	}
	if body.ErrCode != apiresp.ErrCodeValidationFailed {
		t.Fatalf("code = %q, want %q", body.ErrCode, apiresp.ErrCodeValidationFailed)
	}
}

func TestVouchersRouteRateLimitsPerClient(t *testing.T) {
	a := newTestRouter(t)

	send := func(remote string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v2/vouchers?bbox=not-a-box", nil)
		req.RemoteAddr = remote
		res := httptest.NewRecorder()
		a.router.ServeHTTP(res, req)
		return res
	}

	for i := range marketRateBurst {
		if res := send("198.51.100.30:1234"); res.Code == http.StatusTooManyRequests {
			t.Fatalf("request %d of the burst was limited", i+1)
		}
	}

	limited := send("198.51.100.30:1234")
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d after the burst", limited.Code, http.StatusTooManyRequests)
	}
	retryAfter := limited.Header().Get("Retry-After")
	if retryAfter == "" {
		t.Fatal("a 429 must carry Retry-After")
	}
	if seconds, err := strconv.Atoi(retryAfter); err != nil || seconds < 1 {
		t.Fatalf("Retry-After = %q, want whole seconds >= 1", retryAfter)
	}

	var body apiresp.ErrResponse
	if err := json.Unmarshal(limited.Body.Bytes(), &body); err != nil {
		t.Fatalf("expected a JSON error response: %v", err)
	}
	if body.ErrCode != apiresp.ErrCodeLimitExceeded {
		t.Fatalf("code = %q, want %q (rate/quota limit exceeded)", body.ErrCode, apiresp.ErrCodeLimitExceeded)
	}

	// A different client keeps its own budget.
	if res := send("198.51.100.31:1234"); res.Code == http.StatusTooManyRequests {
		t.Fatal("a second client must not inherit the first client's limit")
	}
}

// The Market tab spends its whole budget in one burst: every catalog page, a
// bounded viewport bootstrap, then one detail request per marker the user taps.
// Tapping is how the map is read, so a burst that runs out mid-exploration
// would 429 a first-time visitor for using the feature normally.
func TestMarketBurstCoversACatalogLoadThenMarkerTaps(t *testing.T) {
	const (
		client = "198.51.100.51:1234"
		// 2,181 geolocated vouchers at the 500-row cap.
		catalogPages = 5
		// One request per box; a viewport crossing the antimeridian splits.
		viewportBootstrap = 2
		markerTaps        = 20
	)

	a := newTestRouter(t)

	send := func(target string) int {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.RemoteAddr = client
		res := httptest.NewRecorder()
		a.router.ServeHTTP(res, req)
		return res.Code
	}

	for i := range catalogPages + viewportBootstrap {
		// Invalid bbox: the limiter runs before the handler, and a 400 keeps
		// this test off the database.
		if code := send("/api/v2/vouchers?bbox=not-a-box"); code == http.StatusTooManyRequests {
			t.Fatalf("catalog request %d of %d was rate limited", i+1, catalogPages+viewportBootstrap)
		}
	}

	for i := range markerTaps {
		if code := send("/api/v2/vouchers/not-an-address"); code == http.StatusTooManyRequests {
			t.Fatalf("marker tap %d of %d was rate limited after a catalog load", i+1, markerTaps)
		}
	}
}

func TestMarketCachePolicyIsPublicAndVariesByOrigin(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v2/vouchers", nil)
	res := httptest.NewRecorder()
	ctx := e.NewContext(req, res)
	// The CORS middleware has already varied on Origin by the time the
	// handler runs; merging must not discard an existing value.
	ctx.Response().Header().Set("Vary", "Accept-Encoding")

	setMarketCacheHeaders(ctx)

	if got := ctx.Response().Header().Get("Cache-Control"); got != "public, max-age=60, stale-while-revalidate=300" {
		t.Fatalf("Cache-Control = %q", got)
	}
	vary := ctx.Response().Header().Values("Vary")
	joined := strings.Join(vary, ", ")
	if !strings.Contains(joined, "Accept-Encoding") {
		t.Fatalf("Vary = %q, want the pre-existing value preserved", joined)
	}
	if !strings.Contains(joined, "Origin") {
		t.Fatalf("Vary = %q, want Origin merged in", joined)
	}
}

func TestMarketCachePolicyDoesNotDuplicateOrigin(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v2/vouchers", nil)
	res := httptest.NewRecorder()
	ctx := e.NewContext(req, res)
	ctx.Response().Header().Set("Vary", "Origin")

	setMarketCacheHeaders(ctx)

	joined := strings.Join(ctx.Response().Header().Values("Vary"), ", ")
	if strings.Count(joined, "Origin") != 1 {
		t.Fatalf("Vary = %q, want Origin exactly once", joined)
	}
}
