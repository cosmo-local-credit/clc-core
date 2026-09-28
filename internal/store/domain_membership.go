package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

type (
	// DomainMembership includes inactive memberships so the client can explain their state.
	DomainMembership struct {
		DomainID     uint64     `json:"domainId"`
		Name         string     `json:"name"`
		Status       string     `json:"status"`
		DomainStatus string     `json:"domainStatus"`
		IsGlobal     bool       `json:"isGlobal"`
		Hostname     *string    `json:"hostname"`
		JoinedAt     time.Time  `json:"joinedAt"`
		LeftAt       *time.Time `json:"leftAt"`
	}
	// UserDomains is a consistent snapshot of the account's memberships and revision.
	UserDomains struct {
		UserID               uint64             `db:"user_id"`
		RegistrationDomainID *uint64            `db:"registration_domain_id"`
		DomainRevision       uint64             `db:"domain_revision"`
		GlobalRequired       bool               `db:"global_required"`
		Memberships          []DomainMembership `db:"memberships"`
	}
	domainAccount struct {
		UserID               uint64  `db:"user_id"`
		RegistrationDomainID *uint64 `db:"registration_domain_id"`
		DomainRevision       uint64  `db:"domain_revision"`
		Status               string  `db:"status"`
		Address              string  `db:"address"`
	}
	domainHost struct {
		DomainID   uint64     `db:"domain_id"`
		Hostname   string     `db:"hostname"`
		Status     string     `db:"status"`
		VerifiedAt *time.Time `db:"verified_at"`
	}
	domainMembershipChange struct {
		userID, domainID, revision uint64
		operation                  string
		profile                    *UserProfileUpdate
	}
	domainMembershipContext struct {
		account  domainAccount
		state    UserDomains
		domains  map[uint64]Domain
		hosts    map[uint64]domainHost
		globalID uint64
	}
)

var (
	ErrDomainUnavailable          = errors.New("domain is not active with a verified address")
	ErrDomainAccountRestricted    = errors.New("account is not approved")
	ErrDomainRevisionConflict     = errors.New("account domain revision changed")
	ErrDomainOnboardingRequired   = errors.New("domain onboarding is required")
	ErrDomainRegistrationConflict = errors.New("registration domain is already assigned")
	ErrDomainMembershipSuspended  = errors.New("domain membership is suspended")
	ErrGlobalDomainRequired       = errors.New("global domain membership is required")
	ErrDomainFallbackUnavailable  = errors.New("global fallback is unavailable")
	ErrDomainRepairRequired       = errors.New("registration membership is inactive without an active global membership; support repair required")
)

// GetUserDomains uses one SQL statement so memberships and revision share a snapshot.
func (pg *Pg) GetUserDomains(ctx context.Context, tx pgx.Tx, userID uint64) (UserDomains, error) {
	var state UserDomains
	err := pgxscan.Get(ctx, tx, &state, pg.queries.GetUserDomains, userID)
	return state, err
}

// CompleteDomainOnboarding updates the existing account's profile and initial
// membership atomically. A completed onboarding retry never overwrites the profile
// or reactivates a membership the user subsequently left. The caller supplies an
// authenticated user ID and validated profile; the address is loaded under lock.
func (pg *Pg) CompleteDomainOnboarding(ctx context.Context, tx pgx.Tx, userID, domainID, expectedRevision uint64, profile UserProfileUpdate) (UserDomains, error) {
	return pg.changeDomainMembership(ctx, tx, domainMembershipChange{userID, domainID, expectedRevision, "onboard", &profile})
}

// JoinDomain activates a membership without modifying registration provenance.
func (pg *Pg) JoinDomain(ctx context.Context, tx pgx.Tx, userID, domainID, expectedRevision uint64) (UserDomains, error) {
	return pg.changeDomainMembership(ctx, tx, domainMembershipChange{userID, domainID, expectedRevision, "join", nil})
}

// LeaveDomain deactivates a membership and activates Sarafu in the same transaction
// when leaving the registration space or the last active non-global membership.
func (pg *Pg) LeaveDomain(ctx context.Context, tx pgx.Tx, userID, domainID, expectedRevision uint64) (UserDomains, error) {
	return pg.changeDomainMembership(ctx, tx, domainMembershipChange{userID, domainID, expectedRevision, "leave", nil})
}

// A savepoint rolls back profile, membership, revision and audit writes together
// even if the caller handles an error and commits its outer transaction. Successful
// calls still require the caller to commit. Use one mutation per transaction and
// acquire no account locks before it; configuration/admin writers must follow the
// same domain -> host -> user lock order. READ COMMITTED is the expected isolation.
func (pg *Pg) changeDomainMembership(ctx context.Context, tx pgx.Tx, change domainMembershipChange) (UserDomains, error) {
	var result UserDomains
	err := pgx.BeginFunc(ctx, tx, func(inner pgx.Tx) error {
		locked, err := pg.lockDomainMembership(ctx, inner, change)
		if err != nil {
			return err
		}
		changed, err := pg.applyDomainMembership(ctx, inner, change, locked)
		if err != nil {
			return err
		}
		if changed {
			if _, err = inner.Exec(ctx, pg.queries.IncrementDomainRevision, change.userID); err != nil {
				return err
			}
		}
		result, err = pg.GetUserDomains(ctx, inner, change.userID)
		return err
	})
	if err != nil {
		return UserDomains{}, err
	}
	return result, nil
}

func (pg *Pg) lockDomainMembership(ctx context.Context, tx pgx.Tx, change domainMembershipChange) (domainMembershipContext, error) {
	locked := domainMembershipContext{domains: make(map[uint64]Domain), hosts: make(map[uint64]domainHost)}
	before, err := pg.GetUserDomainState(ctx, tx, change.userID)
	if err != nil {
		return locked, err
	}
	var domains []Domain
	if err = pgxscan.Select(ctx, tx, &domains, pg.queries.LockMembershipDomains, change.domainID, before.RegistrationDomainID); err != nil {
		return locked, err
	}
	ids := make([]int64, 0, len(domains))
	for _, domain := range domains {
		locked.domains[domain.ID] = domain
		ids = append(ids, int64(domain.ID))
		if domain.IsGlobal {
			locked.globalID = domain.ID
		}
	}
	var hosts []domainHost
	if err = pgxscan.Select(ctx, tx, &hosts, pg.queries.LockMembershipHosts, ids); err != nil {
		return locked, err
	}
	for _, host := range hosts {
		locked.hosts[host.DomainID] = host
	}
	if err = pgxscan.Get(ctx, tx, &locked.account, pg.queries.LockDomainAccount, change.userID); err != nil {
		return locked, err
	}
	if locked.account.Status != "APPROVED" {
		return locked, ErrDomainAccountRestricted
	}
	if locked.account.DomainRevision != change.revision {
		return locked, ErrDomainRevisionConflict
	}
	// Registration could have been assigned between the preliminary read and lock.
	if !sameRegistration(before.RegistrationDomainID, locked.account.RegistrationDomainID) {
		return locked, ErrDomainRevisionConflict
	}
	var membershipIDs []uint64
	if err = pgxscan.Select(ctx, tx, &membershipIDs, pg.queries.LockUserMemberships, change.userID); err != nil {
		return locked, err
	}
	locked.state, err = pg.GetUserDomains(ctx, tx, change.userID)
	return locked, err
}

func sameRegistration(a, b *uint64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func (locked domainMembershipContext) ready(domainID uint64) bool {
	domain, ok := locked.domains[domainID]
	host := locked.hosts[domainID]
	return ok && domain.Status == "active" && host.Status == "active" && host.VerifiedAt != nil
}

func (state UserDomains) membershipStatus(domainID uint64) string {
	for _, membership := range state.Memberships {
		if membership.DomainID == domainID {
			return membership.Status
		}
	}
	return ""
}

func (pg *Pg) applyDomainMembership(ctx context.Context, tx pgx.Tx, change domainMembershipChange, locked domainMembershipContext) (bool, error) {
	state := locked.state
	current := state.membershipStatus(change.domainID)
	if current == "suspended" {
		return false, ErrDomainMembershipSuspended
	}
	// Do not silently repair legacy/admin-induced violations or reactivate suspensions.
	if state.GlobalRequired && state.membershipStatus(locked.globalID) != "active" {
		return false, ErrDomainRepairRequired
	}
	if change.operation == "onboard" {
		return pg.onboardDomainMembership(ctx, tx, change, locked)
	}
	if state.RegistrationDomainID == nil {
		return false, ErrDomainOnboardingRequired
	}
	if _, ok := locked.domains[change.domainID]; !ok {
		return false, ErrDomainUnavailable
	}
	if change.operation == "join" {
		if !locked.ready(change.domainID) {
			return false, ErrDomainUnavailable
		}
		if current == "active" {
			return false, nil
		}
		return true, pg.writeDomainMembership(ctx, tx, change.userID, change.domainID, current, "active", "join", "explicit join")
	}
	if current != "active" {
		return false, nil
	}
	remaining := 0
	for _, membership := range state.Memberships {
		if membership.Status == "active" && membership.DomainID != change.domainID {
			remaining++
		}
	}
	if change.domainID == locked.globalID && (state.GlobalRequired || remaining == 0) {
		return false, ErrGlobalDomainRequired
	}
	needsGlobal := change.domainID == *state.RegistrationDomainID || remaining == 0
	if needsGlobal {
		if !locked.ready(locked.globalID) || state.membershipStatus(locked.globalID) == "suspended" {
			return false, ErrDomainFallbackUnavailable
		}
	}
	if err := pg.writeDomainMembership(ctx, tx, change.userID, change.domainID, current, "left", "leave", "explicit leave"); err != nil {
		return false, err
	}
	if needsGlobal && state.membershipStatus(locked.globalID) != "active" {
		if err := pg.writeDomainMembership(ctx, tx, change.userID, locked.globalID, state.membershipStatus(locked.globalID), "active", "fallback_join", "registration or last membership deactivated"); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (pg *Pg) onboardDomainMembership(ctx context.Context, tx pgx.Tx, change domainMembershipChange, locked domainMembershipContext) (bool, error) {
	if registration := locked.state.RegistrationDomainID; registration != nil {
		if *registration != change.domainID {
			return false, ErrDomainRegistrationConflict
		}
		return false, nil
	}
	if !locked.ready(change.domainID) {
		return false, ErrDomainUnavailable
	}
	if !locked.ready(locked.globalID) {
		return false, ErrDomainFallbackUnavailable
	}
	profile := *change.profile
	profile.Address = locked.account.Address
	if _, err := pg.UpdateUserProfile(ctx, tx, profile); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, pg.queries.SetDomainRegistration, change.userID, change.domainID); err != nil {
		return false, err
	}
	if err := pg.domainAudit(ctx, tx, change.userID, change.domainID, "register", "registration", nil, map[string]any{"registrationDomainId": change.domainID}, "domain onboarding"); err != nil {
		return false, err
	}
	current := locked.state.membershipStatus(change.domainID)
	if current != "active" {
		if err := pg.writeDomainMembership(ctx, tx, change.userID, change.domainID, current, "active", "join", "domain onboarding"); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (pg *Pg) writeDomainMembership(ctx context.Context, tx pgx.Tx, userID, domainID uint64, before, after, action, reason string) error {
	query := pg.queries.ActivateDomainMembership
	if after == "left" {
		query = pg.queries.LeaveDomainMembership
	}
	tag, err := tx.Exec(ctx, query, userID, domainID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrDomainRevisionConflict
	}
	var oldState map[string]any
	if before != "" {
		oldState = map[string]any{"status": before}
	}
	return pg.domainAudit(ctx, tx, userID, domainID, action, "membership", oldState, map[string]any{"status": after}, reason)
}

func (pg *Pg) domainAudit(ctx context.Context, tx pgx.Tx, userID, domainID uint64, action, entity string, before, after map[string]any, reason string) error {
	// A nil map would encode as JSON null, which is intentionally rejected by the
	// schema. Pass an untyped nil for SQL NULL instead.
	var oldState any
	if before != nil {
		oldState = before
	}
	_, err := tx.Exec(ctx, pg.queries.InsertDomainMembershipAudit, domainID,
		fmt.Sprintf("user:%d", userID), action, entity, strconv.FormatUint(userID, 10), oldState, after, reason)
	return err
}
