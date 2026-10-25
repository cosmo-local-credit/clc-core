package store

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

type (
	MarketplaceResult struct {
		Kind                 string    `db:"kind" json:"kind"`
		ID                   uint64    `db:"id" json:"id"`
		ChainID              *int64    `db:"chain_id" json:"chainId"`
		Address              *string   `db:"address" json:"address"`
		Name                 string    `db:"name" json:"name"`
		Description          *string   `db:"description" json:"description"`
		Symbol               *string   `db:"symbol" json:"symbol"`
		ImageURL             *string   `db:"image_url" json:"imageUrl"`
		MarkerIcon           *string   `db:"marker_icon" json:"markerIcon"`
		MarkerColor          *string   `db:"marker_color" json:"markerColor"`
		VoucherKind          *string   `db:"voucher_kind" json:"voucherKind"`
		LocationName         *string   `db:"location_name" json:"locationName"`
		GeoLat               float64   `db:"geo_lat" json:"geoLat"`
		GeoLng               float64   `db:"geo_lng" json:"geoLng"`
		DistanceMeters       *float64  `db:"distance_meters" json:"distanceMeters"`
		Tags                 []string  `db:"tags" json:"tags"`
		VoucherAddress       *string   `db:"voucher_address" json:"voucherAddress"`
		VoucherName          *string   `db:"voucher_name" json:"voucherName"`
		VoucherSymbol        *string   `db:"voucher_symbol" json:"voucherSymbol"`
		VoucherUOA           *string   `db:"voucher_uoa" json:"voucherUoa"`
		PriceInUnitOfAccount *string   `db:"price_in_unit_of_account" json:"priceInUnitOfAccount"`
		Unit                 *string   `db:"unit" json:"unit"`
		PoolCount            *uint64   `db:"pool_count" json:"poolCount"`
		VoucherCount         *uint64   `db:"voucher_count" json:"voucherCount"`
		Rank                 int       `db:"rank" json:"-"`
		CreatedAt            time.Time `db:"created_at" json:"createdAt"`
		UpdatedAt            time.Time `db:"updated_at" json:"updatedAt"`
		TagFacets            []byte    `db:"tag_facets" json:"-"`
	}

	MarketplaceCursor struct {
		Rank      int       `json:"rank"`
		Distance  float64   `json:"distance"`
		UpdatedAt time.Time `json:"updatedAt"`
		Kind      string    `json:"kind"`
		ID        uint64    `json:"id"`
		Origin    string    `json:"origin"`
	}
)

func (pg *Pg) ListMarketplace(
	ctx context.Context,
	tx pgx.Tx,
	filter CatalogFilter,
	kinds []string,
	nearby *CatalogLocation,
	cursor MarketplaceCursor,
	limit uint64,
	includeFacets bool,
) ([]MarketplaceResult, error) {
	lat, lng, minLng, minLat, maxLng, maxLat := 0.0, 0.0, 0.0, 0.0, 0.0, 0.0
	isNearby := nearby != nil
	if nearby != nil {
		lat, lng = nearby.Lat, nearby.Lng
		bounds := nearby.bounds()
		minLng, minLat, maxLng, maxLat = bounds.MinLng, bounds.MinLat, bounds.MaxLng, bounds.MaxLat
	}
	hasBounds, boundMinLng, boundMinLat, boundMaxLng, boundMaxLat, hasSecond, secondMinLng, secondMinLat, secondMaxLng, secondMaxLat := catalogBoundsQueryArgs(filter.Bounds)
	rows := []MarketplaceResult{}
	err := pgxscan.Select(ctx, tx, &rows, pg.queries.ListMarketplace,
		filter.likeTerm(), filter.prefixOnly(), filter.Tags, filter.TagMatch == TagMatchAll,
		filter.PoolIDs, kinds, isNearby, lat, lng, minLng, minLat, maxLng, maxLat,
		cursor.Rank, cursor.Distance, cursor.UpdatedAt, cursor.Kind, cursor.ID, limit,
		includeFacets,
		hasBounds, boundMinLng, boundMinLat, boundMaxLng, boundMaxLat,
		hasSecond, secondMinLng, secondMinLat, secondMaxLng, secondMaxLat,
	)
	return rows, err
}
