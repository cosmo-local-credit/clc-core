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
