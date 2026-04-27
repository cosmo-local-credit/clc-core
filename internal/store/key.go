package store

import (
	"context"
	"errors"

	"github.com/cosmo-local-credit/clc-core/internal/keypair"
	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

func (pg *Pg) InsertKeyPair(ctx context.Context, tx pgx.Tx, keypair keypair.Key) error {
	_, err := tx.Exec(ctx, pg.queries.InsertKeyPair, keypair.Public, keypair.Private)
	return err
}

func (pg *Pg) ActivateKeyPair(ctx context.Context, tx pgx.Tx, publicKey string) error {
	_, err := tx.Exec(ctx, pg.queries.ActivateKeyPair, publicKey)
	return err
}

func (pg *Pg) LoadMasterSignerKey(ctx context.Context, tx pgx.Tx) (keypair.Key, error) {
	var key keypair.Key

	row, err := tx.Query(ctx, pg.queries.LoadMasterKey)
	if err != nil {
		return key, err
	}

	if err := pgxscan.ScanOne(&key, row); err != nil {
		return key, err
	}

	return key, nil
}

func (pg *Pg) CheckKeypair(ctx context.Context, tx pgx.Tx, publicKey string) (bool, error) {
	var active bool

	if err := tx.QueryRow(ctx, pg.queries.CheckKeypair, publicKey).Scan(&active); err != nil {
		return false, err
	}

	return active, nil
}

func (pg *Pg) bootstrapMasterSigner(ctx context.Context, tx pgx.Tx) error {
	_, err := pg.LoadMasterSignerKey(ctx, tx)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			masterKeyPair, err := keypair.GenerateKeyPair()
			if err != nil {
				return err
			}

			_, err = tx.Exec(ctx, pg.queries.BootstrapMasterKey, masterKeyPair.Public, masterKeyPair.Private)
			if err != nil {
				return err
			}
		} else {
			return err
		}
	}

	return nil
}
