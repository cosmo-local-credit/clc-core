package store

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

type (
	OTX struct {
		ID             uint64    `db:"id"         json:"id"`
		TrackingID     string    `db:"tracking_id" json:"trackingId"`
		OTXType        string    `db:"otx_type"   json:"otxType"`
		SignerAccount  string    `db:"public_key" json:"signerAccount"`
		RawTx          string    `db:"raw_tx"     json:"rawTx"`
		TxHash         string    `db:"tx_hash"    json:"txHash"`
		Nonce          uint64    `db:"nonce"      json:"nonce"`
		Replaced       bool      `db:"replaced"   json:"replaced"`
		CreatedAt      time.Time `db:"created_at" json:"createdAt"`
		UpdatedAt      time.Time `db:"updated_at" json:"updatedAt"`
		DispatchStatus string    `db:"status"     json:"status"`
	}
)

const (
	TOKEN_PROXY_DEPLOY = "TOKEN_PROXY_DEPLOY"

	POOL_TOKEN_INDEX_DEPLOY     = "POOL_TOKEN_INDEX_DEPLOY"
	POOL_LIMITER_DEPLOY         = "POOL_LIMITER_DEPLOY"
	POOL_FEE_POLICY_DEPLOY      = "POOL_FEE_POLICY_DEPLOY"
	POOL_ORACLE_QUOTER_DEPLOY   = "POOL_ORACLE_QUOTER_DEPLOY"
	POOL_RELATIVE_QUOTER_DEPLOY = "POOL_RELATIVE_QUOTER_DEPLOY"
	POOL_PROXY_DEPLOY           = "POOL_PROXY_DEPLOY"
	PROXY_UPGRADE               = "PROXY_UPGRADE"

	// RELAY_DELEGATE, RELAY_EXEC, RELAY_DELEGATE_EXEC, ACCOUNT_REGISTER are
	// Relayer OTX types (EIP-7702 / Calibur).
	RELAY_DELEGATE      = "RELAY_DELEGATE"      // Bare SetCodeTx delegating an EOA to Calibur.
	RELAY_EXEC          = "RELAY_EXEC"          // DynamicFeeTx executing a signed batch on an already-delegated user.
	RELAY_DELEGATE_EXEC = "RELAY_DELEGATE_EXEC" // SetCodeTx that delegates *and* executes in a single tx (first relay).
	ACCOUNT_REGISTER    = "ACCOUNT_REGISTER"    // AccountIndex.add(userAddress) — onchain user registration on wallet activation.

	// SAFE_EXECUTE is the system signer submitting a Safe's execTransaction
	// once its signers have confirmed it.
	SAFE_EXECUTE = "SAFE_EXECUTE"
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
	var otxs []*OTX

	rows, err := tx.Query(ctx, pg.queries.GetOTXByTrackingID, trackingID)
	if err != nil {
		return nil, err
	}

	if err := pgxscan.ScanAll(&otxs, rows); err != nil {
		return nil, err
	}

	return otxs, nil
}

func (pg *Pg) GetOTXByAccount(ctx context.Context, tx pgx.Tx, address string, limit uint64) ([]*OTX, error) {
	var otxs []*OTX

	rows, err := tx.Query(ctx, pg.queries.GetOTXByAccount, address, limit)
	if err != nil {
		return nil, err
	}

	if err := pgxscan.ScanAll(&otxs, rows); err != nil {
		return nil, err
	}

	return otxs, nil
}

func (pg *Pg) GetOTXByAccountNext(ctx context.Context, tx pgx.Tx, address string, cursor uint64, limit uint64) ([]*OTX, error) {
	var otxs []*OTX

	rows, err := tx.Query(ctx, pg.queries.GetOTXByAccountNext, address, cursor, limit)
	if err != nil {
		return nil, err
	}

	if err := pgxscan.ScanAll(&otxs, rows); err != nil {
		return nil, err
	}

	return otxs, nil
}

func (pg *Pg) GetOTXByAccountPrevious(ctx context.Context, tx pgx.Tx, address string, cursor uint64, limit uint64) ([]*OTX, error) {
	var otxs []*OTX

	rows, err := tx.Query(ctx, pg.queries.GetOTXByAccountPrev, address, cursor, limit)
	if err != nil {
		return nil, err
	}

	if err := pgxscan.ScanAll(&otxs, rows); err != nil {
		return nil, err
	}

	return otxs, nil
}

// GetUnsettledOTXBySigner returns the signer's transactions that have not
// reached a settled dispatch status, oldest nonce first.
func (pg *Pg) GetUnsettledOTXBySigner(ctx context.Context, tx pgx.Tx, publicKey string) ([]*OTX, error) {
	var otxs []*OTX

	rows, err := tx.Query(ctx, pg.queries.GetUnsettledOTXBySigner, publicKey)
	if err != nil {
		return nil, err
	}

	if err := pgxscan.ScanAll(&otxs, rows); err != nil {
		return nil, err
	}

	return otxs, nil
}

// SetOTXReplaced marks an OTX superseded by a replacement at the same nonce.
func (pg *Pg) SetOTXReplaced(ctx context.Context, tx pgx.Tx, otxID uint64) error {
	_, err := tx.Exec(ctx, pg.queries.SetOTXReplaced, otxID)
	return err
}

// CountUndispatchedBelowNonce counts the signer's still-PENDING transactions
// below nonce, i.e. predecessors that have not been sent yet.
func (pg *Pg) CountUndispatchedBelowNonce(ctx context.Context, tx pgx.Tx, publicKey string, nonce uint64) (uint64, error) {
	var count uint64

	if err := tx.QueryRow(ctx, pg.queries.CountUndispatchedBelowNonce, publicKey, nonce).Scan(&count); err != nil {
		return 0, err
	}

	return count, nil
}
