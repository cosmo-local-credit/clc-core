package store

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

type DeployedToken struct {
	ID                  uint64    `db:"id"                    json:"id"`
	OTXID               uint64    `db:"otx_id"                json:"otxId"`
	PlatformContractID  uint64    `db:"platform_contract_id"  json:"platformContractId"`
	Address             string    `db:"address"               json:"address"`
	Symbol              string    `db:"symbol"                json:"symbol"`
	Name                string    `db:"name"                  json:"name"`
	Admin               string    `db:"admin"                 json:"admin"`
	TrackingID          string    `db:"tracking_id"           json:"trackingId"`
	CreatedAt           time.Time `db:"created_at"            json:"createdAt"`
	UpdatedAt           time.Time `db:"updated_at"            json:"updatedAt"`
}

func (pg *Pg) InsertDeployedToken(ctx context.Context, tx pgx.Tx, dt DeployedToken) (uint64, error) {
	var id uint64

	if err := tx.QueryRow(
		ctx,
		pg.queries.InsertDeployedToken,
		dt.OTXID,
		dt.PlatformContractID,
		dt.Address,
		dt.Symbol,
		dt.Name,
		dt.Admin,
	).Scan(&id); err != nil {
		return id, err
	}

	return id, nil
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
