package api

import (
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/labstack/echo/v5"
	"golang.org/x/time/rate"
)

const (
	// 60 sustained requests per minute with a burst of 15, keyed by client IP.
	// One API replica serves this route today; if it is ever scaled
	// horizontally this must move to a shared edge limiter rather than being
	// multiplied per replica.
	marketRateLimit = rate.Limit(1) // one token per second == 60/min
	marketRateBurst = 15

	// The registry is bounded and swept so forged source addresses cannot grow
	// it without bound.
	marketRateMaxClients = 10_000
	marketRateIdleTTL    = 10 * time.Minute
	marketRateSweepEvery = time.Minute
)

type marketRateLimiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type marketRateLimiter struct {
	mu      sync.Mutex
	clients map[string]*marketRateLimiterEntry
	limit   rate.Limit
	burst   int
	now     func() time.Time
}

func newMarketRateLimiter(limit rate.Limit, burst int, now func() time.Time) *marketRateLimiter {
	return &marketRateLimiter{
		clients: make(map[string]*marketRateLimiterEntry),
		limit:   limit,
		burst:   burst,
		now:     now,
	}
}

// allow consumes one token for key. When it returns false the second value is
// how long the caller must wait for the next token, which becomes Retry-After.
func (l *marketRateLimiter) allow(key string) (bool, time.Duration) {
	now := l.now()

	l.mu.Lock()
	entry, ok := l.clients[key]
	if !ok {
		// Sweep before admitting a new key so a flood of one-shot addresses
		// reclaims space instead of only growing the map.
		if len(l.clients) >= marketRateMaxClients {
			l.evictIdleLocked(now)
		}
		if len(l.clients) >= marketRateMaxClients {
			l.mu.Unlock()
			// The registry is saturated by active clients; shed rather than grow.
			return false, time.Second
		}
		entry = &marketRateLimiterEntry{limiter: rate.NewLimiter(l.limit, l.burst)}
		l.clients[key] = entry
	}
	entry.lastSeen = now
	limiter := entry.limiter
	l.mu.Unlock()

	reservation := limiter.ReserveN(now, 1)
	if !reservation.OK() {
		return false, time.Second
	}
	if delay := reservation.DelayFrom(now); delay > 0 {
		reservation.CancelAt(now)
		return false, delay
	}
	return true, 0
}

func (l *marketRateLimiter) evictIdle() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.evictIdleLocked(l.now())
}

func (l *marketRateLimiter) evictIdleLocked(now time.Time) {
	for key, entry := range l.clients {
		if now.Sub(entry.lastSeen) >= marketRateIdleTTL {
			delete(l.clients, key)
		}
	}
}

func (l *marketRateLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.clients)
}

func (l *marketRateLimiter) peek(key string) (*rate.Limiter, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.clients[key]
	if !ok {
		return nil, false
	}
	return entry.limiter, true
}

// startSweeper evicts idle entries on a ticker until ctx-less shutdown; the
// goroutine lives for the process lifetime alongside the API.
func (l *marketRateLimiter) startSweeper() {
	go func() {
		ticker := time.NewTicker(marketRateSweepEvery)
		defer ticker.Stop()
		for range ticker.C {
			l.evictIdle()
		}
	}()
}

// marketRateLimitMiddleware guards the public catalog, which has no
// authentication to fall back on.
func (a *API) marketRateLimitMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			allowed, retryAfter := a.marketLimiter.allow(marketClientIP(c.Request(), a.trustProxy))
			if allowed {
				return next(c)
			}

			seconds := int(math.Ceil(retryAfter.Seconds()))
			if seconds < 1 {
				seconds = 1
			}
			c.Response().Header().Set("Retry-After", strconv.Itoa(seconds))
			return c.JSON(http.StatusTooManyRequests, apiresp.ErrResponse{
				Ok:          false,
				ErrCode:     apiresp.ErrCodeLimitExceeded,
				Description: "rate limit exceeded, retry later",
			})
		}
	}
}

// marketClientIP resolves the rate-limit key. Forwarding headers are honoured
// only when the deployment is explicitly configured to sit behind a trusted
// proxy; otherwise an internet-supplied X-Forwarded-For could let one caller
// spend every other caller's budget. With a single trusted proxy the rightmost
// entry is the address that proxy actually observed, so a client-forged
// leftmost entry cannot displace it.
func marketClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			parts := strings.Split(forwarded, ",")
			if candidate := strings.TrimSpace(parts[len(parts)-1]); candidate != "" {
				return candidate
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
