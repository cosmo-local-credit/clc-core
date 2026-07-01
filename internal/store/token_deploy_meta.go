package store

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

type (
	TokenDeployMeta struct {
	ID                 uint64    `db:"id"                   json:"id"`
	OTXID              uint64    `db:"otx_id"               json:"otxId"`
	PlatformContractID uint64    `db:"platform_contract_id" json:"platformContractId"`
	TokenName          string    `db:"token_name"           json:"tokenName"`
	TokenSymbol        string    `db:"token_symbol"         json:"tokenSymbol"`
	Admin              string    `db:"admin"                json:"admin"`
	Description        *string   `db:"description"          json:"description"`
	Website            *string   `db:"website"              json:"website"`
	Email              *string   `db:"email"                json:"email"`
	Phone              *string   `db:"phone"                json:"phone"`
	VoucherUoa         *string   `db:"voucher_uoa"          json:"voucherUoa"`
	VoucherValue       *float64  `db:"voucher_value"        json:"voucherValue"`
	LocationName       *string   `db:"location_name"        json:"locationName"`
	GeoLat             *float64  `db:"geo_lat"              json:"geoLat"`
	GeoLng             *float64  `db:"geo_lng"              json:"geoLng"`
	CreatedAt          time.Time `db:"created_at"           json:"createdAt"`
	UpdatedAt          time.Time `db:"updated_at"           json:"updatedAt"`
}
)

func (pg *Pg) InsertTokenDeployMeta(ctx context.Context, tx pgx.Tx, meta TokenDeployMeta) error {
	_, err := tx.Exec(
		ctx,
		pg.queries.InsertTokenDeployMeta,
		meta.OTXID,
		meta.PlatformContractID,
		meta.TokenName,
		meta.TokenSymbol,
		meta.Admin,
		meta.Description,
		meta.Website,
		meta.Email,
		meta.Phone,
		meta.VoucherUoa,
		meta.VoucherValue,
		meta.LocationName,
		meta.GeoLat,
		meta.GeoLng,
	)
	return err
}

func (pg *Pg) InsertTokenDeployMetaTag(ctx context.Context, tx pgx.Tx, otxID uint64, tag string) error {
	_, err := tx.Exec(ctx, pg.queries.InsertTokenDeployMetaTag, otxID, tag)
	return err
}

func (pg *Pg) GetTokenDeployMetaByOTXID(ctx context.Context, tx pgx.Tx, otxID uint64) (TokenDeployMeta, error) {
	var meta TokenDeployMeta

	row, err := tx.Query(ctx, pg.queries.GetTokenDeployMetaByOTXID, otxID)
	if err != nil {
		return meta, err
	}

	if err := pgxscan.ScanOne(&meta, row); err != nil {
		return meta, err
	}

	return meta, nil
}

func (pg *Pg) GetTokenDeployMetaTags(ctx context.Context, tx pgx.Tx, otxID uint64) ([]string, error) {
	rows, err := tx.Query(ctx, pg.queries.GetTokenDeployMetaTags, otxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tags []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, err
		}
		tags = append(tags, tag)
	}
	return tags, rows.Err()
}

func (pg *Pg) ListTokenTags(ctx context.Context, tx pgx.Tx) ([]string, error) {
	rows, err := tx.Query(ctx, pg.queries.ListTokenTags)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tags []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, err
		}
		tags = append(tags, tag)
	}
	return tags, rows.Err()
}
