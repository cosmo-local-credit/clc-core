package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type (
	// fakeClock lets the limiter tests advance time without sleeping.
	fakeClock struct {
		mu  sync.Mutex
		now time.Time
	}
)

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 7, 26, 8, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestMarketLimiterAllowsTheConfiguredBurst(t *testing.T) {
	clock := newFakeClock()
	limiter := newMarketRateLimiter(marketRateLimit, marketRateBurst, clock.Now)

	for i := range marketRateBurst {
		allowed, _ := limiter.allow("198.51.100.7")
		if !allowed {
			t.Fatalf("request %d of the burst was rejected", i+1)
		}
	}
}

func TestMarketLimiterRejectsBeyondTheBurstWithARetryDelay(t *testing.T) {
	clock := newFakeClock()
	limiter := newMarketRateLimiter(marketRateLimit, marketRateBurst, clock.Now)

	for range marketRateBurst {
		limiter.allow("198.51.100.7")
	}

	allowed, retryAfter := limiter.allow("198.51.100.7")
	if allowed {
		t.Fatal("expected the request after the burst to be rejected")
	}
	if retryAfter <= 0 {
		t.Fatalf("retryAfter = %v, want a positive delay derived from the next token", retryAfter)
	}
	// 60 requests per minute refills one token per second.
	if retryAfter > time.Second {
		t.Fatalf("retryAfter = %v, want no more than the one second refill interval", retryAfter)
	}
}

func TestMarketLimiterRefillsOverTime(t *testing.T) {
	clock := newFakeClock()
	limiter := newMarketRateLimiter(marketRateLimit, marketRateBurst, clock.Now)

	for range marketRateBurst {
		limiter.allow("198.51.100.7")
	}
	if allowed, _ := limiter.allow("198.51.100.7"); allowed {
		t.Fatal("expected the bucket to be empty")
	}

	clock.Advance(time.Second)
	if allowed, _ := limiter.allow("198.51.100.7"); !allowed {
		t.Fatal("expected one refilled token after one second")
	}
}

func TestRecipientLimiterUsesItsOwnSmallerBudget(t *testing.T) {
	clock := newFakeClock()
	market := newMarketRateLimiter(marketRateLimit, marketRateBurst, clock.Now)
	recipients := newMarketRateLimiter(recipientRateLimit, recipientRateBurst, clock.Now)
	const caller = "0x00000000000000000000000000000000000000aa"

	for range recipientRateBurst {
		if allowed, _ := recipients.allow(caller); !allowed {
			t.Fatal("recipient lookup was limited inside its configured burst")
		}
	}
	if allowed, _ := recipients.allow(caller); allowed {
		t.Fatal("recipient lookup beyond the 30-request burst was allowed")
	}
	if allowed, _ := market.allow(caller); !allowed {
		t.Fatal("recipient lookups consumed the caller's Marketplace budget")
	}
}

func TestRecipientLimiterCaseFoldsAuthenticatedCallerKeys(t *testing.T) {
	clock := newFakeClock()
	limiter := newMarketRateLimiter(recipientRateLimit, recipientRateBurst, clock.Now)
	lower := "0x00000000000000000000000000000000000000aa"
	upper := strings.ToUpper(lower)

	for range recipientRateBurst {
		limiter.allow(strings.ToLower(strings.TrimSpace(upper)))
	}
	if allowed, _ := limiter.allow(strings.ToLower(lower)); allowed {
		t.Fatal("wallet-address casing created a second recipient lookup budget")
	}
}

func TestMarketLimiterIsolatesClients(t *testing.T) {
	clock := newFakeClock()
	limiter := newMarketRateLimiter(marketRateLimit, marketRateBurst, clock.Now)

	for range marketRateBurst {
		limiter.allow("198.51.100.7")
	}
	if allowed, _ := limiter.allow("198.51.100.7"); allowed {
		t.Fatal("expected the first client to be limited")
	}

	if allowed, _ := limiter.allow("203.0.113.9"); !allowed {
		t.Fatal("a second client must not inherit the first client's budget")
	}
}

func TestMarketLimiterEvictsIdleClients(t *testing.T) {
	clock := newFakeClock()
	limiter := newMarketRateLimiter(marketRateLimit, marketRateBurst, clock.Now)

	limiter.allow("198.51.100.7")
	limiter.allow("203.0.113.9")
	if got := limiter.size(); got != 2 {
		t.Fatalf("size = %d, want 2", got)
	}

	clock.Advance(marketRateIdleTTL / 2)
	limiter.allow("203.0.113.9")

	clock.Advance(marketRateIdleTTL/2 + time.Second)
	limiter.evictIdle()

	if got := limiter.size(); got != 1 {
		t.Fatalf("size = %d, want only the recently active client to survive", got)
	}
	// The surviving entry keeps its consumed tokens rather than resetting.
	if _, ok := limiter.peek("203.0.113.9"); !ok {
		t.Fatal("expected the recently active client to survive eviction")
	}
}

func TestMarketLimiterBoundsTheClientRegistry(t *testing.T) {
	clock := newFakeClock()
	limiter := newMarketRateLimiter(marketRateLimit, marketRateBurst, clock.Now)

	for i := range marketRateMaxClients + 100 {
		limiter.allow(spoofedAddress(i))
	}

	if got := limiter.size(); got > marketRateMaxClients {
		t.Fatalf("size = %d, want at most %d so spoofed addresses cannot grow the map without bound", got, marketRateMaxClients)
	}
}

func TestMarketMapLimiterEvictsLeastRecentlyUsedClient(t *testing.T) {
	clock := newFakeClock()
	limiter := newMarketRateLimiterWithCapacity(marketMapGuestRateLimit, marketMapGuestRateBurst, 2, true, clock.Now)

	limiter.allow("first")
	limiter.allow("second")
	limiter.allow("first") // Promote first; second is now least recently used.
	limiter.allow("third")

	if _, ok := limiter.peek("second"); ok {
		t.Fatal("least-recently-used client survived at capacity")
	}
	if _, ok := limiter.peek("first"); !ok {
		t.Fatal("recently used client was evicted")
	}
	if _, ok := limiter.peek("third"); !ok {
		t.Fatal("new client was not admitted")
	}
}

func TestMarketMapSignedInAndGuestBudgetsAreIndependent(t *testing.T) {
	clock := newFakeClock()
	users := newMarketRateLimiterWithCapacity(marketMapUserRateLimit, marketMapUserRateBurst, 10, true, clock.Now)
	guests := newMarketRateLimiterWithCapacity(marketMapGuestRateLimit, marketMapGuestRateBurst, 10, true, clock.Now)

	for range marketMapUserRateBurst {
		users.allow("0x00000000000000000000000000000000000000aa")
	}
	if allowed, _ := users.allow("0x00000000000000000000000000000000000000aa"); allowed {
		t.Fatal("signed-in bucket exceeded its burst")
	}
	if allowed, _ := guests.allow("198.51.100.7"); !allowed {
		t.Fatal("signed-in activity consumed the aggregate guest-IP budget")
	}
}

func TestMarketLimiterIsConcurrencySafe(t *testing.T) {
	clock := newFakeClock()
	limiter := newMarketRateLimiter(marketRateLimit, marketRateBurst, clock.Now)

	var wg sync.WaitGroup
	for i := range 64 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			limiter.allow(spoofedAddress(i % 8))
		}(i)
	}
	wg.Wait()

	if got := limiter.size(); got != 8 {
		t.Fatalf("size = %d, want 8 distinct clients", got)
	}
}

func spoofedAddress(i int) string {
	return "198.51.100." + string(rune('a'+i%26)) + "." + time.Duration(i).String()
}

func TestClientIPIgnoresForwardingHeadersFromUntrustedPeers(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v2/vouchers", nil)
	req.RemoteAddr = "203.0.113.9:54321"
	req.Header.Set("X-Forwarded-For", "198.51.100.7")
	req.Header.Set("X-Real-IP", "198.51.100.8")

	if got := marketClientIP(req, false); got != "203.0.113.9" {
		t.Fatalf("clientIP = %q, want the direct peer 203.0.113.9", got)
	}
}

func TestClientIPUsesTheProxyAppendedAddressWhenTrusted(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v2/vouchers", nil)
	req.RemoteAddr = "10.0.0.2:54321"
	// A client that forges a leftmost entry still ends up rightmost-of-one
	// after Caddy appends the address it actually observed.
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 198.51.100.7")

	if got := marketClientIP(req, true); got != "198.51.100.7" {
		t.Fatalf("clientIP = %q, want the rightmost proxy-appended address", got)
	}
}

func TestClientIPIgnoresForwardingHeadersFromPublicPeerEvenWhenEnabled(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v2/market-map", nil)
	req.RemoteAddr = "203.0.113.9:54321"
	req.Header.Set("X-Forwarded-For", "198.51.100.7")

	if got := marketClientIP(req, true); got != "203.0.113.9" {
		t.Fatalf("clientIP = %q, want the public direct peer", got)
	}
}

func TestClientIPFallsBackToThePeerWhenTrustedProxySendsNoHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v2/vouchers", nil)
	req.RemoteAddr = "10.0.0.2:54321"

	if got := marketClientIP(req, true); got != "10.0.0.2" {
		t.Fatalf("clientIP = %q, want the peer 10.0.0.2", got)
	}
}
