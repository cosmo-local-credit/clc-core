package store

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

const (
	DeployPending   = "PENDING"
	DeployConfirmed = "CONFIRMED"
	DeployFailed    = "FAILED"
)

type (
	DeployedToken struct {
		ID                 uint64    `db:"id"                   json:"id"`
		OTXID              uint64    `db:"otx_id"               json:"otxId"`
		PlatformContractID uint64    `db:"platform_contract_id" json:"platformContractId"`
		Status             string    `db:"status"               json:"status"`
		Address            *string   `db:"address"              json:"address"`
		Symbol             string    `db:"symbol"               json:"symbol"`
		Name               string    `db:"name"                 json:"name"`
		Decimals           uint8     `db:"decimals"             json:"decimals"`
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
		Tags               []string  `db:"tags"                 json:"tags"`
		TrackingID         string    `db:"tracking_id"          json:"trackingId"`
		CreatedAt          time.Time `db:"created_at"           json:"createdAt"`
		UpdatedAt          time.Time `db:"updated_at"           json:"updatedAt"`
	}
)

func (pg *Pg) InsertDeployedToken(ctx context.Context, tx pgx.Tx, dt DeployedToken) (uint64, error) {
	var id uint64

	if err := tx.QueryRow(
		ctx,
		pg.queries.InsertDeployedToken,
		dt.OTXID,
		dt.PlatformContractID,
		dt.Symbol,
		dt.Name,
		dt.Decimals,
		dt.Admin,
		dt.Description,
		dt.Website,
		dt.Email,
		dt.Phone,
		dt.VoucherUoa,
		dt.VoucherValue,
		dt.LocationName,
		dt.GeoLat,
		dt.GeoLng,
	).Scan(&id); err != nil {
		return id, err
	}

	return id, nil
}

func (pg *Pg) ConfirmDeployedToken(ctx context.Context, tx pgx.Tx, otxID uint64, address string) (uint64, error) {
	var id uint64

	if err := tx.QueryRow(ctx, pg.queries.ConfirmDeployedToken, otxID, address).Scan(&id); err != nil {
		return id, err
	}

	return id, nil
}

func (pg *Pg) FailDeployedToken(ctx context.Context, tx pgx.Tx, otxID uint64) error {
	_, err := tx.Exec(ctx, pg.queries.FailDeployedToken, otxID)
	return err
}

func (pg *Pg) InsertDeployedTokenTag(ctx context.Context, tx pgx.Tx, deployedTokenID uint64, tag string) error {
	_, err := tx.Exec(ctx, pg.queries.InsertDeployedTokenTag, deployedTokenID, tag)
	return err
}

func (pg *Pg) GetDeployedTokenByAddress(ctx context.Context, tx pgx.Tx, address string) (DeployedToken, error) {
	var dt DeployedToken

	row, err := tx.Query(ctx, pg.queries.GetDeployedTokenByAddress, address)
	if err != nil {
		return dt, err
	}

	if err := pgxscan.ScanOne(&dt, row); err != nil {
		return dt, err
	}

	return dt, nil
}

func (pg *Pg) GetDeployedTokenByTrackingID(ctx context.Context, tx pgx.Tx, trackingID string) (DeployedToken, error) {
	var dt DeployedToken

	row, err := tx.Query(ctx, pg.queries.GetDeployedTokenByTrackingID, trackingID)
	if err != nil {
		return dt, err
	}

	if err := pgxscan.ScanOne(&dt, row); err != nil {
		return dt, err
	}

	return dt, nil
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
