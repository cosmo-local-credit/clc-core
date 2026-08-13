package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

func (pg *Pg) GetProtocolAddress(ctx context.Context, tx pgx.Tx, key string) (string, error) {
	var address string

	if err := tx.QueryRow(ctx, pg.queries.GetProtocolAddress, key).Scan(&address); err != nil {
		return "", err
	}

	return address, nil
}

func (pg *Pg) SetProtocolAddress(ctx context.Context, tx pgx.Tx, key string, address string) error {
	_, err := tx.Exec(ctx, pg.queries.SetProtocolAddress, key, address)
	return err
}
