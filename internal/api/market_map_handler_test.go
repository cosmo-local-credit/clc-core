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

func TestParseMarketMapZoomUsesStableQuarterBuckets(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want float64
	}{
		{"12.9", 12.75},
		{"14.74", 14.5},
		{"14.75", 14.75},
		{"20.99", 20.75},
	} {
		t.Run(test.raw, func(t *testing.T) {
			c := marketMapTestContext("/api/v2/market-map?zoom=" + test.raw)
			zoom, err := parseMarketMapZoom(c)
			if err != nil {
				t.Fatal(err)
			}
			if zoom != test.want {
				t.Fatalf("zoom = %v, want %v", zoom, test.want)
			}
		})
	}
}

func TestMarketMapKeyDistinguishesPostedWindow(t *testing.T) {
	oneMonth, err := parseMarketMapSearch(marketMapTestContext("/api/v2/market-map"))
	if err != nil {
		t.Fatal(err)
	}
	allTime, err := parseMarketMapSearch(marketMapTestContext("/api/v2/market-map?posted=all"))
	if err != nil {
		t.Fatal(err)
	}
	explicitMonth, err := parseMarketMapSearch(marketMapTestContext("/api/v2/market-map?posted=1m"))
	if err != nil {
		t.Fatal(err)
	}
	kinds := []string{"post"}
	if marketMapKey(oneMonth, kinds, 10) == marketMapKey(allTime, kinds, 10) {
		t.Fatal("default 1m and posted=all must not share a cache key")
	}
	if marketMapKey(oneMonth, kinds, 10) != marketMapKey(explicitMonth, kinds, 10) {
		t.Fatal("omitted posted and posted=1m must share a cache key")
	}
}

func TestMarketMapKeyDistinguishesPostTimingAndRelationships(t *testing.T) {
	base, err := parseMarketMapSearch(marketMapTestContext("/api/v2/market-map?postTiming=future"))
	if err != nil {
		t.Fatal(err)
	}
	past, err := parseMarketMapSearch(marketMapTestContext("/api/v2/market-map?postTiming=past"))
	if err != nil {
		t.Fatal(err)
	}
	pool, err := parseMarketMapSearch(marketMapTestContext(
		"/api/v2/market-map?postTiming=future&postPools=100:0x1111111111111111111111111111111111111111"))
	if err != nil {
		t.Fatal(err)
	}
	kinds := []string{"post"}
	if marketMapKey(base, kinds, 10) == marketMapKey(past, kinds, 10) ||
		marketMapKey(base, kinds, 10) == marketMapKey(pool, kinds, 10) {
		t.Fatal("Post timing or relationships were omitted from map cache identity")
	}
}

func TestParseMarketMapSearchDefaultsPostedToOneMonth(t *testing.T) {
	search, err := parseMarketMapSearch(marketMapTestContext("/api/v2/market-map"))
	if err != nil {
		t.Fatal(err)
	}
	if search.posted != "1m" || search.filter.PostedAfter == nil {
		t.Fatalf("posted = %q PostedAfter = %v, want 1m with a cutoff", search.posted, search.filter.PostedAfter)
	}
}

func TestParseMarketMapSearchRejectsUnknownPosted(t *testing.T) {
	if _, err := parseMarketMapSearch(marketMapTestContext("/api/v2/market-map?posted=2m")); err == nil {
		t.Fatal("unknown posted was accepted")
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
