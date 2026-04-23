package store

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

type TokenDeployMeta struct {
	ID                  uint64    `db:"id"                    json:"id"`
	OTXID               uint64    `db:"otx_id"                json:"otxId"`
	PlatformContractID  uint64    `db:"platform_contract_id"  json:"platformContractId"`
	TokenName           string    `db:"token_name"            json:"tokenName"`
	TokenSymbol         string    `db:"token_symbol"          json:"tokenSymbol"`
	Admin               string    `db:"admin"                 json:"admin"`
	CreatedAt           time.Time `db:"created_at"            json:"createdAt"`
	UpdatedAt           time.Time `db:"updated_at"            json:"updatedAt"`
}

func (pg *Pg) InsertTokenDeployMeta(ctx context.Context, tx pgx.Tx, meta TokenDeployMeta) error {
	_, err := tx.Exec(
		ctx,
		pg.queries.InsertTokenDeployMeta,
		meta.OTXID,
		meta.PlatformContractID,
		meta.TokenName,
		meta.TokenSymbol,
		meta.Admin,
	)
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
