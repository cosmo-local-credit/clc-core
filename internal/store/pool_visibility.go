package store

import (
	"context"
	"errors"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

var (
	ErrRetiredPool = errors.New("this pool is retired; new swaps and deposits are unavailable")
)

// ListManagedPools is called only from authenticated owner/admin routes. Public
// discovery uses ListPoolsFiltered and cannot opt into retained records.
func (pg *Pg) ListManagedPools(ctx context.Context, tx pgx.Tx, filter CatalogFilter, status, owner string, page CatalogPage, cursor, limit uint64) ([]Pool, error) {
	args := &sqlArgs{}
	visibility := ""
	switch status {
	case "", "all":
	case "active":
		visibility = "p.active"
	case "retired":
		visibility = "NOT p.active"
	default:
		return nil, errors.New("invalid pool status")
	}
	ownerMatch, textMatch := "", ""
	if owner != "" {
		ownerMatch = "lower(p.owner_address) = lower(" + args.bind(owner) + ")"
	}
	if filter.Term != "" {
		term := args.bind(filter.likeTerm())
		textMatch = "(lower(concat_ws(' ', COALESCE(p.display_name,p.name),p.symbol,p.address)) LIKE '%' || lower(" + term + ") || '%' ESCAPE '\\')"
	}
	query := "WITH page AS MATERIALIZED (SELECT p.id FROM pools p" + whereClause(
		visibility, ownerMatch, textMatch, poolTagPredicate(args, filter), poolVoucherPredicate(args, filter), cursorPredicate(args, "p.id", page, cursor),
	) + " ORDER BY p.id " + pageOrder(page) + " LIMIT " + args.bind(limit) + ")" + poolPageProjection
	var pools []Pool
	err := pgxscan.Select(ctx, tx, &pools, query, args.values...)
	return pools, err
}

// CheckPoolOperationsAllowed serializes admission with retirement. The share
// locks last until the relay job commits, so retirement and a newly accepted job
// have a defined order. Previously accepted jobs and external transactions can
// still settle after retirement. Recovery calls exclude their source pool, but
// an explicit destination pool must still accept deposits.
func (pg *Pg) CheckPoolOperationsAllowed(ctx context.Context, tx pgx.Tx, chainID int64, addresses []string) error {
	if len(addresses) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT active FROM pools
WHERE chain_id = $1 AND lower(address) = ANY($2::TEXT[])
ORDER BY id FOR SHARE`, chainID, addresses)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var active bool
		if err := rows.Scan(&active); err != nil {
			return err
		}
		if !active {
			return ErrRetiredPool
		}
	}
	return rows.Err()
}
