package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

func (pg *Pg) GetPoolAddress(ctx context.Context, tx pgx.Tx, key string) (string, error) {
	var address string

	if err := tx.QueryRow(ctx, pg.queries.GetPoolAddress, key).Scan(&address); err != nil {
		return "", err
	}

	return address, nil
}

func (pg *Pg) SetPoolAddress(ctx context.Context, tx pgx.Tx, key string, address string) error {
	_, err := tx.Exec(ctx, pg.queries.SetPoolAddress, key, address)
	return err
}
