package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

type (
	DispatchTx struct {
	ID        uint64    `db:"id"         json:"id"`
	OTXID     uint64    `db:"otx_id"     json:"otxId"`
	Status    string    `db:"status"     json:"status"`
	CreatedAt time.Time `db:"created_at" json:"createdAt"`
	UpdatedAt time.Time `db:"updated_at" json:"updatedAt"`
}
)

const (
	PENDING                 = "PENDING"
	IN_NETWORK              = "IN_NETWORK"
	SUCCESS                 = "SUCCESS"
	REVERTED                = "REVERTED"
	LOW_NONCE               = "LOW_NONCE"
	NO_GAS                  = "NO_GAS"
	LOW_GAS_PRICE           = "LOW_GAS_PRICE"
	REPLACEMENT_UNDERPRICED = "REPLACEMENT_UNDERPRICED"
	NETWORK_ERROR           = "NETWORK_ERROR"
	UNKNOWN_RPC_ERROR       = "UNKNOWN_RPC_ERROR"
)

func (pg *Pg) InsertDispatchTx(ctx context.Context, tx pgx.Tx, dispatchTx DispatchTx) error {
	_, err := tx.Exec(ctx, pg.queries.InsertDispatchTx, dispatchTx.OTXID, dispatchTx.Status)
	return err
}

func (pg *Pg) UpdateDispatchTxStatus(ctx context.Context, tx pgx.Tx, dispatchTx DispatchTx) error {
	_, err := tx.Exec(ctx, pg.queries.UpdateDispatchTxStatus, dispatchTx.Status, dispatchTx.OTXID)
	return err
}
