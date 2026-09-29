package store

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

type (
	// DomainDirectoryEntry exposes only public metadata for a ready space.
	DomainDirectoryEntry struct {
		ID          uint64 `db:"id" json:"id"`
		Slug        string `db:"slug" json:"slug"`
		Name        string `db:"name" json:"name"`
		Description string `db:"description" json:"description"`
		IsGlobal    bool   `db:"is_global" json:"isGlobal"`
		Hostname    string `db:"hostname" json:"hostname"`
	}

	// Domain is a space identity, independent of its hostnames and memberships.
	Domain struct {
		ID          uint64    `db:"id"`
		Slug        string    `db:"slug"`
		Name        string    `db:"name"`
		Description string    `db:"description"`
		IsGlobal    bool      `db:"is_global"`
		Status      string    `db:"status"`
		CreatedAt   time.Time `db:"created_at"`
		UpdatedAt   time.Time `db:"updated_at"`
	}

	// UserDomainState keeps registration provenance separate from ordinary user
	// profile writes and the browser's current space.
	UserDomainState struct {
		UserID               uint64  `db:"user_id"`
		RegistrationDomainID *uint64 `db:"registration_domain_id"`
		DomainRevision       uint64  `db:"domain_revision"`
	}
)

// ListActiveDomains returns a bounded directory, ordered by stable ID.
func (pg *Pg) ListActiveDomains(ctx context.Context, tx pgx.Tx, after uint64, limit int) ([]DomainDirectoryEntry, error) {
	domains := make([]DomainDirectoryEntry, 0)
	err := pgxscan.Select(ctx, tx, &domains, pg.queries.ListActiveDomains, after, limit)
	return domains, err
}

// GetDomainByID reads a space for internal configuration/management purposes.
// It deliberately includes archived spaces; it does not authorize discovery.
func (pg *Pg) GetDomainByID(ctx context.Context, tx pgx.Tx, id uint64) (Domain, error) {
	var domain Domain
	err := pgxscan.Get(ctx, tx, &domain, pg.queries.GetDomainByID, id)
	return domain, err
}

// GetGlobalDomain reads the active global identity, not its hostname readiness.
func (pg *Pg) GetGlobalDomain(ctx context.Context, tx pgx.Tx) (Domain, error) {
	var domain Domain
	err := pgxscan.Get(ctx, tx, &domain, pg.queries.GetGlobalDomain)
	return domain, err
}

// GetActiveDomainByHostname requires an exact normalized hostname with verified,
// active configuration. A seeded pending hostname must not resolve through it.
func (pg *Pg) GetActiveDomainByHostname(ctx context.Context, tx pgx.Tx, hostname string) (Domain, error) {
	var domain Domain
	err := pgxscan.Get(ctx, tx, &domain, pg.queries.GetActiveDomainByHostname, hostname)
	return domain, err
}

// GetUserDomainState reads the nullable registration space and membership revision.
func (pg *Pg) GetUserDomainState(ctx context.Context, tx pgx.Tx, userID uint64) (UserDomainState, error) {
	var state UserDomainState
	err := pgxscan.Get(ctx, tx, &state, pg.queries.GetUserDomainState, userID)
	return state, err
}
