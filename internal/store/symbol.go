package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

func listTakenSymbols(ctx context.Context, tx pgx.Tx, query string, prefix string) ([]string, error) {
	rows, err := tx.Query(ctx, query, prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	symbols := make([]string, 0)
	for rows.Next() {
		var symbol string
		if err := rows.Scan(&symbol); err != nil {
			return nil, err
		}
		symbols = append(symbols, symbol)
	}
	return symbols, rows.Err()
}

func (pg *Pg) ListTakenVoucherSymbols(ctx context.Context, tx pgx.Tx, prefix string) ([]string, error) {
	return listTakenSymbols(ctx, tx, pg.queries.ListTakenVoucherSymbols, prefix)
}

func (pg *Pg) ListTakenPoolSymbols(ctx context.Context, tx pgx.Tx, prefix string) ([]string, error) {
	return listTakenSymbols(ctx, tx, pg.queries.ListTakenPoolSymbols, prefix)
}
