package store

import (
	"context"
	"strings"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

// VoucherClaimSnapshot is immutable informational claim metadata captured when
// a supported voucher transfer or swap is submitted. TransferValuationSnapshot
// associates that claim with a transaction hash after relay tracking resolves.
type (
	VoucherClaimSnapshot struct {
		TokenAddress string `db:"token_address" json:"tokenAddress"`
		VoucherValue string `db:"voucher_value" json:"voucherValue"`
		VoucherUOA   string `db:"voucher_uoa" json:"voucherUoa"`
	}

	TransferValuationSnapshot struct {
		TxHash       string `db:"tx_hash"`
		TokenAddress string `db:"token_address"`
		VoucherValue string `db:"voucher_value"`
		VoucherUOA   string `db:"voucher_uoa"`
	}
)

// InsertTransferValuationSnapshots records each catalog voucher's current claim
// metadata. Missing or incomplete catalog rows are successful no-ops.
func (pg *Pg) InsertTransferValuationSnapshots(
	ctx context.Context,
	tx pgx.Tx,
	trackingID string,
	tokenAddresses []string,
) ([]VoucherClaimSnapshot, error) {
	lowered := make([]string, len(tokenAddresses))
	for i, address := range tokenAddresses {
		lowered[i] = strings.ToLower(address)
	}

	var rows []VoucherClaimSnapshot
	if err := pgxscan.Select(ctx, tx, &rows, pg.queries.InsertTransferValuations, trackingID, lowered); err != nil {
		return nil, err
	}
	return rows, nil
}

// ListTransferValuationSnapshotsByTxHashes returns immutable snapshots for a
// transfer page. Matching is case-insensitive and replacement OTX rows sharing
// a tracking id resolve to the original snapshot.
func (pg *Pg) ListTransferValuationSnapshotsByTxHashes(
	ctx context.Context,
	tx pgx.Tx,
	txHashes []string,
) ([]TransferValuationSnapshot, error) {
	lowered := make([]string, len(txHashes))
	for i, hash := range txHashes {
		lowered[i] = strings.ToLower(hash)
	}

	var rows []TransferValuationSnapshot
	if err := pgxscan.Select(ctx, tx, &rows, pg.queries.ListTransferValuations, lowered); err != nil {
		return nil, err
	}
	return rows, nil
}
