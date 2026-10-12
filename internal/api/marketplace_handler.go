package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

var (
	marketplaceKinds = []string{"catalog", "offering", "pool", "voucher"}
)

func parseMarketplaceKinds(c *echo.Context) ([]string, error) {
	seen := map[string]struct{}{}
	for _, raw := range c.Request().URL.Query()["kinds"] {
		for value := range strings.SplitSeq(raw, ",") {
			kind := strings.ToLower(strings.TrimSpace(value))
			if kind == "" {
				continue
			}
			if !slices.Contains(marketplaceKinds, kind) {
				return nil, fmt.Errorf("kinds must contain only catalog, offering, pool, or voucher")
			}
			seen[kind] = struct{}{}
		}
	}
	kinds := make([]string, 0, len(seen))
	for kind := range seen {
		kinds = append(kinds, kind)
	}
	slices.Sort(kinds)
	return kinds, nil
}

func marketplaceOrigin(caller uint64, filter store.CatalogFilter, kinds []string) string {
	lat, lng := 0.0, 0.0
	if filter.Nearby != nil {
		lat, lng = filter.Nearby.Lat, filter.Nearby.Lng
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%d:%t:%.17g:%.17g:%s:%v:%d:%v:%v",
		caller, filter.Nearby != nil, lat, lng, filter.Term, filter.Tags, filter.TagMatch, filter.PoolIDs, kinds))
	return hex.EncodeToString(sum[:])
}

func parseMarketplaceCursor(raw, origin string) (store.MarketplaceCursor, error) {
	var cursor store.MarketplaceCursor
	if raw == "" {
		return cursor, nil
	}
	if len(raw) > 2048 {
		return cursor, errors.New("invalid marketplace cursor")
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.ID == 0 ||
		cursor.Origin != origin || cursor.UpdatedAt.IsZero() || cursor.Rank < 0 ||
		math.IsNaN(cursor.Distance) || math.IsInf(cursor.Distance, 0) || cursor.Distance < 0 ||
		!slices.Contains(marketplaceKinds, cursor.Kind) {
		return store.MarketplaceCursor{}, errors.New("marketplace cursor does not match this search; restart pagination")
	}
	return cursor, nil
}

func encodeMarketplaceCursor(cursor store.MarketplaceCursor) string {
	data, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(data)
}

func (a *API) marketplaceListHandler(c *echo.Context) error {
	size, err := curatedPageSize(c)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	search, err := parseMarketDiscoverySearch(c)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	kinds, err := parseMarketplaceKinds(c)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	return a.curatedResponse(c, func(tx pgx.Tx) (map[string]any, error) {
		if handled, readErr := a.rejectUnknownTags(c, tx, search.filter.Tags); handled || readErr != nil {
			if readErr != nil {
				return nil, readErr
			}
			return nil, errCuratedHandled
		}
		if handled, readErr := a.resolvePoolFilter(c, tx, &search); handled || readErr != nil {
			if readErr != nil {
				return nil, readErr
			}
			return nil, errCuratedHandled
		}
		if handled, readErr := a.resolveNearbyCatalog(c, tx, &search); handled || readErr != nil {
			if readErr != nil {
				return nil, readErr
			}
			return nil, errCuratedHandled
		}
		caller, _, _ := getUserClaims(c)
		origin := marketplaceOrigin(caller, search.filter, kinds)
		cursor, readErr := parseMarketplaceCursor(c.QueryParam("cursor"), origin)
		if readErr != nil {
			return nil, readErr
		}
		includeFacets := search.facets && cursor.ID == 0
		rows, readErr := a.store.ListMarketplace(c.Request().Context(), tx, search.filter, kinds, search.filter.Nearby, cursor, size+1, includeFacets)
		if readErr != nil {
			return nil, readErr
		}
		var next *string
		if uint64(len(rows)) > size {
			rows = rows[:size]
			last := rows[len(rows)-1]
			distance := 0.0
			if last.DistanceMeters != nil {
				distance = *last.DistanceMeters
			}
			token := encodeMarketplaceCursor(store.MarketplaceCursor{
				Rank: last.Rank, Distance: distance, UpdatedAt: last.UpdatedAt,
				Kind: last.Kind, ID: last.ID, Origin: origin,
			})
			next = &token
		}
		result := map[string]any{"results": rows, "nextCursor": next}
		if includeFacets {
			facets := []store.TagFacet{}
			if len(rows) > 0 && len(rows[0].TagFacets) > 0 {
				if readErr := json.Unmarshal(rows[0].TagFacets, &facets); readErr != nil {
					return nil, readErr
				}
			}
			result["tagFacets"] = facets
		}
		return result, nil
	})
}
