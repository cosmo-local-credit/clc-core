package store

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

const (
	SNGasApproverStatusPending = "PENDING"
	SNGasApproverStatusSuccess = "SUCCESS"
)

type SNGasApproverAccount struct {
	TrackingID string    `db:"tracking_id" json:"trackingId"`
	Address    string    `db:"address" json:"address"`
	UsersIndex string    `db:"users_index" json:"usersIndex"`
	Active     bool      `db:"active" json:"active"`
	Status     string    `db:"status" json:"status"`
	CreatedAt  time.Time `db:"created_at" json:"createdAt"`
	UpdatedAt  time.Time `db:"updated_at" json:"updatedAt"`
}

func (pg *Pg) InsertSNGasApproverAccount(ctx context.Context, tx pgx.Tx, account SNGasApproverAccount) error {
	_, err := tx.Exec(
		ctx,
		pg.queries.InsertSNGasApproverAccount,
		account.TrackingID,
		account.Address,
		account.UsersIndex,
	)

	return err
}

func (pg *Pg) GetSNGasApproverAccountByTrackingID(ctx context.Context, tx pgx.Tx, trackingID string) (SNGasApproverAccount, error) {
	var account SNGasApproverAccount

	rows, err := tx.Query(ctx, pg.queries.GetSNGasApproverAccountByTrackingID, trackingID)
	if err != nil {
		return account, err
	}

	if err := pgxscan.ScanOne(&account, rows); err != nil {
		return account, err
	}

	return account, nil
}

func (pg *Pg) GetSNGasApproverAccountByAddress(ctx context.Context, tx pgx.Tx, address string) (SNGasApproverAccount, error) {
	var account SNGasApproverAccount

	rows, err := tx.Query(ctx, pg.queries.GetSNGasApproverAccountByAddress, address)
	if err != nil {
		return account, err
	}

	if err := pgxscan.ScanOne(&account, rows); err != nil {
		return account, err
	}

	return account, nil
}

func (pg *Pg) UpdateSNGasApproverAccountStatus(ctx context.Context, tx pgx.Tx, trackingID string, status string) error {
	_, err := tx.Exec(ctx, pg.queries.UpdateSNGasApproverAccountStatus, trackingID, status)
	return err
}
