package store

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

type (
	// RelayOperation ties a client-chosen operation id to the one relay job it
	// queued. See migration 047 for why the table exists.
	RelayOperation struct {
		OperationID   string    `db:"operation_id"`
		UserAddress   string    `db:"user_address"`
		RequestDigest []byte    `db:"request_digest"`
		TrackingID    string    `db:"tracking_id"`
		CreatedAt     time.Time `db:"created_at"`
	}
)

// ClaimRelayOperation records op unless its operation id is already taken.
//
// It reports claimed=true when op was inserted, and the caller goes on to
// queue the job under op.TrackingID in the same transaction. Otherwise it
// returns the existing row, committed by an earlier or concurrent submission,
// and the caller decides from its digest whether this is a replay.
func (pg *Pg) ClaimRelayOperation(ctx context.Context, tx pgx.Tx, op RelayOperation) (RelayOperation, bool, error) {
	tag, err := tx.Exec(
		ctx,
		pg.queries.InsertRelayOperation,
		op.OperationID,
		op.UserAddress,
		op.RequestDigest,
		op.TrackingID,
	)
	if err != nil {
		return RelayOperation{}, false, err
	}
	if tag.RowsAffected() == 1 {
		return op, true, nil
	}

	var existing RelayOperation
	if err := pgxscan.Get(ctx, tx, &existing, pg.queries.GetRelayOperation, op.OperationID); err != nil {
		return RelayOperation{}, false, err
	}
	return existing, false, nil
}
