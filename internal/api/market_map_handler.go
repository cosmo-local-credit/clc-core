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
		Term           string                `json:"term"`
		Tags           []string              `json:"tags"`
		TagMatch       store.TagMatch        `json:"tagMatch"`
		Bounds         []store.CatalogBounds `json:"bounds"`
		CreditablePool []store.PoolRef       `json:"creditablePools"`
		Kinds          []string              `json:"kinds"`
		Zoom           float64               `json:"zoom"`
		Facets         bool                  `json:"facets"`
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
		Kinds: canonicalKinds, Zoom: zoom, Facets: search.facets,
	}
	encoded, _ := json.Marshal(identity)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func (a *API) marketMapHandler(c *echo.Context) error {
	search, err := parseMarketMapSearch(c)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	kinds, err := parseMarketMapKinds(c)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	zoom, err := parseMarketMapZoom(c)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	key := marketMapKey(search, kinds, zoom)
	if cached, ok := a.marketMapCache.get(key); ok {
		return respondMarketMap(c, cached)
	}
	limiter, limiterKey := a.marketMapRateLimitIdentity(c)

	resultChannel := a.loadMarketMap(key, limiter, limiterKey, func(ctx context.Context) (store.MarketMapResult, error) {
		filter := search.filter
		tx, beginErr := a.store.Pool().Begin(ctx)
		if beginErr != nil {
			return store.MarketMapResult{}, beginErr
		}
		defer tx.Rollback(ctx)
		if timeoutErr := a.store.SetCatalogStatementTimeout(ctx, tx); timeoutErr != nil {
			return store.MarketMapResult{}, timeoutErr
		}
		unknown, readErr := a.store.UnknownTokenTags(ctx, tx, search.filter.Tags)
		if readErr != nil {
			return store.MarketMapResult{}, readErr
		}
		if len(unknown) > 0 {
			return store.MarketMapResult{}, errUnknownTagValue
		}
		if len(search.creditablePoolRefs) > 0 {
			ids, resolveErr := a.store.ResolvePoolRefs(ctx, tx, search.creditablePoolRefs)
			if resolveErr != nil {
				return store.MarketMapResult{}, resolveErr
			}
			if len(ids) != len(search.creditablePoolRefs) {
				return store.MarketMapResult{}, errUnknownPoolRef
			}
			filter, resolveErr = filter.WithPoolIDsMatch(ids, store.TagMatchAny)
			if resolveErr != nil {
				return store.MarketMapResult{}, resolveErr
			}
		}
		result, readErr := a.store.ListMarketMap(ctx, tx, filter, kinds, zoom, search.facets)
		if readErr != nil {
			return store.MarketMapResult{}, readErr
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return store.MarketMapResult{}, commitErr
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
			if errors.Is(loaded.Err, errUnknownTagValue) || errors.Is(loaded.Err, errUnknownPoolRef) {
				return respondCatalogSearchError(c, loaded.Err)
			}
			return handlePostgresError(c, loaded.Err)
		}
		result, ok := loaded.Val.(store.MarketMapResult)
		if !ok {
			return fmt.Errorf("invalid market map result")
		}
		return respondMarketMap(c, result)
	}
}

func (a *API) loadMarketMap(
	key string,
	limiter *marketRateLimiter,
	limiterKey string,
	load func(context.Context) (store.MarketMapResult, error),
) <-chan singleflight.Result {
	return a.marketMapLoads.DoChan(key, func() (any, error) {
		if cached, ok := a.marketMapCache.get(key); ok {
			return cached, nil
		}
		// Only the coalesced cache miss spends a token. Cache hits and other
		// waiters on an identical miss must not make a shared carrier or VPN
		// address exhaust its aggregate abuse budget.
		if allowed, retryAfter := limiter.allow(limiterKey); !allowed {
			return store.MarketMapResult{}, marketMapRateLimitError{retryAfter: retryAfter}
		}
		// A cache fill is shared by every identical request. Give it its own
		// bounded context so one browser cancelling its obsolete viewport does
		// not cancel useful work for the other waiters or prevent the result
		// from reaching the short-lived cache.
		loadContext, cancelLoad := context.WithTimeout(context.Background(), util.SLATimeout)
		defer cancelLoad()
		return a.marketMapExecutor.run(loadContext, func(ctx context.Context) (store.MarketMapResult, error) {
			result, err := load(ctx)
			if err != nil {
				return store.MarketMapResult{}, err
			}
			a.marketMapCache.set(key, result)
			return result, nil
		})
	})
}

func respondMarketMap(c *echo.Context, result store.MarketMapResult) error {
	header := c.Response().Header()
	header.Set("Cache-Control", "public, max-age=15, stale-while-revalidate=60")
	header.Add("Vary", "Origin")
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok: true, Description: "Market map",
		Result: map[string]any{"features": result.Features, "tagFacets": result.TagFacets},
	})
}
