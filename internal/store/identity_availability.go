package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type (
	// IdentityTaken says whether each requested identity is already claimed.
	// A nil pointer means that argument was omitted and the column was not checked.
	// Email and phone are exact. Username is folded with normalize_identity_name.
	IdentityTaken struct {
		Email    *bool
		Phone    *bool
		Username *bool
	}
)

// IdentityAvailability is an advisory read. It does not reserve the identity;
// signup and profile save still enforce the unique constraints.
// Pass nil to skip a column. Email must already be lowercased, matching
// GetUserByEmail and the value signup stores. Owner is the caller's lowercased
// address, or nil for a guest; identities that user holds are not taken.
func (pg *Pg) IdentityAvailability(ctx context.Context, tx pgx.Tx, email, phone, username, owner *string) (IdentityTaken, error) {
	var taken IdentityTaken
	err := tx.QueryRow(ctx, pg.queries.IdentityAvailability, email, phone, username, owner).Scan(
		&taken.Email, &taken.Phone, &taken.Username,
	)
	return taken, err
}
