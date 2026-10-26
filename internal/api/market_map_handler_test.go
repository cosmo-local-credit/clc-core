package api

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
)

func marketMapTestContext(target string) *echo.Context {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	res := httptest.NewRecorder()
	return e.NewContext(req, res)
}

func TestParseMarketMapKindsCanonicalizesRepeatedValues(t *testing.T) {
	c := marketMapTestContext("/api/v2/market-map?kinds=want,voucher&kinds=post&kinds=want")
	kinds, err := parseMarketMapKinds(c)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"post", "voucher", "want"}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("kinds = %#v, want %#v", kinds, want)
	}
}

func TestParseMarketMapKindsRejectsUnknownKind(t *testing.T) {
	c := marketMapTestContext("/api/v2/market-map?kinds=voucher,offering")
	if _, err := parseMarketMapKinds(c); err == nil {
		t.Fatal("unknown kind was accepted")
	}
}

func TestParseMarketMapZoomUsesStableBucket(t *testing.T) {
	c := marketMapTestContext("/api/v2/market-map?zoom=12.9")
	zoom, err := parseMarketMapZoom(c)
	if err != nil {
		t.Fatal(err)
	}
	if zoom != 12 {
		t.Fatalf("zoom = %v, want 12", zoom)
	}
}

func TestMarketMapKeyIgnoresKindInputOrder(t *testing.T) {
	c := marketMapTestContext("/api/v2/market-map?q=market&bbox=35,-5,40,1")
	search, err := parseMarketMapSearch(c)
	if err != nil {
		t.Fatal(err)
	}
	first := marketMapKey(search, []string{"voucher", "want"}, 10)
	second := marketMapKey(search, []string{"want", "voucher"}, 10)
	if first != second {
		t.Fatalf("equivalent cache identities differ: %q != %q", first, second)
	}
}

func TestMarketMapGuestIdentityUsesClientIPWithoutSettingCookie(t *testing.T) {
	c := marketMapTestContext("/api/v2/market-map?bbox=35,-5,40,1")
	c.Request().RemoteAddr = "203.0.113.8:443"
	guestLimiter := newMarketRateLimiterWithCapacity(
		marketMapGuestRateLimit,
		marketMapGuestRateBurst,
		marketMapRateMaxClients,
		true,
		time.Now,
	)
	a := &API{marketMapGuestLimiter: guestLimiter}

	limiter, key := a.marketMapRateLimitIdentity(c)
	if limiter != guestLimiter {
		t.Fatal("guest request did not use the aggregate guest limiter")
	}
	if key != "203.0.113.8" {
		t.Fatalf("guest limiter key = %q, want client IP", key)
	}
	if cookies := c.Response().Header().Values("Set-Cookie"); len(cookies) != 0 {
		t.Fatalf("guest map request created cookies: %v", cookies)
	}
}
