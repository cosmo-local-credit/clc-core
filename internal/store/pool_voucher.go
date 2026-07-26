package store

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

type PoolVoucher struct {
	ID                  uint64    `db:"id"                     json:"id"`
	ChainID             int64     `db:"chain_id"               json:"chainId"`
	Address             string    `db:"address"                json:"address"`
	Name                *string   `db:"name"                   json:"name"`
	Symbol              *string   `db:"symbol"                 json:"symbol"`
	Decimals            *int32    `db:"decimals"               json:"decimals"`
	OwnerAddress        *string   `db:"owner_address"          json:"ownerAddress"`
	VoucherType         *string   `db:"voucher_type"           json:"voucherType"`
	SinkAddress         *string   `db:"sink_address"           json:"sinkAddress"`
	RedemptionAddress   *string   `db:"redemption_address"     json:"redemptionAddress"`
	Description         *string   `db:"description"            json:"description"`
	VoucherUOA          *string   `db:"voucher_uoa"            json:"voucherUoa"`
	VoucherValue        *string   `db:"voucher_value"          json:"voucherValue"`
	LocationName        *string   `db:"location_name"          json:"locationName"`
	GeoLat              *float64  `db:"geo_lat"                json:"geoLat"`
	GeoLng              *float64  `db:"geo_lng"                json:"geoLng"`
	Email               *string   `db:"email"                  json:"email"`
	Website             *string   `db:"website"                json:"website"`
	Phone               *string   `db:"phone"                  json:"phone"`
	IconURL             *string   `db:"icon_url"               json:"iconUrl"`
	BannerURL           *string   `db:"banner_url"             json:"bannerUrl"`
	Terms               *string   `db:"terms"                  json:"terms"`
	ContractVersion     *string   `db:"contract_version"       json:"contractVersion"`
	Active              bool      `db:"active"                 json:"active"`
	Internal            bool      `db:"internal"               json:"internal"`
	IsAllowed           bool      `db:"is_allowed"             json:"isAllowed"`
	TokenLimit          *string   `db:"token_limit"            json:"tokenLimit"`
	SyncedAt            time.Time `db:"synced_at"              json:"syncedAt"`
	OracleFeedID        *uint64   `db:"oracle_feed_id"          json:"oracleFeedId"`
	OracleName          *string   `db:"oracle_name"             json:"oracleName"`
	OracleAddress       *string   `db:"oracle_address"          json:"oracleAddress"`
	OfferCount          uint64    `db:"offer_count"             json:"offerCount"`
	AvailableOfferCount uint64    `db:"available_offer_count"   json:"availableOfferCount"`
	Tags                []string  `db:"tags"                    json:"tags"`
	CreatedAt           time.Time `db:"created_at"              json:"createdAt"`
	UpdatedAt           time.Time `db:"updated_at"              json:"updatedAt"`
}

func (pg *Pg) ListPoolVouchers(ctx context.Context, tx pgx.Tx, poolAddress string, limit uint64) ([]PoolVoucher, error) {
	var vouchers []PoolVoucher
	if err := pgxscan.Select(ctx, tx, &vouchers, pg.queries.ListPoolVouchers, poolAddress, limit); err != nil {
		return nil, err
	}
	return vouchers, nil
}

func (pg *Pg) ListPoolVouchersNext(ctx context.Context, tx pgx.Tx, poolAddress string, cursor uint64, limit uint64) ([]PoolVoucher, error) {
	var vouchers []PoolVoucher
	if err := pgxscan.Select(ctx, tx, &vouchers, pg.queries.ListPoolVouchersNext, poolAddress, cursor, limit); err != nil {
		return nil, err
	}
	return vouchers, nil
}

func (pg *Pg) ListPoolVouchersPrevious(ctx context.Context, tx pgx.Tx, poolAddress string, cursor uint64, limit uint64) ([]PoolVoucher, error) {
	var vouchers []PoolVoucher
	if err := pgxscan.Select(ctx, tx, &vouchers, pg.queries.ListPoolVouchersPrev, poolAddress, cursor, limit); err != nil {
		return nil, err
	}
	return vouchers, nil
}
