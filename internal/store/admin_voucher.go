package store

import (
	"context"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

// The console's voucher catalog.
//
// Everything public reads the catalog through `v.active`: the market list, the
// detail, the offering lists and the facet vocabulary all filter on it, and the
// map statements additionally require complete coordinates. That is the right
// contract for a public catalog and the wrong one for the screen an operator
// restores a voucher from — retiring one would otherwise remove the only screen
// that could undo it.
//
// So these statements deliberately carry neither predicate, and are mounted
// under /api/v2/admin where requireSuperAdmin has already decided the caller may
// see a retired voucher. The public path is untouched: the composer below is
// separate from composeMarketVoucherPage rather than a flag on it, so no change
// here can widen what a signed-out visitor sees.

type (
	// AdminVoucherStatus narrows a console page to one side of the visibility
	// gate. It is parsed from a closed set of request strings into this enum
	// before it reaches SQL; no request data becomes a predicate.
	AdminVoucherStatus uint8

	// AdminVoucher is one row of the console's voucher list: the market summary
	// plus the visibility flag the operator is triaging by.
	//
	// Coordinates are nullable here and not on MarketVoucher, because the reason
	// they were non-nullable there was that every public statement filters
	// incomplete rows. This one does not — an ungeolocated voucher is exactly
	// the kind the console has to be able to reach.
	AdminVoucher struct {
		ID           uint64   `db:"id"            json:"id"`
		ChainID      int64    `db:"chain_id"      json:"chainId"`
		Address      string   `db:"address"       json:"address"`
		Name         *string  `db:"name"          json:"name"`
		Symbol       *string  `db:"symbol"        json:"symbol"`
		IconURL      *string  `db:"icon_url"      json:"iconUrl"`
		LocationName *string  `db:"location_name" json:"locationName"`
		GeoLat       *float64 `db:"geo_lat"       json:"geoLat"`
		GeoLng       *float64 `db:"geo_lng"       json:"geoLng"`
		Kind         string   `db:"kind"          json:"kind"`
		Active       bool     `db:"active"        json:"active"`
	}

	// AdminVoucherDetail is the console's read of one voucher: the public detail
	// projection plus `active`, and without the `active` predicate.
	AdminVoucherDetail struct {
		MarketVoucherDetail

		Active bool `db:"active" json:"active"`
	}
)

const (
	// AdminVoucherStatusAll is the default: the console lists the whole catalog
	// and renders each row's status, because an operator arriving at the screen
	// is usually looking for a voucher, not for a status.
	AdminVoucherStatusAll AdminVoucherStatus = 0
	// AdminVoucherStatusActive lists only vouchers the public can see.
	AdminVoucherStatusActive AdminVoucherStatus = 1
	// AdminVoucherStatusRetired lists only retired vouchers — the restore queue.
	AdminVoucherStatusRetired AdminVoucherStatus = 2

	// adminVoucherPageProjection hydrates one already-selected page of voucher IDs.
	// Same candidate-first shape as marketVoucherPageProjection, and the same
	// derivation of `kind`, with `active` added.
	adminVoucherPageProjection = `
SELECT v.id, v.chain_id, v.address, COALESCE(v.display_name, v.name) AS name, v.symbol, v.icon_url,
    v.location_name, v.geo_lat, v.geo_lng, v.active,
    'voucher'::TEXT AS kind
FROM page
INNER JOIN vouchers v ON v.id = page.id
ORDER BY v.id DESC;`
)

// predicate returns the visibility fragment for a status. It is a compile-time
// constant per enum value, never built from request text.
func (s AdminVoucherStatus) predicate() string {
	switch s {
	case AdminVoucherStatusActive:
		return "v.active"
	case AdminVoucherStatusRetired:
		return "NOT v.active"
	case AdminVoucherStatusAll:
		return ""
	default:
		return ""
	}
}

// composeAdminVoucherPage builds one page of the console's catalog.
//
// The two driving shapes and the reasoning behind them are
// composeMarketVoucherPage's: with a search term the match set is materialized
// first so the trigram index keeps driving, and without one the backward
// primary-key walk stops as soon as it has a page. The difference is visibility
// — one optional status predicate in place of the public pair — and no viewport,
// since the console lists rather than maps.
func composeAdminVoucherPage(
	filter CatalogFilter,
	status AdminVoucherStatus,
	page CatalogPage,
	cursor, limit uint64,
) composed {
	args := &sqlArgs{}

	if filter.Term == "" {
		body := "WITH page AS MATERIALIZED (\n" +
			"    SELECT v.id\n" +
			"    FROM vouchers v" +
			whereClause(
				status.predicate(),
				ownerPredicate(args, "v.owner_address", filter),
				voucherTagPredicate(args, filter),
				cursorPredicate(args, "v.id", page, cursor),
			) +
			"\n    ORDER BY v.id " + pageOrder(page) +
			"\n    LIMIT " + args.bind(limit) +
			"\n)"
		return composed{sql: body + adminVoucherPageProjection, args: args.values}
	}

	body := "WITH matches AS MATERIALIZED (\n" +
		"    SELECT v.id\n" +
		"    FROM vouchers v" +
		whereClause(
			status.predicate(),
			ownerPredicate(args, "v.owner_address", filter),
			searchPredicate(args, filter, "v"),
			voucherTagPredicate(args, filter),
		) +
		"\n),\npage AS MATERIALIZED (\n" +
		"    SELECT m.id\n" +
		"    FROM matches m" +
		whereClause(cursorPredicate(args, "m.id", page, cursor)) +
		"\n    ORDER BY m.id " + pageOrder(page) +
		"\n    LIMIT " + args.bind(limit) +
		"\n)"
	return composed{sql: body + adminVoucherPageProjection, args: args.values}
}

// ListAdminVouchers returns one page of the console's voucher catalog.
//
// Unlike ListMarketVouchersFiltered there is no static-statement fallback for
// the unfiltered case: the public fallback exists to preserve the map's measured
// bulk-import plans, and the console pages a hundred rows at a time.
func (pg *Pg) ListAdminVouchers(
	ctx context.Context,
	tx pgx.Tx,
	filter CatalogFilter,
	status AdminVoucherStatus,
	page CatalogPage,
	cursor, limit uint64,
) ([]AdminVoucher, error) {
	query := composeAdminVoucherPage(filter, status, page, cursor, limit)

	var vouchers []AdminVoucher
	if err := pgxscan.Select(ctx, tx, &vouchers, query.sql, query.args...); err != nil {
		return nil, err
	}
	return vouchers, nil
}

// GetAdminVoucherByAddress resolves one voucher case-insensitively regardless of
// its visibility. Returns pgx.ErrNoRows only for an address the catalog does not
// hold — a retired voucher resolves here, which is the whole point.
func (pg *Pg) GetAdminVoucherByAddress(ctx context.Context, tx pgx.Tx, address string) (AdminVoucherDetail, error) {
	var voucher AdminVoucherDetail
	if err := pgxscan.Get(ctx, tx, &voucher, pg.queries.AdminGetVoucherByAddress, address); err != nil {
		return AdminVoucherDetail{}, err
	}
	return voucher, nil
}
