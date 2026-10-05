package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

// WebAuthn credential states. Only WebAuthnActive is trusted for sign-in; see
// migration 038 for what each one means.
const (
	WebAuthnPending    = "pending"
	WebAuthnActive     = "active"
	WebAuthnFailed     = "failed"
	WebAuthnLegacy     = "legacy"
	WebAuthnUnverified = "unverified"
)

type (
	WebAuthnCredential struct {
		CredentialID string    `db:"credential_id" json:"credentialId"`
		Address      string    `db:"address"       json:"address"`
		KeyHash      string    `db:"key_hash"      json:"keyHash"`
		PublicKeyX   string    `db:"public_key_x"  json:"publicKeyX"`
		PublicKeyY   string    `db:"public_key_y"  json:"publicKeyY"`
		TrackingID   *string   `db:"tracking_id"   json:"trackingId"`
		State        string    `db:"state"         json:"state"`
		CreatedAt    time.Time `db:"created_at"    json:"createdAt"`
	}

	// WebAuthnRegistrationError says why a credential cannot be registered
	// under the requested tracking id. Existing is the row already holding the
	// credential id.
	WebAuthnRegistrationError struct {
		Reason   error
		Existing WebAuthnCredential
	}
)

var (
	// ErrWebAuthnCredentialConflict: the credential id is already mapped to a
	// different address or key.
	ErrWebAuthnCredentialConflict = errors.New("webauthn credential id is mapped to a different wallet or key")
	// ErrWebAuthnRegistrationPending: the same mapping is already waiting on a
	// transaction; follow Existing.TrackingID instead of relaying again.
	ErrWebAuthnRegistrationPending = errors.New("webauthn registration is already pending")
	// ErrWebAuthnAlreadyRegistered: the same mapping is already active (or a
	// legacy row still awaiting its chain check).
	ErrWebAuthnAlreadyRegistered = errors.New("webauthn credential is already registered")
)

func (e *WebAuthnRegistrationError) Error() string { return e.Reason.Error() }
func (e *WebAuthnRegistrationError) Unwrap() error { return e.Reason }

// CheckWebAuthnRegistration decides whether cred may be recorded as a new
// pending registration given the row, if any, already holding its credential
// id. It returns nil when there is no row or the row is a failed attempt at
// the same mapping, which may be retried.
func CheckWebAuthnRegistration(existing *WebAuthnCredential, cred WebAuthnCredential) error {
	if existing == nil {
		return nil
	}
	if existing.Address != cred.Address || existing.KeyHash != cred.KeyHash {
		return &WebAuthnRegistrationError{Reason: ErrWebAuthnCredentialConflict, Existing: *existing}
	}
	switch existing.State {
	case WebAuthnFailed, WebAuthnUnverified:
		return nil
	case WebAuthnPending:
		return &WebAuthnRegistrationError{Reason: ErrWebAuthnRegistrationPending, Existing: *existing}
	default:
		return &WebAuthnRegistrationError{Reason: ErrWebAuthnAlreadyRegistered, Existing: *existing}
	}
}

// RecordWebAuthnRegistration writes cred as a pending registration tracked by
// cred.TrackingID, or re-arms a failed attempt at the same mapping. Any other
// existing row is returned as a *WebAuthnRegistrationError and nothing is
// written, so the caller's transaction (and the relay it carries) can abort.
func (pg *Pg) RecordWebAuthnRegistration(ctx context.Context, tx pgx.Tx, cred WebAuthnCredential) error {
	if cred.TrackingID == nil || *cred.TrackingID == "" {
		return errors.New("webauthn: registration requires a tracking id")
	}

	var existing *WebAuthnCredential
	row, err := pg.scanWebAuthnCredential(ctx, tx, pg.queries.GetWebAuthnCredentialForUpdate, cred.CredentialID)
	switch {
	case err == nil:
		existing = &row
	case !errors.Is(err, pgx.ErrNoRows):
		return err
	}

	if err := CheckWebAuthnRegistration(existing, cred); err != nil {
		return err
	}

	if existing != nil {
		tag, err := tx.Exec(ctx, pg.queries.RearmWebAuthnCredential, cred.CredentialID, *cred.TrackingID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("webauthn: re-arm %s affected %d rows", cred.CredentialID, tag.RowsAffected())
		}
		return nil
	}

	_, err = tx.Exec(
		ctx,
		pg.queries.InsertWebAuthnCredential,
		cred.CredentialID,
		cred.Address,
		cred.KeyHash,
		cred.PublicKeyX,
		cred.PublicKeyY,
		*cred.TrackingID,
	)
	return err
}

// GetWebAuthnCredential returns the row for credentialID in any state. It must
// not back an authentication decision; use GetActiveWebAuthnCredential.
func (pg *Pg) GetWebAuthnCredential(ctx context.Context, tx pgx.Tx, credentialID string) (WebAuthnCredential, error) {
	return pg.scanWebAuthnCredential(ctx, tx, pg.queries.GetWebAuthnCredential, credentialID)
}

// GetActiveWebAuthnCredential returns the credential only once its
// registration is confirmed on chain; otherwise pgx.ErrNoRows.
func (pg *Pg) GetActiveWebAuthnCredential(ctx context.Context, tx pgx.Tx, credentialID string) (WebAuthnCredential, error) {
	return pg.scanWebAuthnCredential(ctx, tx, pg.queries.GetActiveWebAuthnCredential, credentialID)
}

func (pg *Pg) ListWebAuthnCredentialsByAddress(ctx context.Context, tx pgx.Tx, address string) ([]WebAuthnCredential, error) {
	var creds []WebAuthnCredential
	if err := pgxscan.Select(ctx, tx, &creds, pg.queries.ListWebAuthnCredentialsByAddress, address); err != nil {
		return nil, err
	}
	return creds, nil
}

func (pg *Pg) ListLegacyWebAuthnCredentials(ctx context.Context, tx pgx.Tx) ([]WebAuthnCredential, error) {
	var creds []WebAuthnCredential
	if err := pgxscan.Select(ctx, tx, &creds, pg.queries.ListLegacyWebAuthnCredentials); err != nil {
		return nil, err
	}
	return creds, nil
}

// ResolveLegacyWebAuthnCredential moves a legacy row to state (active or
// unverified). A row that has left 'legacy' in the meantime is left alone.
func (pg *Pg) ResolveLegacyWebAuthnCredential(ctx context.Context, tx pgx.Tx, credentialID, state string) error {
	if state != WebAuthnActive && state != WebAuthnUnverified {
		return fmt.Errorf("webauthn: legacy credential cannot resolve to %q", state)
	}
	_, err := tx.Exec(ctx, pg.queries.ResolveLegacyWebAuthnCredential, credentialID, state)
	return err
}

func (pg *Pg) scanWebAuthnCredential(ctx context.Context, tx pgx.Tx, query string, args ...any) (WebAuthnCredential, error) {
	var cred WebAuthnCredential

	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return cred, err
	}

	if err := pgxscan.ScanOne(&cred, rows); err != nil {
		return cred, err
	}

	return cred, nil
}
