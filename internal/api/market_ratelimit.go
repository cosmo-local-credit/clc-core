package api

import (
	"container/list"
	"math"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/labstack/echo/v5"
	"golang.org/x/time/rate"
)

const (
	// 60 sustained requests per minute, keyed by client IP. One API replica
	// serves this route today; if it is ever scaled horizontally this must move
	// to a shared edge limiter rather than being multiplied per replica.
	//
	// The sustained rate is the abuse control. The burst only decides how bursty a
	// legitimate first minute may be, and it is sized from the requests the Market
	// tab actually issues, enumerated in
	// TestMarketBurstCoversACatalogLoadThenMarkerTaps:
	//
	//	5   catalog pages
	//	2   viewport bootstrap boxes (a viewport crossing the antimeridian splits)
	//	1   tag vocabulary bootstrap
	//	8   filter changes — a typed term settling a few times, then chip toggles
	//	20  marker taps
	//	--
	//	36
	//
	// 40 covered the 27 requests that existed before catalog search, leaving 13 —
	// not enough for a search session on top of a map session, and a first-time
	// visitor would have been 429'd for using the feature normally. 60 covers the
	// enumerated 36 with margin for the exhaustion pages a filtered set over 500
	// rows adds.
	marketRateLimit         = rate.Limit(1) // one token per second == 60/min
	marketRateBurst         = 60
	recipientRateLimit      = rate.Limit(1)
	recipientRateBurst      = 30
	marketMapUserRateLimit  = rate.Limit(2)
	marketMapUserRateBurst  = 30
	marketMapGuestRateLimit = rate.Limit(100)
	marketMapGuestRateBurst = 1000

	// The registry is bounded and swept so forged source addresses cannot grow
	// it without bound.
	marketRateMaxClients    = 10_000
	marketMapRateMaxClients = 100_000
	marketRateIdleTTL       = 10 * time.Minute
	marketRateSweepEvery    = time.Minute
)

type (
	marketRateLimiterEntry struct {
		limiter  *rate.Limiter
		lastSeen time.Time
		element  *list.Element
	}

	marketRateLimiter struct {
		mu          sync.Mutex
		clients     map[string]*marketRateLimiterEntry
		limit       rate.Limit
		burst       int
		now         func() time.Time
		order       *list.List
		maxClients  int
		evictOldest bool
	}
)

func newMarketRateLimiter(limit rate.Limit, burst int, now func() time.Time) *marketRateLimiter {
	return newMarketRateLimiterWithCapacity(limit, burst, marketRateMaxClients, false, now)
}

func newMarketRateLimiterWithCapacity(
	limit rate.Limit,
	burst int,
	maxClients int,
	evictOldest bool,
	now func() time.Time,
) *marketRateLimiter {
	return &marketRateLimiter{
		clients:     make(map[string]*marketRateLimiterEntry),
		limit:       limit,
		burst:       burst,
		now:         now,
		order:       list.New(),
		maxClients:  maxClients,
		evictOldest: evictOldest,
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
		if len(l.clients) >= l.maxClients {
			l.evictIdleLocked(now)
		}
		if len(l.clients) >= l.maxClients && l.evictOldest {
			l.removeOldestLocked()
		}
		if len(l.clients) >= l.maxClients {
			l.mu.Unlock()
			// The registry is saturated by active clients; shed rather than grow.
			return false, time.Second
		}
		entry = &marketRateLimiterEntry{limiter: rate.NewLimiter(l.limit, l.burst)}
		entry.element = l.order.PushFront(key)
		l.clients[key] = entry
	} else {
		l.order.MoveToFront(entry.element)
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
			l.order.Remove(entry.element)
			delete(l.clients, key)
		}
	}
}

func (l *marketRateLimiter) removeOldestLocked() {
	oldest := l.order.Back()
	if oldest == nil {
		return
	}
	key, _ := oldest.Value.(string)
	delete(l.clients, key)
	l.order.Remove(oldest)
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
			return a.throttle(c, a.marketLimiter, next)
		}
	}
}

func (a *API) marketMapRateLimitIdentity(c *echo.Context) (*marketRateLimiter, string) {
	if a.authenticateCaller(c) {
		_, address, role := getUserClaims(c)
		if address != "" && role != "SERVICE" {
			return a.marketMapUserLimiter, strings.ToLower(address)
		}
	}
	return a.marketMapGuestLimiter, marketClientIP(c.Request(), a.trustProxy)
}

// recipientRateLimitMiddleware isolates authenticated identity lookups from
// public Marketplace discovery. User sessions are keyed by their verified
// wallet; non-user credentials fall back to the same trusted client-IP rules
// as public traffic.
func (a *API) recipientRateLimitMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			_, address, _ := getUserClaims(c)
			key := strings.ToLower(strings.TrimSpace(address))
			if key == "" {
				key = marketClientIP(c.Request(), a.trustProxy)
			}
			return a.throttleKey(c, a.recipientLimiter, key, next)
		}
	}
}

// poolCatalogMiddleware opens the pool reads to signed-out visitors, so the
// map's voucher card can list the pools that hold a voucher. A caller with a
// valid credential passes unthrottled and keeps its caller context, exactly as
// on apiGroup, where these routes used to sit: the burst is sized for a visitor,
// not for the signed-in pool screens. Anyone else, including a caller whose
// session has expired, is served as signed out and spends from the pool
// limiter. That limiter is separate from the market's, so pool lookups cannot
// eat into the budget the Market tab is sized against.
func (a *API) poolCatalogMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if a.authenticateCaller(c) {
				return next(c)
			}
			return a.throttle(c, a.poolLimiter, next)
		}
	}
}

// throttle spends one of the client's tokens from limiter, answering 429 with
// Retry-After once they run out.
func (a *API) throttle(c *echo.Context, limiter *marketRateLimiter, next echo.HandlerFunc) error {
	return a.throttleKey(c, limiter, marketClientIP(c.Request(), a.trustProxy), next)
}

func (a *API) throttleKey(c *echo.Context, limiter *marketRateLimiter, key string, next echo.HandlerFunc) error {
	allowed, retryAfter := limiter.allow(key)
	if allowed {
		return next(c)
	}

	seconds := max(int(math.Ceil(retryAfter.Seconds())), 1)
	c.Response().Header().Set("Retry-After", strconv.Itoa(seconds))
	return c.JSON(http.StatusTooManyRequests, apiresp.ErrResponse{
		Ok:          false,
		ErrCode:     apiresp.ErrCodeLimitExceeded,
		Description: "rate limit exceeded, retry later",
	})
}

// marketClientIP resolves the rate-limit key. Forwarding headers are honoured
// only when the deployment is explicitly configured to sit behind a trusted
// proxy; otherwise an internet-supplied X-Forwarded-For could let one caller
// spend every other caller's budget. With a single trusted proxy the rightmost
// entry is the address that proxy actually observed, so a client-forged
// leftmost entry cannot displace it.
func marketClientIP(r *http.Request, trustProxy bool) string {
	peer := directPeerIP(r.RemoteAddr)
	if trustProxy && trustedProxyPeer(peer) {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			parts := strings.Split(forwarded, ",")
			if candidate := strings.TrimSpace(parts[len(parts)-1]); candidate != "" {
				return clientKey(candidate)
			}
		}
	}
	return clientKey(peer)
}

// clientKey buckets an IPv6 caller by its /64. One subscriber is normally
// delegated at least a /64 and can rotate through it freely, so keying the full
// address would hand every rotation a fresh budget. IPv4, including the
// IPv4-mapped form, stays keyed by the single address. A value that does not
// parse is kept as it was, as before.
func clientKey(address string) string {
	ip, err := netip.ParseAddr(address)
	if err != nil {
		return address
	}
	ip = ip.Unmap()
	if ip.Is4() {
		return ip.String()
	}
	prefix, err := ip.Prefix(64)
	if err != nil {
		return address
	}
	return prefix.String()
}

func directPeerIP(remoteAddress string) string {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		return remoteAddress
	}
	return host
}

// trustedProxyPeer constrains forwarded-header trust to the Caddy hop. The
// deployment flag still has to be enabled, and the service must be reached by
// loopback or a private container-network address. A public direct client
// cannot opt itself into a new rate-limit identity with a forged header.
func trustedProxyPeer(peer string) bool {
	ip := net.ParseIP(strings.TrimSpace(peer))
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}
