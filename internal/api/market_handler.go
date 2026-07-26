package api

import (
	"errors"
	"strconv"
	"strings"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/labstack/echo/v5"
)

var errInvalidBbox = errors.New("bbox must be four comma-separated numbers: minLng,minLat,maxLng,maxLat")

// marketMaxPerPage is both the default and the maximum page size for the
// public market catalog. It is deliberately higher than catalogMaxPerPage:
// the map client bulk-imports the whole catalog so its cluster counts are
// correct, and doing that 100 rows at a time would be 22 round trips.
const marketMaxPerPage = 500

// parseMarketBounds reads the optional `bbox=minLng,minLat,maxLng,maxLat`
// query value. An empty value means the caller wants the unbounded catalog.
// The parsed box is never interpolated into SQL; it only selects the bounded
// prepared statement and supplies its parameters.
func parseMarketBounds(raw string) (*store.MarketVoucherBounds, error) {
	if raw == "" {
		return nil, nil
	}

	parts := strings.Split(raw, ",")
	if len(parts) != 4 {
		return nil, errInvalidBbox
	}

	var values [4]float64
	for i, part := range parts {
		value, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil {
			return nil, errInvalidBbox
		}
		values[i] = value
	}

	bounds := store.MarketVoucherBounds{
		MinLng: values[0],
		MinLat: values[1],
		MaxLng: values[2],
		MaxLat: values[3],
	}
	if err := bounds.Validate(); err != nil {
		return nil, err
	}
	return &bounds, nil
}

// setMarketCacheHeaders marks the catalog publicly cacheable. The
// representation is identical for guests and signed-in users, so no
// authorization or cookie value may change it. Origin is *merged* into Vary
// rather than overwriting it: AllowCredentials is true in the global CORS
// policy, which makes origin variance mandatory even though this client
// deliberately sends no credentials.
func setMarketCacheHeaders(c *echo.Context) {
	header := c.Response().Header()
	header.Set("Cache-Control", "public, max-age=60, stale-while-revalidate=300")

	for _, existing := range header.Values("Vary") {
		for _, field := range strings.Split(existing, ",") {
			if strings.EqualFold(strings.TrimSpace(field), "Origin") {
				return
			}
		}
	}
	header.Add("Vary", "Origin")
}
