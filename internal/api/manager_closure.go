package api

import (
	"context"
	"strings"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/jackc/pgx/v5"
)

// managerClosure is the one thing a public voucher or pool response says about
// its managing account's retirement: that it is scheduled to close, or has. It
// carries no user ID, name, contact, reason, ban state or hold.
type (
	managerClosure struct {
		State    string     `json:"state"`
		ClosesAt *time.Time `json:"closesAt"`
	}
)

const (
	managerClosureScheduled = "SCHEDULED"
	managerClosureClosed    = "CLOSED"
)

// managerClosures answers for every owner address in one query, so a list of
// resources costs one lookup rather than one per row. Owners are matched
// case-insensitively against the account's own address; a Safe address has no
// account row, so a resource owned by a Safe never shows its signers' closures.
func managerClosures(ctx context.Context, tx pgx.Tx, owners []*string) (map[string]*managerClosure, error) {
	seen := map[string]struct{}{}
	addresses := make([]string, 0, len(owners))
	for _, owner := range owners {
		if owner == nil || *owner == "" {
			continue
		}
		lowered := strings.ToLower(*owner)
		if _, ok := seen[lowered]; ok {
			continue
		}
		seen[lowered] = struct{}{}
		addresses = append(addresses, lowered)
	}
	closures := map[string]*managerClosure{}
	if len(addresses) == 0 {
		return closures, nil
	}
	rows, err := tx.Query(ctx, `SELECT lower(u.blockchain_address), r.state, r.closes_at
        FROM account_retirements r JOIN users u ON u.id = r.user_id
        WHERE r.restored_at IS NULL AND lower(u.blockchain_address) = ANY($1)`, addresses)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now()
	for rows.Next() {
		var address, state string
		var closesAt time.Time
		if err := rows.Scan(&address, &state, &closesAt); err != nil {
			return nil, err
		}
		if state == store.LifecycleRetirementScheduled && now.Before(closesAt) {
			closes := closesAt
			closures[address] = &managerClosure{State: managerClosureScheduled, ClosesAt: &closes}
		} else {
			closures[address] = &managerClosure{State: managerClosureClosed}
		}
	}
	return closures, rows.Err()
}

func closureFor(closures map[string]*managerClosure, owner *string) *managerClosure {
	if owner == nil {
		return nil
	}
	return closures[strings.ToLower(*owner)]
}

// projectWithClosures wraps values for the public boundary and attaches each
// one's managerClosure, which is null for an owner with no retirement.
func projectWithClosures[T any](ctx context.Context, tx pgx.Tx, values []T, owner func(T) *string, remove ...string) ([]publicResourceProjection[T], error) {
	owners := make([]*string, 0, len(values))
	for _, value := range values {
		owners = append(owners, owner(value))
	}
	closures, err := managerClosures(ctx, tx, owners)
	if err != nil {
		return nil, err
	}
	projected := make([]publicResourceProjection[T], 0, len(values))
	for _, value := range values {
		item := publicResource(value, remove...)
		item.extra = map[string]any{"managerClosure": closureFor(closures, owner(value))}
		projected = append(projected, item)
	}
	return projected, nil
}

func projectWithClosure[T any](ctx context.Context, tx pgx.Tx, value T, owner *string, remove ...string) (publicResourceProjection[T], error) {
	projected, err := projectWithClosures(ctx, tx, []T{value}, func(T) *string { return owner }, remove...)
	if err != nil {
		return publicResourceProjection[T]{}, err
	}
	return projected[0], nil
}
