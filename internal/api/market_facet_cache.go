package api

import (
	"slices"
	"sync"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// An in-process cache for the one tag facet that depends on nothing in the
// request.
//
// Every other facet shape is cheap because a term or a viewport narrows the
// candidate set before the tag junction is touched: measured at 5x scale, a
// common-term facet is 1.1 ms and a no-match one 0.05 ms. The filter-free facet
// has no such narrowing, so it groups over every available offering of every
// visible voucher: 9.1 ms, against 1.1 ms for the 500-row catalog page this same
// endpoint serves. It is the only query here an order of magnitude off the
// others, it is unauthenticated, and the client asks for it once per cold load to
// populate its tag vocabulary -- so it is worth exactly one cache and nothing
// else is.

const (
	// The TTL deliberately matches the max-age this endpoint already publishes in
	// Cache-Control, so caching server-side introduces no staleness the response
	// contract did not already permit. A newly tagged offering appears in the chip
	// row within a minute, which is the same guarantee a client behind a shared
	// HTTP cache already had.
	marketFacetCacheTTL = time.Minute
)

type (
	marketFacetCache struct {
		mu       sync.Mutex
		facets   []store.TagFacet
		loadedAt time.Time
		now      func() time.Time
	}
)

func newMarketFacetCache(now func() time.Time) *marketFacetCache {
	return &marketFacetCache{now: now}
}

// get returns the cached facet when it is still fresh. The copy is defensive: the
// cached slice outlives the request that produced it, and a caller that sorted or
// appended to it in place would corrupt every later response.
func (c *marketFacetCache) get() ([]store.TagFacet, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.facets == nil || c.now().Sub(c.loadedAt) >= marketFacetCacheTTL {
		return nil, false
	}
	return slices.Clone(c.facets), true
}

func (c *marketFacetCache) set(facets []store.TagFacet) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.facets = slices.Clone(facets)
	c.loadedAt = c.now()
}

// marketVoucherTagFacets serves the voucher tag facet, reading through the cache
// only for the filter-free shape.
//
// The lock is never held across the database call. Concurrent cold requests
// therefore each compute the facet once rather than queueing behind one another:
// at 9.1 ms a small herd is cheaper than serialising every facet request in the
// process behind a single mutex, and the redundant work is bounded by the rate
// limiter.
func (a *API) marketVoucherTagFacets(
	c *echo.Context,
	tx pgx.Tx,
	bounds *store.MarketVoucherBounds,
	filter store.CatalogFilter,
) ([]store.TagFacet, error) {
	ctx := c.Request().Context()

	// Cacheable means request-independent: no viewport and no filter. This
	// coincides with the condition under which the store skips its vocabulary
	// statement, but the two decisions are made for different reasons -- that one
	// is a SQL equivalence, this one is about what varies per caller -- so neither
	// should be derived from the other.
	if bounds != nil || filter.Active() {
		return a.store.ListMarketVoucherTagFacets(ctx, tx, bounds, filter)
	}

	if cached, ok := a.marketFacets.get(); ok {
		return cached, nil
	}

	facets, err := a.store.ListMarketVoucherTagFacets(ctx, tx, bounds, filter)
	if err != nil {
		return nil, err
	}
	a.marketFacets.set(facets)
	return facets, nil
}
