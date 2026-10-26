package store

import (
	"context"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

// Per-account relationships for the operator console's account screen.
//
// "Pool membership" is a derived relation, not a stored one: nothing records a
// user joining a pool. What the catalog does record is which vouchers a pool
// allows, so an account belongs to the pools that allow a voucher it owns. That is
// the relation these read, and the console labels it as such.
//
// The counts deliberately apply the same visibility rules as the lists they sit
// beside — approved reports, catalog-visible vouchers — so a figure and the tab
// under it never disagree.

type (
	AccountRelationshipCounts struct {
		Reports         uint64 `db:"reports"          json:"reports"`
		Vouchers        uint64 `db:"vouchers"         json:"vouchers"`
		PoolMemberships uint64 `db:"pool_memberships" json:"poolMemberships"`
	}
)

func (pg *Pg) AccountRelationshipCounts(
	ctx context.Context,
	tx pgx.Tx,
	address string,
) (AccountRelationshipCounts, error) {
	var counts AccountRelationshipCounts
	rows, err := tx.Query(ctx, pg.queries.AccountRelationshipCounts, address)
	if err != nil {
		return counts, err
	}
	if err := pgxscan.ScanOne(&counts, rows); err != nil {
		return counts, err
	}
	return counts, nil
}

// composeAccountPoolPage pages the pools that allow a voucher this account owns.
//
// Composed rather than three static statements for the reason catalog_search.go
// gives, and it reuses the pool projection so a membership row and a catalog row
// carry identical fields.
func composeAccountPoolPage(owner string, page CatalogPage, cursor, limit uint64) composed {
	args := &sqlArgs{}
	ownerParam := args.bind(owner)
	membership := `EXISTS (
            SELECT 1
            FROM pool_tokens pt
            INNER JOIN vouchers v ON v.id = pt.voucher_id
            WHERE pt.pool_id = p.id
                AND pt.is_allowed
                AND ` + ownedByPredicate("v", ownerParam) + `
        )`

	body := "WITH page AS MATERIALIZED (\n" +
		"    SELECT p.id\n" +
		"    FROM pools p" +
		whereClause(membership, cursorPredicate(args, "p.id", page, cursor)) +
		"\n    ORDER BY p.id " + pageOrder(page) +
		"\n    LIMIT " + args.bind(limit) +
		"\n)"

	return composed{sql: body + poolPageProjection, args: args.values}
}

func (pg *Pg) ListAccountPools(
	ctx context.Context,
	tx pgx.Tx,
	address string,
	page CatalogPage,
	cursor, limit uint64,
) ([]Pool, error) {
	query := composeAccountPoolPage(address, page, cursor, limit)

	var pools []Pool
	if err := pgxscan.Select(ctx, tx, &pools, query.sql, query.args...); err != nil {
		return nil, err
	}
	return pools, nil
}
