package store

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

type OTX struct {
	ID             uint64    `db:"id" json:"id"`
	TrackingID     string    `db:"tracking_id" json:"trackingId"`
	OTXType        string    `db:"otx_type" json:"otxType"`
	SignerAccount  string    `db:"public_key" json:"signerAccount"`
	RawTx          string    `db:"raw_tx" json:"rawTx"`
	TxHash         string    `db:"tx_hash" json:"txHash"`
	Nonce          uint64    `db:"nonce" json:"nonce"`
	Replaced       bool      `db:"replaced" json:"replaced"`
	CreatedAt      time.Time `db:"created_at" json:"createdAt"`
	UpdatedAt      time.Time `db:"updated_at" json:"updatedAt"`
	DispatchStatus string    `db:"status" json:"status"`
}

const (
	GAS_REFILL                       = "GAS_REFILL"
	ACCOUNT_REGISTER                 = "ACCOUNT_REGISTER"
	GAS_TRANSFER                     = "GAS_TRANSFER"
	TOKEN_TRANSFER                   = "TOKEN_TRANSFER"
	TOKEN_APPROVE                    = "TOKEN_APPROVE"
	TOKEN_SWEEP                      = "TOKEN_SWEEP"
	POOL_SWAP                        = "POOL_SWAP"
	POOL_DEPOSIT                     = "POOL_DEPOSIT"
	OTHER_MANUAL                     = "OTHER_MANUAL"
	GENERIC_SIGN                     = "GENERIC_SIGN"
	STANDARD_TOKEN_DEPLOY            = "STANDARD_TOKEN_DEPLOY"
	TRANSFER_OWNERSHIP               = "TRANSFER_OWNERSHIP"
	TOKEN_INDEX_ADD                  = "TOKEN_INDEX_ADD"
	POOL_DEPLOY                      = "POOL_DEPLOY"
	TOKEN_INDEX_DEPLOY               = "TOKEN_INDEX_DEPLOY"
	LIMITER_DEPLOY                   = "LIMITER_DEPLOY"
	SWAPPOOL_DEPLOY                  = "SWAPPOOL_DEPLOY"
	PRICEINDEXQUOTER_DEPLOY          = "PRICEINDEXQUOTER_DEPLOY"
	POOL_INDEX_ADD                   = "POOL_INDEX_ADD"
	SET_QUOTER                       = "SET_QUOTER"
	DEMURRAGE_TOKEN_DEPLOY           = "DEMURRAGE_TOKEN_DEPLOY"
	EXPIRING_TOKEN_DEPLOY            = "EXPIRING_TOKEN_DEPLOY"
	TOKEN_PROXY_DEPLOY = "TOKEN_PROXY_DEPLOY"
	TOKEN_IMPL_DEPLOY  = "TOKEN_IMPL_DEPLOY"
)

func (pg *Pg) InsertOTX(ctx context.Context, tx pgx.Tx, otx OTX) (uint64, error) {
	var id uint64

	if err := tx.QueryRow(
		ctx,
		pg.queries.InsertOTX,
		otx.TrackingID,
		otx.OTXType,
		otx.SignerAccount,
		otx.RawTx,
		otx.TxHash,
		otx.Nonce,
	).Scan(&id); err != nil {
		return id, err
	}

	return id, nil
}

func (pg *Pg) GetOTXByTxHash(ctx context.Context, tx pgx.Tx, txHash string) (OTX, error) {
	var otx OTX

	row, err := tx.Query(ctx, pg.queries.GetOTXByTxHash, txHash)
	if err != nil {
		return otx, err
	}

	if err := pgxscan.ScanOne(&otx, row); err != nil {
		return otx, err
	}

	return otx, nil
}

func (pg *Pg) GetOTXByTrackingID(ctx context.Context, tx pgx.Tx, trackingID string) ([]*OTX, error) {
	var otx []*OTX

	if err := pgxscan.Select(ctx, tx, &otx, pg.queries.GetOTXByTrackingID, trackingID); err != nil {
		return otx, err
	}

	return otx, nil
}

func (pg *Pg) GetOTXByAccount(ctx context.Context, tx pgx.Tx, publicKey string, limit int) ([]*OTX, error) {
	var otx []*OTX

	if err := pgxscan.Select(ctx, tx, &otx, pg.queries.GetOTXByAccount, publicKey, limit); err != nil {
		return nil, err
	}

	return otx, nil
}

func (pg *Pg) GetOTXByAccountNext(ctx context.Context, tx pgx.Tx, publicKey string, cursor int, limit int) ([]*OTX, error) {
	var otx []*OTX

	if err := pgxscan.Select(ctx, tx, &otx, pg.queries.GetOTXByAccountNext, publicKey, cursor, limit); err != nil {
		return nil, err
	}

	return otx, nil
}

func (pg *Pg) GetOTXByAccountPrevious(ctx context.Context, tx pgx.Tx, publicKey string, cursor int, limit int) ([]*OTX, error) {
	var otx []*OTX

	if err := pgxscan.Select(ctx, tx, &otx, pg.queries.GetOTXByAccountPrevious, publicKey, cursor, limit); err != nil {
		return nil, err
	}

	return otx, nil
}

func (pg *Pg) GetFailedOTX(ctx context.Context, tx pgx.Tx) ([]*OTX, error) {
	var otx []*OTX

	if err := pgxscan.Select(ctx, tx, &otx, pg.queries.GetFailedOTX); err != nil {
		return nil, err
	}

	return otx, nil
}
