package store

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

type (
	WebAuthnCredential struct {
		CredentialID string    `db:"credential_id" json:"credentialId"`
		Address      string    `db:"address"       json:"address"`
		KeyHash      string    `db:"key_hash"      json:"keyHash"`
		PublicKeyX   string    `db:"public_key_x"  json:"publicKeyX"`
		PublicKeyY   string    `db:"public_key_y"  json:"publicKeyY"`
		CreatedAt    time.Time `db:"created_at"    json:"createdAt"`
	}
)

func (pg *Pg) UpsertWebAuthnCredential(ctx context.Context, tx pgx.Tx, cred WebAuthnCredential) error {
	_, err := tx.Exec(
		ctx,
		pg.queries.UpsertWebAuthnCredential,
		cred.CredentialID,
		cred.Address,
		cred.KeyHash,
		cred.PublicKeyX,
		cred.PublicKeyY,
	)
	return err
}

func (pg *Pg) GetWebAuthnCredential(ctx context.Context, tx pgx.Tx, credentialID string) (WebAuthnCredential, error) {
	var cred WebAuthnCredential

	rows, err := tx.Query(ctx, pg.queries.GetWebAuthnCredential, credentialID)
	if err != nil {
		return cred, err
	}

	if err := pgxscan.ScanOne(&cred, rows); err != nil {
		return cred, err
	}

	return cred, nil
}
