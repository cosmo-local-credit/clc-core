package store

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

type (
	UserDeployment struct {
		ID           uint64    `db:"id"            json:"id"`
		UserID       uint64    `db:"user_id"       json:"userId"`
		TrackingID   string    `db:"tracking_id"   json:"trackingId"`
		ResourceType string    `db:"resource_type" json:"resourceType"`
		CreatedAt    time.Time `db:"created_at"    json:"createdAt"`
	}

	UserDeploymentWithUser struct {
		ID              uint64    `db:"id"               json:"id"`
		UserID          uint64    `db:"user_id"          json:"userId"`
		TrackingID      string    `db:"tracking_id"      json:"trackingId"`
		ResourceType    string    `db:"resource_type"    json:"resourceType"`
		CreatedAt       time.Time `db:"created_at"       json:"createdAt"`
		EthereumAddress string    `db:"ethereum_address" json:"ethereumAddress"`
		UserName        string    `db:"user_name"        json:"userName"`
	}
)

func (pg *Pg) InsertUserDeployment(ctx context.Context, tx pgx.Tx, ud UserDeployment) error {
	_, err := tx.Exec(ctx, pg.queries.InsertUserDeployment, ud.UserID, ud.TrackingID, ud.ResourceType)
	return err
}

func (pg *Pg) CountUserTokens(ctx context.Context, tx pgx.Tx, userID uint64) (int, error) {
	var count int

	if err := tx.QueryRow(ctx, pg.queries.CountUserTokens, userID).Scan(&count); err != nil {
		return 0, err
	}

	return count, nil
}

func (pg *Pg) CountUserPools(ctx context.Context, tx pgx.Tx, userID uint64) (int, error) {
	var count int

	if err := tx.QueryRow(ctx, pg.queries.CountUserPools, userID).Scan(&count); err != nil {
		return 0, err
	}

	return count, nil
}

func (pg *Pg) ListUserDeployments(ctx context.Context, tx pgx.Tx, userID uint64) ([]UserDeployment, error) {
	var deployments []UserDeployment

	rows, err := tx.Query(ctx, pg.queries.ListUserDeployments, userID)
	if err != nil {
		return nil, err
	}

	if err := pgxscan.ScanAll(&deployments, rows); err != nil {
		return nil, err
	}

	return deployments, nil
}

func (pg *Pg) GetUserDeploymentByTrackingID(ctx context.Context, tx pgx.Tx, trackingID string, userID uint64) (UserDeployment, error) {
	var ud UserDeployment

	row, err := tx.Query(ctx, pg.queries.GetUserDeploymentByTrackingID, trackingID, userID)
	if err != nil {
		return ud, err
	}

	if err := pgxscan.ScanOne(&ud, row); err != nil {
		return ud, err
	}

	return ud, nil
}

func (pg *Pg) ListAllDeployments(ctx context.Context, tx pgx.Tx) ([]UserDeploymentWithUser, error) {
	var deployments []UserDeploymentWithUser

	rows, err := tx.Query(ctx, pg.queries.ListAllDeployments)
	if err != nil {
		return nil, err
	}

	if err := pgxscan.ScanAll(&deployments, rows); err != nil {
		return nil, err
	}

	return deployments, nil
}

func (pg *Pg) ListTokensByUser(ctx context.Context, tx pgx.Tx, userID uint64, limit uint64) ([]DeployedToken, error) {
	var tokens []DeployedToken

	rows, err := tx.Query(ctx, pg.queries.ListTokensByUser, userID, limit)
	if err != nil {
		return nil, err
	}

	if err := pgxscan.ScanAll(&tokens, rows); err != nil {
		return nil, err
	}

	return tokens, nil
}

func (pg *Pg) ListTokensByUserNext(ctx context.Context, tx pgx.Tx, userID uint64, cursor uint64, limit uint64) ([]DeployedToken, error) {
	var tokens []DeployedToken

	rows, err := tx.Query(ctx, pg.queries.ListTokensByUserNext, userID, cursor, limit)
	if err != nil {
		return nil, err
	}

	if err := pgxscan.ScanAll(&tokens, rows); err != nil {
		return nil, err
	}

	return tokens, nil
}

func (pg *Pg) ListTokensByUserPrevious(ctx context.Context, tx pgx.Tx, userID uint64, cursor uint64, limit uint64) ([]DeployedToken, error) {
	var tokens []DeployedToken

	rows, err := tx.Query(ctx, pg.queries.ListTokensByUserPrev, userID, cursor, limit)
	if err != nil {
		return nil, err
	}

	if err := pgxscan.ScanAll(&tokens, rows); err != nil {
		return nil, err
	}

	return tokens, nil
}

func (pg *Pg) ListPoolsByUser(ctx context.Context, tx pgx.Tx, userID uint64, limit uint64) ([]DeployedPool, error) {
	var pools []DeployedPool

	rows, err := tx.Query(ctx, pg.queries.ListPoolsByUser, userID, limit)
	if err != nil {
		return nil, err
	}

	if err := pgxscan.ScanAll(&pools, rows); err != nil {
		return nil, err
	}

	return pools, nil
}

func (pg *Pg) ListPoolsByUserNext(ctx context.Context, tx pgx.Tx, userID uint64, cursor uint64, limit uint64) ([]DeployedPool, error) {
	var pools []DeployedPool

	rows, err := tx.Query(ctx, pg.queries.ListPoolsByUserNext, userID, cursor, limit)
	if err != nil {
		return nil, err
	}

	if err := pgxscan.ScanAll(&pools, rows); err != nil {
		return nil, err
	}

	return pools, nil
}

func (pg *Pg) ListPoolsByUserPrevious(ctx context.Context, tx pgx.Tx, userID uint64, cursor uint64, limit uint64) ([]DeployedPool, error) {
	var pools []DeployedPool

	rows, err := tx.Query(ctx, pg.queries.ListPoolsByUserPrev, userID, cursor, limit)
	if err != nil {
		return nil, err
	}

	if err := pgxscan.ScanAll(&pools, rows); err != nil {
		return nil, err
	}

	return pools, nil
}

func (pg *Pg) ListAllTokens(ctx context.Context, tx pgx.Tx) ([]DeployedToken, error) {
	var tokens []DeployedToken

	rows, err := tx.Query(ctx, pg.queries.ListAllTokens)
	if err != nil {
		return nil, err
	}

	if err := pgxscan.ScanAll(&tokens, rows); err != nil {
		return nil, err
	}

	return tokens, nil
}

func (pg *Pg) ListAllPools(ctx context.Context, tx pgx.Tx) ([]DeployedPool, error) {
	var pools []DeployedPool

	rows, err := tx.Query(ctx, pg.queries.ListAllPools)
	if err != nil {
		return nil, err
	}

	if err := pgxscan.ScanAll(&pools, rows); err != nil {
		return nil, err
	}

	return pools, nil
}
