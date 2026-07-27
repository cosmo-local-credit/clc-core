package store

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

type (
	Offering struct {
		ID              uint64    `db:"id"               json:"id"`
		SourceID        *uint64   `db:"source_id"        json:"sourceId"`
		VoucherID       uint64    `db:"voucher_id"       json:"voucherId"`
		VoucherAddress  string    `db:"voucher_address"  json:"voucherAddress"`
		VoucherName     *string   `db:"voucher_name"     json:"voucherName"`
		VoucherSymbol   *string   `db:"voucher_symbol"   json:"voucherSymbol"`
		VoucherDecimals *int32    `db:"voucher_decimals" json:"voucherDecimals"`
		SellerAddress   string    `db:"seller_address"   json:"sellerAddress"`
		CommodityName   string    `db:"commodity_name"   json:"commodityName"`
		CommodityType   string    `db:"commodity_type"   json:"commodityType"`
		Description     string    `db:"description"      json:"description"`
		Quantity        string    `db:"quantity"         json:"quantity"`
		Unit            *string   `db:"unit"             json:"unit"`
		Frequency       *string   `db:"frequency"        json:"frequency"`
		Price           *string   `db:"price"            json:"price"`
		Available       bool      `db:"available"        json:"available"`
		LocationName    *string   `db:"location_name"    json:"locationName"`
		GeoLat          *float64  `db:"geo_lat"          json:"geoLat"`
		GeoLng          *float64  `db:"geo_lng"          json:"geoLng"`
		ImageURL        *string   `db:"image_url"        json:"imageUrl"`
		Tags            []string  `db:"tags"             json:"tags"`
		CreatedAt       time.Time `db:"created_at"       json:"createdAt"`
		UpdatedAt       time.Time `db:"updated_at"       json:"updatedAt"`
	}
)

func (pg *Pg) ListOfferings(ctx context.Context, tx pgx.Tx, limit uint64) ([]Offering, error) {
	var offerings []Offering
	if err := pgxscan.Select(ctx, tx, &offerings, pg.queries.ListOfferings, limit); err != nil {
		return nil, err
	}
	return offerings, nil
}

func (pg *Pg) ListOfferingsNext(ctx context.Context, tx pgx.Tx, cursor uint64, limit uint64) ([]Offering, error) {
	var offerings []Offering
	if err := pgxscan.Select(ctx, tx, &offerings, pg.queries.ListOfferingsNext, cursor, limit); err != nil {
		return nil, err
	}
	return offerings, nil
}

func (pg *Pg) ListOfferingsPrevious(ctx context.Context, tx pgx.Tx, cursor uint64, limit uint64) ([]Offering, error) {
	var offerings []Offering
	if err := pgxscan.Select(ctx, tx, &offerings, pg.queries.ListOfferingsPrev, cursor, limit); err != nil {
		return nil, err
	}
	return offerings, nil
}

func (pg *Pg) ListVoucherOfferings(ctx context.Context, tx pgx.Tx, voucherAddress string, limit uint64) ([]Offering, error) {
	var offerings []Offering
	if err := pgxscan.Select(ctx, tx, &offerings, pg.queries.ListVoucherOfferings, voucherAddress, limit); err != nil {
		return nil, err
	}
	return offerings, nil
}

func (pg *Pg) ListVoucherOfferingsNext(ctx context.Context, tx pgx.Tx, voucherAddress string, cursor uint64, limit uint64) ([]Offering, error) {
	var offerings []Offering
	if err := pgxscan.Select(ctx, tx, &offerings, pg.queries.ListVoucherOfferingsNext, voucherAddress, cursor, limit); err != nil {
		return nil, err
	}
	return offerings, nil
}

func (pg *Pg) ListVoucherOfferingsPrevious(ctx context.Context, tx pgx.Tx, voucherAddress string, cursor uint64, limit uint64) ([]Offering, error) {
	var offerings []Offering
	if err := pgxscan.Select(ctx, tx, &offerings, pg.queries.ListVoucherOfferingsPrev, voucherAddress, cursor, limit); err != nil {
		return nil, err
	}
	return offerings, nil
}

// Pool-scoped offering lists. A pool page needs every offering its allowed
// vouchers publish, which the pool-joined queries return in one page rather
// than one request per voucher.
func (pg *Pg) ListPoolOfferings(ctx context.Context, tx pgx.Tx, poolAddress string, limit uint64) ([]Offering, error) {
	var offerings []Offering
	if err := pgxscan.Select(ctx, tx, &offerings, pg.queries.ListPoolOfferings, poolAddress, limit); err != nil {
		return nil, err
	}
	return offerings, nil
}

func (pg *Pg) ListPoolOfferingsNext(ctx context.Context, tx pgx.Tx, poolAddress string, cursor uint64, limit uint64) ([]Offering, error) {
	var offerings []Offering
	if err := pgxscan.Select(ctx, tx, &offerings, pg.queries.ListPoolOfferingsNext, poolAddress, cursor, limit); err != nil {
		return nil, err
	}
	return offerings, nil
}

func (pg *Pg) ListPoolOfferingsPrevious(ctx context.Context, tx pgx.Tx, poolAddress string, cursor uint64, limit uint64) ([]Offering, error) {
	var offerings []Offering
	if err := pgxscan.Select(ctx, tx, &offerings, pg.queries.ListPoolOfferingsPrev, poolAddress, cursor, limit); err != nil {
		return nil, err
	}
	return offerings, nil
}
