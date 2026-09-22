package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

var errInvalidVoucherStatus = errors.New("status must be one of: all, active, retired")

// The console's voucher reads, and the restore that depends on them.
//
// These exist because the public catalog cannot answer for a retired voucher by
// design: GET /api/v2/vouchers filters `v.active` and complete coordinates, and
// GET /api/v2/vouchers/:address returns a 404 byte-identical to an address that
// was never in the database. Without a read that ignores the gate, retiring a
// voucher would also retire the only screen an operator could restore it from.
//
// They are mounted under /api/v2/admin rather than added as a parameter to the
// public routes: those are registered on the root router precisely so a
// signed-out visitor can browse the Market tab, so they carry no caller identity
// to gate a wider view on, and giving them one would put an authentication
// decision on the one path documented as having none.

// parseAdminVoucherStatus reads the optional `status` filter. The value selects
// a constant predicate through a closed enum; it never reaches SQL as text.
func parseAdminVoucherStatus(c *echo.Context) (store.AdminVoucherStatus, error) {
	switch strings.ToLower(strings.TrimSpace(c.Request().URL.Query().Get("status"))) {
	case "", "all":
		return store.AdminVoucherStatusAll, nil
	case "active":
		return store.AdminVoucherStatusActive, nil
	case "retired":
		return store.AdminVoucherStatusRetired, nil
	default:
		return store.AdminVoucherStatusAll, errInvalidVoucherStatus
	}
}

// adminVouchersListHandler lists the catalog without the public visibility gate,
// narrowed by the same `q`, `tags`, `tagMatch` and `owner` parameters the market
// list takes, plus `status`. No `bbox`: the console lists, it does not map, and a
// viewport filter would silently exclude the ungeolocated rows this endpoint
// exists to reach.
func (a *API) adminVouchersListHandler(c *echo.Context) error {
	req := apiresp.CursorPaginationRequest{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}

	search, err := parseCatalogSearch(c)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	if search.filter.Owner, err = addressFilterParam(c, "owner"); err != nil {
		return respondCatalogSearchError(c, err)
	}
	status, err := parseAdminVoucherStatus(c)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}

	pagination := normalizePaginationWithMax(req, catalogMaxPerPage)

	return cursorPaginatedCatalogList(c, a, paginatedCatalogListCallbacks[store.AdminVoucher]{
		firstPage: func(ctx context.Context, tx pgx.Tx, limit uint64) ([]store.AdminVoucher, error) {
			return a.store.ListAdminVouchers(ctx, tx, search.filter, status, store.CatalogFirstPage, 0, limit)
		},
		nextPage: func(ctx context.Context, tx pgx.Tx, cursor, limit uint64) ([]store.AdminVoucher, error) {
			return a.store.ListAdminVouchers(ctx, tx, search.filter, status, store.CatalogNextPage, cursor, limit)
		},
		prevPage: func(ctx context.Context, tx pgx.Tx, cursor, limit uint64) ([]store.AdminVoucher, error) {
			return a.store.ListAdminVouchers(ctx, tx, search.filter, status, store.CatalogPrevPage, cursor, limit)
		},
	}, pagination, "vouchers", "Vouchers", func(v store.AdminVoucher) uint64 {
		return v.ID
	}, catalogListHooks{
		validate: func(tx pgx.Tx) (bool, error) {
			return a.rejectUnknownTags(c, tx, search.filter.Tags)
		},
	})
}

// adminVoucherByAddressHandler resolves one voucher whatever its visibility.
func (a *API) adminVoucherByAddressHandler(c *echo.Context) error {
	address, handled, err := adminUserAddress(c)
	if handled || err != nil {
		return err
	}

	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	voucher, err := a.store.GetAdminVoucherByAddress(ctx, tx, address)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	// Not setMarketCacheHeaders: the public market responses are cacheable
	// because they are identical for every caller, and this one is not — it is
	// scoped to a role and shows rows the cache must never serve to anyone else.
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Voucher",
		Result: map[string]any{
			"voucher": voucher,
		},
	})
}

// adminRestoreVoucherHandler returns a retired voucher to the public catalog.
//
// Superadmin-only, and deliberately not the mirror image of retire: an owner may
// take their own voucher out of circulation, but putting one back is an operator
// decision, so there is no owner-facing route and no toggle for an owner to flip
// back and forth.
//
// Thin, like adminUpdateVoucherProfileHandler: requireSuperAdmin on the group has
// already decided the caller may act, so there is no ownership check to make and
// no reason to lock the row for one. Restoring an already-active voucher is not
// an error — the caller asked for a state the row is already in, and reporting
// 409 would only make the console handle a case with no user-visible difference.
func (a *API) adminRestoreVoucherHandler(c *echo.Context) error {
	address, handled, err := adminUserAddress(c)
	if handled || err != nil {
		return err
	}

	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	voucher, err := a.store.GetAdminVoucherByAddress(ctx, tx, address)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := a.store.SetVoucherActive(ctx, tx, voucher.ID, true); err != nil {
		return handlePostgresError(c, err)
	}

	// Read back through the admin statement rather than the public one so the
	// response carries `active` and the console can seed its cache with the row
	// it will render.
	restored, err := a.store.GetAdminVoucherByAddress(ctx, tx, address)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Voucher restored",
		Result: map[string]any{
			"voucher": restored,
		},
	})
}
