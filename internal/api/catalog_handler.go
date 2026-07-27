package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

type (
	paginatedCatalogListCallbacks[T any] struct {
		firstPage func(context.Context, pgx.Tx, uint64) ([]T, error)
		nextPage  func(context.Context, pgx.Tx, uint64, uint64) ([]T, error)
		prevPage  func(context.Context, pgx.Tx, uint64, uint64) ([]T, error)
	}
)

func (a *API) poolVouchersListHandler(c *echo.Context) error { //nolint:dupl // distinct generic instantiations of catalogAddressListHandler, not extractable further
	return catalogAddressListHandler(c, a, "vouchers", "Vouchers in pool %s",
		func(v store.PoolVoucher) uint64 { return v.ID },
		func(ctx context.Context, tx pgx.Tx, address string, limit uint64) ([]store.PoolVoucher, error) {
			return a.store.ListPoolVouchers(ctx, tx, address, limit)
		},
		func(ctx context.Context, tx pgx.Tx, address string, cursor, limit uint64) ([]store.PoolVoucher, error) {
			return a.store.ListPoolVouchersNext(ctx, tx, address, cursor, limit)
		},
		func(ctx context.Context, tx pgx.Tx, address string, cursor, limit uint64) ([]store.PoolVoucher, error) {
			return a.store.ListPoolVouchersPrevious(ctx, tx, address, cursor, limit)
		},
	)
}

func (a *API) offeringsListHandler(c *echo.Context) error {
	pagination, err := bindCatalogPagination(c)
	if err != nil {
		return err
	}

	return cursorPaginatedCatalogList(c, a, paginatedCatalogListCallbacks[store.Offering]{
		firstPage: a.store.ListOfferings,
		nextPage:  a.store.ListOfferingsNext,
		prevPage:  a.store.ListOfferingsPrevious,
	}, pagination, "offerings", "Offerings", func(o store.Offering) uint64 {
		return o.ID
	})
}

func (a *API) voucherOfferingsListHandler(c *echo.Context) error { //nolint:dupl // distinct generic instantiations of catalogAddressListHandler, not extractable further
	return catalogAddressListHandler(c, a, "offerings", "Offerings for voucher %s",
		func(o store.Offering) uint64 { return o.ID },
		func(ctx context.Context, tx pgx.Tx, address string, limit uint64) ([]store.Offering, error) {
			return a.store.ListVoucherOfferings(ctx, tx, address, limit)
		},
		func(ctx context.Context, tx pgx.Tx, address string, cursor, limit uint64) ([]store.Offering, error) {
			return a.store.ListVoucherOfferingsNext(ctx, tx, address, cursor, limit)
		},
		func(ctx context.Context, tx pgx.Tx, address string, cursor, limit uint64) ([]store.Offering, error) {
			return a.store.ListVoucherOfferingsPrevious(ctx, tx, address, cursor, limit)
		},
	)
}

// catalogAddressListHandler is the shared implementation behind every
// paginated catalog endpoint keyed by a path address (pool or voucher).
func catalogAddressListHandler[T any](
	c *echo.Context,
	a *API,
	resultKey, descriptionFmt string,
	getID func(T) uint64,
	firstPage func(context.Context, pgx.Tx, string, uint64) ([]T, error),
	nextPage func(context.Context, pgx.Tx, string, uint64, uint64) ([]T, error),
	prevPage func(context.Context, pgx.Tx, string, uint64, uint64) ([]T, error),
) error {
	address, pagination, valid, err := bindCatalogAddressPagination(c)
	if err != nil {
		return err
	}
	if !valid {
		return nil
	}

	return cursorPaginatedCatalogList(c, a, paginatedCatalogListCallbacks[T]{
		firstPage: func(ctx context.Context, tx pgx.Tx, limit uint64) ([]T, error) {
			return firstPage(ctx, tx, address, limit)
		},
		nextPage: func(ctx context.Context, tx pgx.Tx, cursor, limit uint64) ([]T, error) {
			return nextPage(ctx, tx, address, cursor, limit)
		},
		prevPage: func(ctx context.Context, tx pgx.Tx, cursor, limit uint64) ([]T, error) {
			return prevPage(ctx, tx, address, cursor, limit)
		},
	}, pagination, resultKey, fmt.Sprintf(descriptionFmt, address), getID)
}

// vouchersListHandler serves the public market catalog: active, geolocated
// voucher summaries with a backend-derived marker kind. It is the only public
// /api/v2 route, so it carries its own cache policy and per-IP rate limit.
func (a *API) vouchersListHandler(c *echo.Context) error {
	req := apiresp.CursorPaginationRequest{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}

	// Parsed once here; the box only ever selects the bounded prepared
	// statement and supplies its parameters. No SQL is built from user input.
	bounds, err := parseMarketBounds(c.Request().URL.Query().Get("bbox"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: err.Error(),
		})
	}

	setMarketCacheHeaders(c)

	return cursorPaginatedCatalogList(c, a, paginatedCatalogListCallbacks[store.MarketVoucher]{
		firstPage: func(ctx context.Context, tx pgx.Tx, limit uint64) ([]store.MarketVoucher, error) {
			return a.store.ListMarketVouchers(ctx, tx, bounds, limit)
		},
		nextPage: func(ctx context.Context, tx pgx.Tx, cursor, limit uint64) ([]store.MarketVoucher, error) {
			return a.store.ListMarketVouchersNext(ctx, tx, bounds, cursor, limit)
		},
		prevPage: func(ctx context.Context, tx pgx.Tx, cursor, limit uint64) ([]store.MarketVoucher, error) {
			return a.store.ListMarketVouchersPrevious(ctx, tx, bounds, cursor, limit)
		},
	}, normalizePaginationWithMax(req, marketMaxPerPage), "vouchers", "Market vouchers", func(v store.MarketVoucher) uint64 {
		return v.ID
	})
}

func bindCatalogPagination(c *echo.Context) (paginationParams, error) {
	req := apiresp.CursorPaginationRequest{}
	if err := c.Bind(&req); err != nil {
		return paginationParams{}, handleBindError(c, err)
	}
	return normalizePagination(req), nil
}

func bindCatalogAddressPagination(c *echo.Context) (string, paginationParams, bool, error) {
	pagination, err := bindCatalogPagination(c)
	if err != nil {
		return "", paginationParams{}, false, err
	}

	address := c.Param("address")
	if !common.IsHexAddress(address) {
		err := c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "address must be a valid hex address",
		})
		return "", paginationParams{}, false, err
	}
	return address, pagination, true, nil
}

func cursorPaginatedCatalogList[T any](
	c *echo.Context,
	a *API,
	callbacks paginatedCatalogListCallbacks[T],
	pagination paginationParams,
	resultKey, description string,
	getID func(T) uint64,
) error {
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var items []T
	switch {
	case pagination.FirstPage:
		items, err = callbacks.firstPage(ctx, tx, pagination.PerPage)
	case pagination.Next:
		items, err = callbacks.nextPage(ctx, tx, pagination.Cursor, pagination.PerPage)
	default:
		items, err = callbacks.prevPage(ctx, tx, pagination.Cursor, pagination.PerPage)
	}
	if err != nil {
		return handlePostgresError(c, err)
	}
	if items == nil {
		items = []T{}
	}

	var first, last uint64
	if len(items) > 0 {
		first = getID(items[0])
		last = getID(items[len(items)-1])
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: description,
		Result: map[string]any{
			resultKey: items,
			"first":   first,
			"last":    last,
		},
	})
}
