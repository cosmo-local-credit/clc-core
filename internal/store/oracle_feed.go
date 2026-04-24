package store

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

type (
	OracleFeed struct {
		ID        uint64    `db:"id"         json:"id"`
		Name      string    `db:"name"       json:"name"`
		Address   string    `db:"address"    json:"address"`
		CreatedAt time.Time `db:"created_at" json:"createdAt"`
		UpdatedAt time.Time `db:"updated_at" json:"updatedAt"`
	}
)

func (pg *Pg) GetOracleFeedByName(ctx context.Context, tx pgx.Tx, name string) (OracleFeed, error) {
	var feed OracleFeed

	row, err := tx.Query(ctx, pg.queries.GetOracleFeedByName, name)
	if err != nil {
		return feed, err
	}

	if err := pgxscan.ScanOne(&feed, row); err != nil {
		return feed, err
	}

	return feed, nil
}

func (pg *Pg) ListOracleFeeds(ctx context.Context, tx pgx.Tx) ([]OracleFeed, error) {
	var feeds []OracleFeed

	rows, err := tx.Query(ctx, pg.queries.ListOracleFeeds)
	if err != nil {
		return nil, err
	}

	if err := pgxscan.ScanAll(&feeds, rows); err != nil {
		return nil, err
	}

	return feeds, nil
}
