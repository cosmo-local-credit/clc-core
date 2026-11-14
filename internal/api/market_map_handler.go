package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/internal/util"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
	"golang.org/x/sync/singleflight"
)

var (
	marketMapKinds = []string{"catalog", "pool", "post", "voucher", "want"}
)

const (
	marketMapZoomStep = 0.25
)

type (
	marketMapRateLimitError struct {
		retryAfter time.Duration
	}

	marketMapCacheIdentity struct {
		Term             string                `json:"term"`
		Tags             []string              `json:"tags"`
		TagMatch         store.TagMatch        `json:"tagMatch"`
		Bounds           []store.CatalogBounds `json:"bounds"`
		CreditablePool   []store.PoolRef       `json:"creditablePools"`
		Kinds            []string              `json:"kinds"`
		Zoom             float64               `json:"zoom"`
		Facets           bool                  `json:"facets"`
		Posted           string                `json:"posted"`
		PostTiming       store.ReportTiming    `json:"postTiming"`
		PostVouchers     []store.VoucherRef    `json:"postVouchers"`
		PostVoucherMatch store.TagMatch        `json:"postVoucherMatch"`
		PostPools        []store.PoolRef       `json:"postPools"`
		PostPoolMatch    store.TagMatch        `json:"postPoolMatch"`
		// creditDepth answers are per wallet: the key carries the wallet,
		// the depth and the reach digest.
		CreditWallet string `json:"creditWallet,omitempty"`
		CreditDepth  int    `json:"creditDepth,omitempty"`
		CreditDigest string `json:"creditDigest,omitempty"`
	}
)

func (e marketMapRateLimitError) Error() string {
	return "market map rate limit exceeded"
}

func parseMarketMapKinds(c *echo.Context) ([]string, error) {
	seen := map[string]struct{}{}
	for _, raw := range c.Request().URL.Query()["kinds"] {
		for value := range strings.SplitSeq(raw, ",") {
			kind := strings.ToLower(strings.TrimSpace(value))
			if kind == "" {
				continue
			}
			if !slices.Contains(marketMapKinds, kind) {
				return nil, errors.New("kinds must contain only catalog, pool, post, voucher, or want")
			}
			seen[kind] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return slices.Clone(marketMapKinds), nil
	}
	kinds := make([]string, 0, len(seen))
	for kind := range seen {
		kinds = append(kinds, kind)
	}
	slices.Sort(kinds)
	return kinds, nil
}

func parseMarketMapSearch(c *echo.Context) (catalogSearchRequest, error) {
	search, err := parseCatalogSearch(c)
	if err != nil {
		return catalogSearchRequest{}, err
	}
	refs, err := parseCatalogRelationshipRefs(
		c, "creditablePools", store.CatalogMaxPools, errInvalidPoolRef, store.ErrCatalogTooManyPools,
	)
	if err != nil {
		return catalogSearchRequest{}, err
	}
	for _, ref := range refs {
		search.creditablePoolRefs = append(search.creditablePoolRefs, store.PoolRef{
			ChainID: ref.chainID,
			Address: ref.address,
		})
	}
	slices.SortFunc(search.creditablePoolRefs, func(left, right store.PoolRef) int {
		if left.ChainID != right.ChainID {
			return int(left.ChainID - right.ChainID)
		}
		return strings.Compare(strings.ToLower(left.Address), strings.ToLower(right.Address))
	})
	if err := parsePostDiscoverySearch(c, &search, true); err != nil {
		return catalogSearchRequest{}, err
	}
	return search, nil
}

func parseMarketMapZoom(c *echo.Context) (float64, error) {
	raw := strings.TrimSpace(c.QueryParam("zoom"))
	if raw == "" {
		return 0, nil
	}
	zoom, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(zoom) || math.IsInf(zoom, 0) || zoom < 0 || zoom > 22 {
		return 0, errors.New("zoom must be between 0 and 22")
	}
	return math.Floor((zoom+1e-9)/marketMapZoomStep) * marketMapZoomStep, nil
}

func marketMapKey(search catalogSearchRequest, kinds []string, zoom float64) string {
	canonicalKinds := slices.Clone(kinds)
	slices.Sort(canonicalKinds)
	identity := marketMapCacheIdentity{
		Term: search.filter.Term, Tags: search.filter.Tags, TagMatch: search.filter.TagMatch,
		Bounds: search.filter.Bounds, CreditablePool: search.creditablePoolRefs,
		Kinds: canonicalKinds, Zoom: zoom, Facets: search.facets, Posted: search.posted,
		PostTiming: search.postTiming, PostVouchers: search.postVoucherRefs,
		PostVoucherMatch: search.postVoucherMatch, PostPools: search.postPoolRefs,
		PostPoolMatch: search.postPoolMatch,
	}
	if search.credit != nil {
		identity.CreditWallet = search.credit.wallet
		identity.CreditDepth = search.credit.depth
		identity.CreditDigest = search.credit.digest
	}
	encoded, _ := json.Marshal(identity)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// marketMapPointsKey identifies a points response by its filters alone. The
// viewport, zoom and facets flag never reach it.
func marketMapPointsKey(search catalogSearchRequest, kinds []string) string {
	search.filter.Bounds = nil
	search.facets = false
	return "points:" + marketMapKey(search, kinds, 0)
}

// marketMapFacetsKey identifies a facets response by its filters and exact
// viewport. Zoom does not change a count.
func marketMapFacetsKey(search catalogSearchRequest, kinds []string) string {
	search.facets = true
	return "facets:" + marketMapKey(search, kinds, 0)
}

func (a *API) marketMapHandler(c *echo.Context) error {
	search, err := parseMarketMapSearch(c)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	credit, handled, err := a.resolveCreditDepth(c, len(search.creditablePoolRefs) > 0)
	if handled {
		return err
	}
	search.credit = credit
	kinds, err := parseMarketMapKinds(c)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	zoom, err := parseMarketMapZoom(c)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	key := marketMapKey(search, kinds, zoom)
	return serveMarketMap(a, c, key, search, func(ctx context.Context, tx pgx.Tx, filter store.CatalogFilter) (store.MarketMapResult, error) {
		result, err := a.store.ListMarketMap(ctx, tx, filter, kinds, zoom, search.facets)
		if err != nil {
			return store.MarketMapResult{}, err
		}
		if search.facets && slices.Contains(kinds, "post") {
			result.PostVoucherFacets, result.PostPoolFacets, err = a.marketMapPostFacets(ctx, tx, filter)
		}
		return result, err
	}, func(result store.MarketMapResult) map[string]any {
		return map[string]any{
			"features": result.Features, "tagFacets": result.TagFacets,
			"postVoucherFacets": result.PostVoucherFacets,
			"postPoolFacets":    result.PostPoolFacets,
		}
	})
}

// marketMapPointsHandler returns every matching point for the client to
// cluster. The viewport is not part of the request: panning and zooming reuse
// one response, and the unfiltered map is one shared cache entry.
func (a *API) marketMapPointsHandler(c *echo.Context) error {
	search, err := parseMarketMapSearch(c)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	credit, handled, err := a.resolveCreditDepth(c, len(search.creditablePoolRefs) > 0)
	if handled {
		return err
	}
	search.credit = credit
	kinds, err := parseMarketMapKinds(c)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	search.filter.Bounds = nil
	return serveMarketMap(a, c, marketMapPointsKey(search, kinds), search, func(ctx context.Context, tx pgx.Tx, filter store.CatalogFilter) (store.MarketMapPoints, error) {
		return a.store.ListMarketMapPoints(ctx, tx, filter, kinds)
	}, func(result store.MarketMapPoints) map[string]any {
		return map[string]any{"points": result.Points, "truncated": result.Truncated}
	})
}

// marketMapFacetsHandler counts the filter facets of the points inside bbox.
func (a *API) marketMapFacetsHandler(c *echo.Context) error {
	search, err := parseMarketMapSearch(c)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	credit, handled, err := a.resolveCreditDepth(c, len(search.creditablePoolRefs) > 0)
	if handled {
		return err
	}
	search.credit = credit
	kinds, err := parseMarketMapKinds(c)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	return serveMarketMap(a, c, marketMapFacetsKey(search, kinds), search, func(ctx context.Context, tx pgx.Tx, filter store.CatalogFilter) (store.MarketMapFacets, error) {
		result := store.MarketMapFacets{PostVoucherFacets: []store.VoucherFacet{}, PostPoolFacets: []store.PoolFacet{}}
		var err error
		result.TagFacets, err = a.store.ListMarketMapTagFacets(ctx, tx, filter, kinds)
		if err != nil {
			return store.MarketMapFacets{}, err
		}
		if slices.Contains(kinds, "post") {
			result.PostVoucherFacets, result.PostPoolFacets, err = a.marketMapPostFacets(ctx, tx, filter)
		}
		return result, err
	}, func(result store.MarketMapFacets) map[string]any {
		return map[string]any{
			"tagFacets": result.TagFacets, "postVoucherFacets": result.PostVoucherFacets,
			"postPoolFacets": result.PostPoolFacets,
		}
	})
}

// serveMarketMap answers one market map read through the shared cache,
// singleflight, executor and rate limiter. read runs inside a read
// transaction with the search's references already resolved.
func serveMarketMap[T any](
	a *API,
	c *echo.Context,
	key string,
	search catalogSearchRequest,
	read func(context.Context, pgx.Tx, store.CatalogFilter) (T, error),
	body func(T) map[string]any,
) error {
	if cached, ok := a.marketMapCache.get(key); ok {
		if result, ok := cached.(T); ok {
			return respondMarketMap(c, body(result))
		}
	}
	limiter, limiterKey := a.marketMapRateLimitIdentity(c)

	resultChannel := loadMarketMap(a, key, limiter, limiterKey, func(ctx context.Context) (T, error) {
		var zero T
		tx, err := a.store.Pool().Begin(ctx)
		if err != nil {
			return zero, err
		}
		defer tx.Rollback(ctx)
		if err := a.store.SetCatalogStatementTimeout(ctx, tx); err != nil {
			return zero, err
		}
		filter, err := a.resolveMarketMapFilter(ctx, tx, search)
		if err != nil {
			return zero, err
		}
		result, err := read(ctx, tx, filter)
		if err != nil {
			return zero, err
		}
		if err := tx.Commit(ctx); err != nil {
			return zero, err
		}
		return result, nil
	})

	select {
	case <-c.Request().Context().Done():
		return c.Request().Context().Err()
	case loaded := <-resultChannel:
		if loaded.Err != nil {
			var limitErr marketMapRateLimitError
			if errors.As(loaded.Err, &limitErr) {
				seconds := max(int(math.Ceil(limitErr.retryAfter.Seconds())), 1)
				c.Response().Header().Set("Retry-After", strconv.Itoa(seconds))
				return c.JSON(http.StatusTooManyRequests, apiresp.ErrResponse{
					Ok: false, ErrCode: apiresp.ErrCodeLimitExceeded,
					Description: "rate limit exceeded, retry later",
				})
			}
			if errors.Is(loaded.Err, errMarketMapCapacity) {
				c.Response().Header().Set("Retry-After", "1")
				return c.JSON(http.StatusServiceUnavailable, apiresp.ErrResponse{
					Ok: false, ErrCode: apiresp.ErrCodeLimitExceeded,
					Description: "market map capacity is temporarily full",
				})
			}
			if errors.Is(loaded.Err, errUnknownTagValue) || errors.Is(loaded.Err, errUnknownPoolRef) || errors.Is(loaded.Err, errUnknownVoucherRef) {
				return respondCatalogSearchError(c, loaded.Err)
			}
			return handlePostgresError(c, loaded.Err)
		}
		result, ok := loaded.Val.(T)
		if !ok {
			return fmt.Errorf("invalid market map result")
		}
		return respondMarketMap(c, body(result))
	}
}

// resolveMarketMapFilter validates the search's tags and turns its pool and
// voucher references into ids.
func (a *API) resolveMarketMapFilter(ctx context.Context, tx pgx.Tx, search catalogSearchRequest) (store.CatalogFilter, error) {
	filter := search.filter
	unknown, err := a.store.UnknownTokenTags(ctx, tx, search.filter.Tags)
	if err != nil {
		return filter, err
	}
	if len(unknown) > 0 {
		return filter, errUnknownTagValue
	}
	if search.credit != nil {
		filter = filter.WithReachablePoolIDs(search.credit.poolIDs).WithCreditVoucherIDs(search.credit.heldVoucherIDs)
	}
	if len(search.creditablePoolRefs) > 0 {
		ids, err := a.store.ResolvePoolRefs(ctx, tx, search.creditablePoolRefs)
		if err != nil {
			return filter, err
		}
		if len(ids) != len(search.creditablePoolRefs) {
			return filter, errUnknownPoolRef
		}
		if filter, err = filter.WithDiscoveryPoolIDs(ids); err != nil {
			return filter, err
		}
	}
	if len(search.postVoucherRefs) > 0 {
		ids, err := a.store.ResolveVoucherRefs(ctx, tx, search.postVoucherRefs)
		if err != nil {
			return filter, err
		}
		if len(ids) != len(search.postVoucherRefs) {
			return filter, errUnknownVoucherRef
		}
		if filter, err = filter.WithVoucherIDs(ids, search.postVoucherMatch); err != nil {
			return filter, err
		}
	}
	if len(search.postPoolRefs) > 0 {
		ids, err := a.store.ResolvePoolRefs(ctx, tx, search.postPoolRefs)
		if err != nil {
			return filter, err
		}
		if len(ids) != len(search.postPoolRefs) {
			return filter, errUnknownPoolRef
		}
		if filter, err = filter.WithPoolIDsMatch(ids, search.postPoolMatch); err != nil {
			return filter, err
		}
	}
	return filter, nil
}

// marketMapPostFacets lists the vouchers and pools of the Posts the filter
// matches. Credit's pools narrow a Post through its own relationships.
func (a *API) marketMapPostFacets(ctx context.Context, tx pgx.Tx, filter store.CatalogFilter) ([]store.VoucherFacet, []store.PoolFacet, error) {
	postFilter := filter
	postFilter.CreditablePoolIDs = postFilter.DiscoveryPoolIDs
	postFilter.DiscoveryPoolIDs = nil
	vouchers, err := a.store.ListReportVoucherFacets(ctx, tx, postFilter)
	if err != nil {
		return nil, nil, err
	}
	pools, err := a.store.ListReportPoolFacets(ctx, tx, postFilter)
	if err != nil {
		return nil, nil, err
	}
	// An empty facet list is [], never null: the client schema rejects null.
	if vouchers == nil {
		vouchers = []store.VoucherFacet{}
	}
	if pools == nil {
		pools = []store.PoolFacet{}
	}
	return vouchers, pools, nil
}

func loadMarketMap[T any](
	a *API,
	key string,
	limiter *marketRateLimiter,
	limiterKey string,
	load func(context.Context) (T, error),
) <-chan singleflight.Result {
	return a.marketMapLoads.DoChan(key, func() (any, error) {
		if cached, ok := a.marketMapCache.get(key); ok {
			return cached, nil
		}
		// Only the coalesced cache miss spends a token. Cache hits and other
		// waiters on an identical miss must not make a shared carrier or VPN
		// address exhaust its aggregate abuse budget.
		if allowed, retryAfter := limiter.allow(limiterKey); !allowed {
			return nil, marketMapRateLimitError{retryAfter: retryAfter}
		}
		// A cache fill is shared by every identical request. Give it its own
		// bounded context so one browser cancelling its obsolete viewport does
		// not cancel useful work for the other waiters or prevent the result
		// from reaching the short-lived cache.
		loadContext, cancelLoad := context.WithTimeout(context.Background(), util.SLATimeout)
		defer cancelLoad()
		return a.marketMapExecutor.run(loadContext, func(ctx context.Context) (any, error) {
			result, err := load(ctx)
			if err != nil {
				return nil, err
			}
			a.marketMapCache.set(key, result)
			return result, nil
		})
	})
}

func respondMarketMap(c *echo.Context, result map[string]any) error {
	header := c.Response().Header()
	// A creditDepth answer is the caller's own reachable set: it must never
	// be stored by a shared cache.
	if header.Get("Cache-Control") != "private, no-store" {
		header.Set("Cache-Control", "public, max-age=15, stale-while-revalidate=60")
	}
	header.Add("Vary", "Origin")
	return c.JSON(http.StatusOK, apiresp.OKResponse{Ok: true, Description: "Market map", Result: result})
}
