package store

import (
	"context"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

type (
	// MarketVoucher is the public map/list summary of an active, geolocated
	// voucher. It includes the small icon field used by list rows while omitting
	// larger voucher detail data and the `internal` import artifact, which carries
	// no market visibility semantics.
	MarketVoucher struct {
		ID           uint64  `db:"id"            json:"id"`
		ChainID      int64   `db:"chain_id"      json:"chainId"`
		Address      string  `db:"address"       json:"address"`
		Name         *string `db:"name"          json:"name"`
		Symbol       *string `db:"symbol"        json:"symbol"`
		IconURL      *string `db:"icon_url"      json:"iconUrl"`
		LocationName *string `db:"location_name" json:"locationName"`
		// Coordinates are non-nullable because every query filters incomplete rows.
		GeoLat float64 `db:"geo_lat" json:"geoLat"`
		GeoLng float64 `db:"geo_lng" json:"geoLng"`
		Kind   string  `db:"kind"    json:"kind"`
		// The issuer's marker choice. Null means automatic: the client draws
		// that half from Kind.
		MarkerIcon  *string `db:"marker_icon"  json:"markerIcon"`
		MarkerColor *string `db:"marker_color" json:"markerColor"`
	}

	// MarketVoucherDetail is the public detail view of one voucher, backing the
	// map card and the voucher page. It extends the list summary with contact,
	// banner, and unit-of-account fields, which are too large to ship on every
	// row of a whole-catalog page. `internal` stays off the wire here too.
	//
	// Coordinates are nullable: a voucher without them still has a readable page,
	// it simply renders no map.
	MarketVoucherDetail struct {
		ID      uint64  `db:"id"            json:"id"`
		ChainID int64   `db:"chain_id"      json:"chainId"`
		Address string  `db:"address"       json:"address"`
		Name    *string `db:"name"          json:"name"`
		// ContractName is the immutable ERC20 name. Name is the effective catalog
		// label, which may be an owner-set display-name override.
		ContractName *string  `db:"contract_name" json:"contractName"`
		Symbol       *string  `db:"symbol"        json:"symbol"`
		Decimals     *int     `db:"decimals"      json:"decimals"`
		OwnerAddress *string  `db:"owner_address" json:"ownerAddress"`
		Description  *string  `db:"description"   json:"description"`
		VoucherUOA   *string  `db:"voucher_uoa"   json:"voucherUoa"`
		VoucherValue *string  `db:"voucher_value" json:"voucherValue"`
		LocationName *string  `db:"location_name" json:"locationName"`
		GeoLat       *float64 `db:"geo_lat"       json:"geoLat"`
		GeoLng       *float64 `db:"geo_lng"       json:"geoLng"`
		Email        *string  `db:"email"         json:"email"`
		Website      *string  `db:"website"       json:"website"`
		Phone        *string  `db:"phone"         json:"phone"`
		Terms        *string  `db:"terms"         json:"terms"`
		IconURL      *string  `db:"icon_url"      json:"iconUrl"`
		BannerURL    *string  `db:"banner_url"    json:"bannerUrl"`
		// Never null on the wire: the statement COALESCEs to an empty array, so a
		// client can iterate without a nil check and an untagged voucher is
		// distinguishable from one whose tags failed to load only by the request
		// failing.
		Tags        []string `db:"tags"         json:"tags"`
		Kind        string   `db:"kind"         json:"kind"`
		MarkerIcon  *string  `db:"marker_icon"  json:"markerIcon"`
		MarkerColor *string  `db:"marker_color" json:"markerColor"`
	}

	// MarketVoucherBounds is retained as a source-compatible name for the
	// original voucher-only viewport API. New discovery code uses CatalogBounds.
	MarketVoucherBounds = CatalogBounds
)

// GetMarketVoucherByAddress resolves one active voucher case-insensitively, so
// a shared link works with either checksummed or lowercased casing. Returns
// pgx.ErrNoRows for an unknown or deactivated address, which the handler maps
// to 404 — an inactive voucher is deliberately indistinguishable from a
// missing one.
func (pg *Pg) GetMarketVoucherByAddress(ctx context.Context, tx pgx.Tx, address string) (MarketVoucherDetail, error) {
	var voucher MarketVoucherDetail
	if err := pgxscan.Get(ctx, tx, &voucher, pg.queries.GetMarketVoucherByAddress, address); err != nil {
		return MarketVoucherDetail{}, err
	}
	return voucher, nil
}

func (pg *Pg) ListMarketVouchers(ctx context.Context, tx pgx.Tx, bounds *MarketVoucherBounds, limit uint64) ([]MarketVoucher, error) {
	var vouchers []MarketVoucher
	if bounds == nil {
		if err := pgxscan.Select(ctx, tx, &vouchers, pg.queries.ListMarketVouchers, limit); err != nil {
			return nil, err
		}
		return vouchers, nil
	}
	if err := pgxscan.Select(ctx, tx, &vouchers, pg.queries.ListMarketVouchersBounded,
		bounds.MinLng, bounds.MinLat, bounds.MaxLng, bounds.MaxLat, limit); err != nil {
		return nil, err
	}
	return vouchers, nil
}

func (pg *Pg) ListMarketVouchersNext(ctx context.Context, tx pgx.Tx, bounds *MarketVoucherBounds, cursor uint64, limit uint64) ([]MarketVoucher, error) {
	var vouchers []MarketVoucher
	if bounds == nil {
		if err := pgxscan.Select(ctx, tx, &vouchers, pg.queries.ListMarketVouchersNext, cursor, limit); err != nil {
			return nil, err
		}
		return vouchers, nil
	}
	if err := pgxscan.Select(ctx, tx, &vouchers, pg.queries.ListMarketVouchersNextBounded,
		bounds.MinLng, bounds.MinLat, bounds.MaxLng, bounds.MaxLat, cursor, limit); err != nil {
		return nil, err
	}
	return vouchers, nil
}

func (pg *Pg) ListMarketVouchersPrevious(ctx context.Context, tx pgx.Tx, bounds *MarketVoucherBounds, cursor uint64, limit uint64) ([]MarketVoucher, error) {
	var vouchers []MarketVoucher
	if bounds == nil {
		if err := pgxscan.Select(ctx, tx, &vouchers, pg.queries.ListMarketVouchersPrev, cursor, limit); err != nil {
			return nil, err
		}
		return vouchers, nil
	}
	if err := pgxscan.Select(ctx, tx, &vouchers, pg.queries.ListMarketVouchersPrevBounded,
		bounds.MinLng, bounds.MinLat, bounds.MaxLng, bounds.MaxLat, cursor, limit); err != nil {
		return nil, err
	}
	return vouchers, nil
}
