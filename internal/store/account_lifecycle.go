package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

// Account lifecycle values. Lifecycle is orthogonal to users.status: a ban is
// a status, retirement is a row in account_retirements, and neither changes the
// other.
const (
	LifecycleActive              = "ACTIVE"
	LifecycleRetirementScheduled = "RETIREMENT_SCHEDULED"
	LifecycleErasurePending      = "ERASURE_PENDING"
	LifecycleErased              = "ERASED"

	RetirementSourceSelf  = "SELF"
	RetirementSourceAdmin = "ADMIN"

	// RetirementRecoveryMonths is the recovery period, counted in calendar months
	// from the request.
	RetirementRecoveryMonths = 6

	// MaxAccountRetirementReason bounds the optional retirement and moderation
	// reason, in characters.
	MaxAccountRetirementReason = 2000

	accountRetirementColumns = `id, user_id, state, requested_at, closes_at, reason, request_source, restored_at, version`
)

var (
	// ErrRetirementClosed reports a restore attempted after the recovery period
	// ended or erasure began; the application cannot reverse it.
	ErrRetirementClosed = errors.New("account retirement can no longer be restored")
	// ErrAccountNotRetired reports a restore of an account with no open retirement.
	ErrAccountNotRetired = errors.New("account is not retired")
)

type (
	// AccountRetirement is one lifecycle row. It is the user-facing record, so the
	// reason is included for the account's own owner and operators only; callers
	// building a public response must not copy it.
	AccountRetirement struct {
		ID            uint64     `db:"id" json:"id"`
		UserID        uint64     `db:"user_id" json:"-"`
		State         string     `db:"state" json:"state"`
		RequestedAt   time.Time  `db:"requested_at" json:"requestedAt"`
		ClosesAt      time.Time  `db:"closes_at" json:"closesAt"`
		Reason        string     `db:"reason" json:"reason"`
		RequestSource string     `db:"request_source" json:"requestSource"`
		RestoredAt    *time.Time `db:"restored_at" json:"restoredAt"`
		Version       int        `db:"version" json:"version"`
	}

	AccountLifecycleEvent struct {
		ID           uint64    `db:"id" json:"id"`
		Action       string    `db:"action" json:"action"`
		Source       string    `db:"source" json:"source"`
		ActorAddress *string   `db:"actor_address" json:"actorAddress"`
		CreatedAt    time.Time `db:"created_at" json:"createdAt"`
	}

	ModerationEvent struct {
		ID             uint64    `db:"id" json:"id"`
		Action         string    `db:"action" json:"action"`
		PreviousStatus string    `db:"previous_status" json:"previousStatus"`
		NewStatus      string    `db:"new_status" json:"newStatus"`
		Reason         string    `db:"reason" json:"reason"`
		ActorAddress   *string   `db:"actor_address" json:"actorAddress"`
		CreatedAt      time.Time `db:"created_at" json:"createdAt"`
	}
)

// GetOpenAccountRetirement returns the account's unrestored lifecycle row, with
// ok false when the account is active. lock takes the row FOR UPDATE so a
// restore and a worker claim cannot race.
func GetOpenAccountRetirement(ctx context.Context, tx pgx.Tx, userID uint64, lock bool) (AccountRetirement, bool, error) {
	query := `SELECT ` + accountRetirementColumns + ` FROM account_retirements WHERE user_id=$1 AND restored_at IS NULL`
	if lock {
		query += " FOR UPDATE"
	}
	var r AccountRetirement
	if err := pgxscan.Get(ctx, tx, &r, query, userID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AccountRetirement{}, false, nil
		}
		return AccountRetirement{}, false, err
	}
	return r, true, nil
}

// RetireAccount opens a retirement for the account and returns it, with created
// false when one was already open: repeated requests are idempotent and write no
// second event. The caller holds the user row locked so two requests serialize.
func RetireAccount(ctx context.Context, tx pgx.Tx, userID, actorID uint64, source, reason string) (AccountRetirement, bool, error) {
	if len([]rune(reason)) > MaxAccountRetirementReason {
		return AccountRetirement{}, false, errors.New("reason is too long")
	}
	if existing, ok, err := GetOpenAccountRetirement(ctx, tx, userID, true); err != nil || ok {
		return existing, false, err
	}
	var r AccountRetirement
	err := pgxscan.Get(ctx, tx, &r, `INSERT INTO account_retirements(user_id, closes_at, reason, requested_by, request_source)
        VALUES($1, CURRENT_TIMESTAMP + make_interval(months => $5), $2, $3, $4)
        RETURNING `+accountRetirementColumns, userID, strings.TrimSpace(reason), actorID, source, RetirementRecoveryMonths)
	if err != nil {
		return AccountRetirement{}, false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO account_lifecycle_events(user_id, retirement_id, action, source, actor_id)
        VALUES($1, $2, 'retired', $3, $4)`, userID, r.ID, source, actorID); err != nil {
		return AccountRetirement{}, false, err
	}
	return r, true, nil
}

// RestoreAccount closes the open retirement. It reports restored false, with a
// nil error, when the account was already active, so a repeated restore is a
// no-op that records nothing. A retirement past closes_at or no longer
// RETIREMENT_SCHEDULED returns ErrRetirementClosed.
func RestoreAccount(ctx context.Context, tx pgx.Tx, userID, actorID uint64, source string) (restored bool, err error) {
	r, ok, err := GetOpenAccountRetirement(ctx, tx, userID, true)
	if err != nil || !ok {
		return false, err
	}
	if r.State != LifecycleRetirementScheduled || !time.Now().Before(r.ClosesAt) {
		return false, ErrRetirementClosed
	}
	if _, err = tx.Exec(ctx, `UPDATE account_retirements SET restored_at=CURRENT_TIMESTAMP, restored_by=$2, version=version+1 WHERE id=$1`, r.ID, actorID); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO account_lifecycle_events(user_id, retirement_id, action, source, actor_id)
        VALUES($1, $2, 'restored', $3, $4)`, userID, r.ID, source, actorID); err != nil {
		return false, err
	}
	return true, nil
}

func ListAccountLifecycleEvents(ctx context.Context, tx pgx.Tx, userID uint64) ([]AccountLifecycleEvent, error) {
	result := []AccountLifecycleEvent{}
	err := pgxscan.Select(ctx, tx, &result, `SELECT e.id, e.action, e.source, a.blockchain_address AS actor_address, e.created_at
        FROM account_lifecycle_events e LEFT JOIN users a ON a.id=e.actor_id
        WHERE e.user_id=$1 ORDER BY e.id DESC`, userID)
	return result, err
}

// RecordModerationEvent appends one Ban/Unban fact.
func RecordModerationEvent(ctx context.Context, tx pgx.Tx, userID, actorID uint64, action, previous, next, reason string) error {
	_, err := tx.Exec(ctx, `INSERT INTO user_moderation_events(user_id, actor_id, action, previous_status, new_status, reason)
        VALUES($1, $2, $3, $4::user_status, $5::user_status, $6)`, userID, actorID, action, previous, next, strings.TrimSpace(reason))
	return err
}

func ListModerationEvents(ctx context.Context, tx pgx.Tx, userID uint64) ([]ModerationEvent, error) {
	result := []ModerationEvent{}
	err := pgxscan.Select(ctx, tx, &result, `SELECT e.id, e.action, e.previous_status::text AS previous_status, e.new_status::text AS new_status,
        e.reason, a.blockchain_address AS actor_address, e.created_at
        FROM user_moderation_events e LEFT JOIN users a ON a.id=e.actor_id
        WHERE e.user_id=$1 ORDER BY e.id DESC`, userID)
	return result, err
}

// LockUserForLifecycle serializes every lifecycle or moderation write for one
// account and returns its current moderation status and role.
func LockUserForLifecycle(ctx context.Context, tx pgx.Tx, address string) (id uint64, status, role string, err error) {
	err = tx.QueryRow(ctx, `SELECT id, status::text, role::text FROM users WHERE lower(blockchain_address)=lower($1) FOR UPDATE`, address).Scan(&id, &status, &role)
	return id, status, role, err
}

// RetirementClosingDate is the deadline a retirement requested now would store.
// RetireAccount computes it with the same expression, so a preview and the
// retirement it previews cannot disagree on month-end dates.
func RetirementClosingDate(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var closesAt time.Time
	err := tx.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP + make_interval(months => $1)`, RetirementRecoveryMonths).Scan(&closesAt)
	return closesAt, err
}
