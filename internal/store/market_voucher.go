package store

import (
	"context"
	"errors"
	"math"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

// MarketVoucher is the public map/list summary of an active, geolocated
// voucher. It deliberately omits voucher detail data, iconUrl, and the
// `internal` import artifact, which carries no market visibility semantics.
type MarketVoucher struct {
	ID           uint64  `db:"id"            json:"id"`
	ChainID      int64   `db:"chain_id"      json:"chainId"`
	Address      string  `db:"address"       json:"address"`
	Name         *string `db:"name"          json:"name"`
	Symbol       *string `db:"symbol"        json:"symbol"`
	LocationName *string `db:"location_name" json:"locationName"`
	// Coordinates are non-nullable because every query filters incomplete rows.
	GeoLat float64 `db:"geo_lat" json:"geoLat"`
	GeoLng float64 `db:"geo_lng" json:"geoLng"`
	Kind   string  `db:"kind"    json:"kind"`
}

// MarketVoucherBounds is an optional viewport filter. A single box may never
// cross the antimeridian; a caller whose viewport does must split it into two
// requests and merge the results.
type MarketVoucherBounds struct {
	MinLng float64
	MinLat float64
	MaxLng float64
	MaxLat float64
}

var errInvalidBounds = errors.New("bbox must be minLng,minLat,maxLng,maxLat within [-180,180] and [-90,90] with min <= max")

func (b MarketVoucherBounds) Validate() error {
	for _, v := range [4]float64{b.MinLng, b.MinLat, b.MaxLng, b.MaxLat} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return errInvalidBounds
		}
	}
	if b.MinLng < -180 || b.MaxLng > 180 || b.MinLat < -90 || b.MaxLat > 90 {
		return errInvalidBounds
	}
	if b.MinLng > b.MaxLng || b.MinLat > b.MaxLat {
		return errInvalidBounds
	}
	return nil
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
