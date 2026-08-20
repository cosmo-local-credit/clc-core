package store

import (
	"context"

	"github.com/georgysavva/scany/v2/pgxscan"
)

// Chain activity totals for one account, read from the federation database.
//
// These are the figures the account screen cannot derive from a page of transfers:
// a lifetime count and a distinct-counterparty count are properties of the whole
// history, and counting a fetched page would report a fraction as a total.
//
// Only successful transactions count. A reverted transfer moved nothing, so
// including it would overstate both the total and the partner list.

type (
	AccountActivityCounts struct {
		Transactions uint64 `db:"transactions" json:"transactions"`
		Swaps        uint64 `db:"swaps"        json:"swaps"`
		Partners     uint64 `db:"partners"     json:"partners"`
	}
)

func (s *FederationPg) AccountActivityCounts(
	ctx context.Context,
	address string,
) (AccountActivityCounts, error) {
	var counts AccountActivityCounts
	rows, err := s.db.Query(ctx, s.queries.AccountActivityCounts, address)
	if err != nil {
		return counts, err
	}
	if err := pgxscan.ScanOne(&counts, rows); err != nil {
		return counts, err
	}
	return counts, nil
}
