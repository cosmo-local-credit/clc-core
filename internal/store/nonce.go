package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

func (pg *Pg) AcquireNonce(ctx context.Context, tx pgx.Tx, publicKey string) (uint64, error) {
	var nonce uint64

	if err := tx.QueryRow(ctx, pg.queries.AcquireNonce, publicKey).Scan(&nonce); err != nil {
		return 0, err
	}

	return nonce, nil
}

func (pg *Pg) PeekNonce(ctx context.Context, tx pgx.Tx, publicKey string) (uint64, error) {
	var nonce uint64

	if err := tx.QueryRow(ctx, pg.queries.PeekNonce, publicKey).Scan(&nonce); err != nil {
		return 0, err
	}

	return nonce, nil
}

// SetNonceIfHigher fast-forwards the noncestore row for publicKey to
// newNonce, but only when newNonce > current. Used at startup to align the
// relayer signer with the on-chain pending nonce.
func (pg *Pg) SetNonceIfHigher(ctx context.Context, tx pgx.Tx, publicKey string, newNonce uint64) error {
	_, err := tx.Exec(ctx, pg.queries.SetNonceIfHigher, publicKey, newNonce)
	return err
}

// LockSigner takes a transaction-scoped advisory lock on the signer's keystore
// row, serialising the dispatcher, the fee bumper and the reconciler against
// each other. Released on commit or rollback.
func (pg *Pg) LockSigner(ctx context.Context, tx pgx.Tx, publicKey string) error {
	_, err := tx.Exec(ctx, pg.queries.LockSigner, publicKey)
	return err
}
