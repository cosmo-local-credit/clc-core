package store

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

type DashboardCatalogSnapshot struct {
	Pools    int64 `db:"pools"    json:"pools"`
	Vouchers int64 `db:"vouchers" json:"vouchers"`
	Accounts int64 `db:"accounts" json:"accounts"`
	Reports  int64 `db:"reports"  json:"reports"`
}

type DashboardCatalogPoint struct {
	T        time.Time `db:"t"        json:"t"`
	Reports  int64     `db:"reports"  json:"reports"`
	Vouchers int64     `db:"vouchers" json:"vouchers"`
	Pools    int64     `db:"pools"    json:"pools"`
}

type DashboardTransactionPoint struct {
	T            time.Time `db:"t"            json:"t"`
	Transactions int64     `db:"transactions" json:"transactions"`
}

func (pg *Pg) DashboardCatalogSnapshot(ctx context.Context, tx pgx.Tx, since, until time.Time) (DashboardCatalogSnapshot, error) {
	var snap DashboardCatalogSnapshot
	row, err := tx.Query(ctx, pg.queries.DashboardCatalogSnapshot, since, until)
	if err != nil {
		return snap, err
	}
	if err := pgxscan.ScanOne(&snap, row); err != nil {
		return snap, err
	}
	return snap, nil
}

func (pg *Pg) DashboardCatalogSeries(ctx context.Context, tx pgx.Tx, since, until time.Time, bucketSeconds int64) ([]DashboardCatalogPoint, error) {
	var rows []DashboardCatalogPoint
	if err := pgxscan.Select(ctx, tx, &rows, pg.queries.DashboardCatalogSeries, since, until, bucketSeconds); err != nil {
		return nil, err
	}
	if rows == nil {
		return []DashboardCatalogPoint{}, nil
	}
	return rows, nil
}
