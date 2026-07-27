package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/labstack/echo/v5"
)

var (
	errInvalidBbox = errors.New("bbox must be four comma-separated numbers: minLng,minLat,maxLng,maxLat")
)

const (
	// marketMaxPerPage is both the default and the maximum page size for the
	// public market catalog. It is deliberately higher than catalogMaxPerPage:
	// the map client bulk-imports the whole catalog so its cluster counts are
	// correct, and doing that 100 rows at a time would be 22 round trips.
	marketMaxPerPage = 500
)

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

// voucherByAddressHandler serves the public detail view for one voucher: the
// map card and the voucher page both read it. Public for the same reason as
// the catalog — a shared voucher link must open for a signed-out visitor.
func (a *API) voucherByAddressHandler(c *echo.Context) error {
	ctx := c.Request().Context()
	address := c.Param("address")
	if !common.IsHexAddress(address) {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "address must be a valid hex address",
		})
	}

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	voucher, err := a.store.GetMarketVoucherByAddress(ctx, tx, address)
	if err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	setMarketCacheHeaders(c)

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Market voucher",
		Result: map[string]any{
			"voucher": voucher,
		},
	})
}

// setMarketCacheHeaders marks a successful public market response cacheable.
// The representation is identical for guests and signed-in users, so no
// authorization or cookie value may change it. Origin is *merged* into Vary
// rather than overwriting it: AllowCredentials is true in the global CORS
// policy, which makes origin variance mandatory even though this client
// deliberately sends no credentials.
func setMarketCacheHeaders(c *echo.Context) {
	header := c.Response().Header()
	header.Set("Cache-Control", "public, max-age=60, stale-while-revalidate=300")

	for _, existing := range header.Values("Vary") {
		for field := range strings.SplitSeq(existing, ",") {
			if strings.EqualFold(strings.TrimSpace(field), "Origin") {
				return
			}
		}
	}
	header.Add("Vary", "Origin")
}
