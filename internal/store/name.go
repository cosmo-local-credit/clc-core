package store

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
)

// NormalizeDeployName is the canonical form a voucher or pool label is compared
// in: trimmed, inner whitespace collapsed, lowercased. It has to agree exactly
// with the SQL expression the *-display-name-taken queries and the indexes in
// migration 037 use, or a duplicate slips through on a lookup that scans nothing.
//
// NormalizeCatalogTerm already does the trim-and-collapse half, so search terms
// and name occupancy fold whitespace the same way.
func NormalizeDeployName(raw string) string {
	return strings.ToLower(NormalizeCatalogTerm(raw))
}

func displayNameTaken(ctx context.Context, tx pgx.Tx, query, name string, excludeAddress *string) (bool, error) {
	var exclude *string
	if excludeAddress != nil {
		lowered := strings.ToLower(strings.TrimSpace(*excludeAddress))
		if lowered != "" {
			exclude = &lowered
		}
	}

	var taken bool
	if err := tx.QueryRow(ctx, query, NormalizeDeployName(name), exclude).Scan(&taken); err != nil {
		return false, err
	}
	return taken, nil
}

// VoucherDisplayNameTaken reports whether the catalog or an open token deploy
// already carries this label. Pass the address of the voucher being renamed as
// excludeAddress so re-saving its own name is not a conflict; pass nil on deploy.
//
// Like symbol occupancy this is a read, not a reservation: callers run it inside
// the transaction that goes on to write, which is as close to atomic as the
// deploy path gets — the deployed_tokens row itself is only written later, by
// the worker.
func (pg *Pg) VoucherDisplayNameTaken(ctx context.Context, tx pgx.Tx, name string, excludeAddress *string) (bool, error) {
	return displayNameTaken(ctx, tx, pg.queries.VoucherDisplayNameTaken, name, excludeAddress)
}

// PoolDisplayNameTaken is VoucherDisplayNameTaken's counterpart for pools.
func (pg *Pg) PoolDisplayNameTaken(ctx context.Context, tx pgx.Tx, name string, excludeAddress *string) (bool, error) {
	return displayNameTaken(ctx, tx, pg.queries.PoolDisplayNameTaken, name, excludeAddress)
}
