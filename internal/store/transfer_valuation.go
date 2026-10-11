package store

import (
	"context"
	"strings"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

// TransferValuationSnapshot is the immutable informational Unit-of-Account
// value captured when a direct voucher transfer is submitted.
type (
	TransferValuationSnapshot struct {
		TxHash       string `db:"tx_hash"`
		TokenAddress string `db:"token_address"`
		UOAValue     string `db:"uoa_value"`
		UOA          string `db:"voucher_uoa"`
	}
)

// InsertTransferValuationSnapshot records a snapshot only when tokenAddress is
// a catalog voucher with complete, positive valuation metadata. An incomplete
// catalog row is intentionally a successful no-op.
func (pg *Pg) InsertTransferValuationSnapshot(
	ctx context.Context,
	tx pgx.Tx,
	trackingID string,
	tokenAddress string,
	amountRaw string,
) error {
	_, err := tx.Exec(ctx, pg.queries.InsertTransferValuation, trackingID, tokenAddress, amountRaw)
	return err
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
