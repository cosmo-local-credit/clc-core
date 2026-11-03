package store

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

const (
	RotationByUser  = "user"
	RotationByAdmin = "admin"
)

type (
	// AddressRotation records one account moving off a retired address.
	AddressRotation struct {
		ID          int64     `db:"id"            json:"id"`
		UserID      uint64    `db:"user_id"       json:"userId"`
		OldAddress  string    `db:"old_address"   json:"oldAddress"`
		NewAddress  string    `db:"new_address"   json:"newAddress"`
		InitiatedBy string    `db:"initiated_by"  json:"initiatedBy"`
		AdminUserID *uint64   `db:"admin_user_id" json:"adminUserId,omitempty"`
		CreatedAt   time.Time `db:"created_at"    json:"createdAt"`
	}

	// AddressRotationRequest moves UserID from OldAddress to NewAddress. Both
	// addresses are EIP-55 checksummed. AdminUserID is set only when
	// InitiatedBy is RotationByAdmin.
	AddressRotationRequest struct {
		UserID      uint64
		OldAddress  string
		NewAddress  string
		InitiatedBy string
		AdminUserID *uint64
	}

	AddressRotationResult struct {
		User          User
		Rotation      AddressRotation
		MovedPasskeys []WebAuthnCredential
		// Replayed is true when this exact rotation had already committed; the
		// result describes it and nothing was written.
		Replayed bool
	}
)

var (
	// ErrAddressRetired: the old address was already rotated away to a
	// different address or by a different account.
	ErrAddressRetired = errors.New("sign-in card was replaced")
	// ErrAddressRotationStale: the account no longer holds the old address.
	ErrAddressRotationStale = errors.New("account address has changed")
	// ErrAddressRotationTargetTaken: the new address already has an account.
	ErrAddressRotationTargetTaken = errors.New("new address already belongs to an account")
	// ErrAddressRotationDeploying: a deployment for the account is in flight
	// and would be cataloged under the old address when it confirms.
	ErrAddressRotationDeploying = errors.New("a deployment is still in progress")
)

// RotateUserAddress moves an account to a new address in tx. Everything keyed
// by user id follows by itself; this rewrites the rows keyed by address,
// re-points passkeys as 'moved' and retires the old address for good.
//
// An identical rotation that already committed is answered as Replayed so a
// client that lost the response can retry with fresh proofs.
func (pg *Pg) RotateUserAddress(ctx context.Context, tx pgx.Tx, req AddressRotationRequest) (AddressRotationResult, error) {
	var result AddressRotationResult

	// Both addresses, in a fixed order: the users trigger takes the same key on
	// the address it writes, so a sign-in that would re-register the old one
	// waits for this transaction and then sees it retired.
	keys := []string{strings.ToLower(req.OldAddress), strings.ToLower(req.NewAddress)}
	sort.Strings(keys)
	for _, key := range keys {
		if _, err := tx.Exec(ctx, pg.queries.LockUserAddressRotation, key); err != nil {
			return result, err
		}
	}

	existing, err := pg.scanAddressRotation(ctx, tx, pg.queries.GetAddressRotationByOld, req.OldAddress)
	switch {
	case err == nil:
		if existing.UserID != req.UserID || !strings.EqualFold(existing.NewAddress, req.NewAddress) {
			return result, ErrAddressRetired
		}
		user, err := pg.GetUserByID(ctx, tx, req.UserID)
		if err != nil {
			return result, err
		}
		moved, err := pg.listWebAuthn(ctx, tx, pg.queries.ListMovedWebAuthn, req.NewAddress)
		if err != nil {
			return result, err
		}
		return AddressRotationResult{User: user, Rotation: existing, MovedPasskeys: moved, Replayed: true}, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return result, err
	}

	var current string
	if err := tx.QueryRow(ctx, pg.queries.LockUserForRotation, req.UserID).Scan(&current); err != nil {
		return result, err
	}
	if !strings.EqualFold(current, req.OldAddress) {
		return result, ErrAddressRotationStale
	}

	if _, err := pg.GetUserByAddress(ctx, tx, req.NewAddress); err == nil {
		return result, ErrAddressRotationTargetTaken
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return result, err
	}

	var deploying int64
	if err := tx.QueryRow(ctx, pg.queries.CountInFlightUserDeployments, req.UserID).Scan(&deploying); err != nil {
		return result, err
	}
	if deploying > 0 {
		return result, ErrAddressRotationDeploying
	}

	rows, err := tx.Query(ctx, pg.queries.RotateUserAddress, req.OldAddress, req.NewAddress, req.UserID)
	if err != nil {
		return result, err
	}
	if err := pgxscan.ScanOne(&result.User, rows); err != nil {
		return result, err
	}

	var rewritten int64
	if err := tx.QueryRow(ctx, pg.queries.RewriteRotatedAddressRefs, req.OldAddress, req.NewAddress).Scan(&rewritten); err != nil {
		return result, err
	}

	result.MovedPasskeys, err = pg.listWebAuthn(ctx, tx, pg.queries.MoveRotatedWebAuthn, req.OldAddress, req.NewAddress)
	if err != nil {
		return result, err
	}

	result.Rotation, err = pg.scanAddressRotation(ctx, tx, pg.queries.InsertAddressRotation,
		req.UserID, req.OldAddress, req.NewAddress, req.InitiatedBy, req.AdminUserID)
	if err != nil {
		return result, err
	}
	return result, nil
}

func (pg *Pg) ListAddressRotationsByUser(ctx context.Context, tx pgx.Tx, userID uint64) ([]AddressRotation, error) {
	rotations := []AddressRotation{}
	if err := pgxscan.Select(ctx, tx, &rotations, pg.queries.ListAddressRotationsByUser, userID); err != nil {
		return nil, err
	}
	return rotations, nil
}

func (pg *Pg) AddressIsRetired(ctx context.Context, tx pgx.Tx, address string) (bool, error) {
	var retired bool
	err := tx.QueryRow(ctx, pg.queries.AddressIsRetired, address).Scan(&retired)
	return retired, err
}

func (pg *Pg) scanAddressRotation(ctx context.Context, tx pgx.Tx, query string, args ...any) (AddressRotation, error) {
	var rotation AddressRotation
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return rotation, err
	}
	if err := pgxscan.ScanOne(&rotation, rows); err != nil {
		return rotation, err
	}
	return rotation, nil
}

func (pg *Pg) listWebAuthn(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]WebAuthnCredential, error) {
	creds := []WebAuthnCredential{}
	if err := pgxscan.Select(ctx, tx, &creds, query, args...); err != nil {
		return nil, err
	}
	return creds, nil
}
