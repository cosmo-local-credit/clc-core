package store

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

type UpgradeMeta struct {
	ID               uint64    `db:"id"                json:"id"`
	OTXID            uint64    `db:"otx_id"            json:"otxId"`
	ProxyAddress     string    `db:"proxy_address"     json:"proxyAddress"`
	ContractType     string    `db:"contract_type"     json:"contractType"`
	NewImplementation string   `db:"new_implementation" json:"newImplementation"`
	Version          string    `db:"version"           json:"version"`
	CreatedAt        time.Time `db:"created_at"        json:"createdAt"`
}

func (pg *Pg) InsertUpgradeMeta(ctx context.Context, tx pgx.Tx, meta UpgradeMeta) error {
	_, err := tx.Exec(
		ctx,
		pg.queries.InsertUpgradeMeta,
		meta.OTXID,
		meta.ProxyAddress,
		meta.ContractType,
		meta.NewImplementation,
		meta.Version,
	)
	return err
}

func (pg *Pg) GetUpgradeMetaByOTXID(ctx context.Context, tx pgx.Tx, otxID uint64) (UpgradeMeta, error) {
	var meta UpgradeMeta

	row, err := tx.Query(ctx, pg.queries.GetUpgradeMetaByOTXID, otxID)
	if err != nil {
		return meta, err
	}

	if err := pgxscan.ScanOne(&meta, row); err != nil {
		return meta, err
	}

	return meta, nil
}
