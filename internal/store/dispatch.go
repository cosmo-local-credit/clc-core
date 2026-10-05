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

	// POOL_FULL was added in migration 026, with POOL_CONFLICT, NONCE_GAP and
	// INVALID_TX, to carry reth's rejection vocabulary. The four pre-existing
	// chain-error statuses could not distinguish a transient pool rejection
	// (resend the same bytes) from an invalid transaction (never resend), so
	// everything unmatched fell through as a success.
	POOL_FULL     = "POOL_FULL"     // pool over capacity; resend unchanged
	POOL_CONFLICT = "POOL_CONFLICT" // EIP-7702 reservation or in-flight limit
	NONCE_GAP     = "NONCE_GAP"     // an earlier nonce must land first
	INVALID_TX    = "INVALID_TX"    // our bug; the bytes will never be accepted
)

// terminalDispatchStatuses are the statuses the reconciler treats as settled.
// Anything else is a transaction it still owes an outcome for.
var (
	terminalDispatchStatuses = map[string]struct{}{
		SUCCESS:    {},
		REVERTED:   {},
		LOW_NONCE:  {},
		INVALID_TX: {},
	}
)

func IsTerminalDispatchStatus(status string) bool {
	_, ok := terminalDispatchStatuses[status]
	return ok
}

func (pg *Pg) InsertDispatchTx(ctx context.Context, tx pgx.Tx, dispatchTx DispatchTx) error {
	_, err := tx.Exec(ctx, pg.queries.InsertDispatchTx, dispatchTx.OTXID, dispatchTx.Status)
	return err
}

// UpdateDispatchTxStatus records a transaction's dispatch status and, in the
// same database transaction, re-derives any pending WebAuthn registration the
// transaction carries.
//
// The registration step lives here rather than in each caller because a status
// reaches this table from four places — the dispatcher, the receipt poller, the
// reconciler and the chain-event subscriber — and a passkey must become usable
// exactly when its register() call is confirmed, whichever of them sees it
// first. It is a no-op for a transaction that carries no registration.
func (pg *Pg) UpdateDispatchTxStatus(ctx context.Context, tx pgx.Tx, dispatchTx DispatchTx) error {
	if _, err := tx.Exec(ctx, pg.queries.UpdateDispatchTxStatus, dispatchTx.Status, dispatchTx.OTXID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, pg.queries.SettleWebAuthnRegistration, dispatchTx.OTXID)
	return err
}
