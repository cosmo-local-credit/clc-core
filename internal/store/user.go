package store

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

type (
	User struct {
		ID              uint64    `db:"id"               json:"id"`
		EthereumAddress string    `db:"ethereum_address" json:"ethereumAddress"`
		Name            string    `db:"name"             json:"name"`
		Email           string    `db:"email"            json:"email"`
		Phone           string    `db:"phone"            json:"phone"`
		Country         string    `db:"country"          json:"country"`
		LocationName    *string   `db:"location_name"    json:"locationName,omitempty"`
		GeoLat          *float64  `db:"geo_lat"          json:"geoLat,omitempty"`
		GeoLng          *float64  `db:"geo_lng"          json:"geoLng,omitempty"`
		Role            string    `db:"role"             json:"role"`
		Status          string    `db:"status"           json:"status"`
		MaxPools        int       `db:"max_pools"        json:"maxPools"`
		MaxTokens       int       `db:"max_tokens"       json:"maxTokens"`
		CreatedAt       time.Time `db:"created_at"       json:"createdAt"`
		UpdatedAt       time.Time `db:"updated_at"       json:"updatedAt"`
	}

	UserProfileUpdate struct {
		Address       string
		Name          string
		Email         string
		Phone         string
		Country       string
		ClearLocation bool
		LocationName  *string
		GeoLat        *float64
		GeoLng        *float64
	}
)

func (pg *Pg) InsertUser(ctx context.Context, tx pgx.Tx, u User) (User, error) {
	var inserted User

	row, err := tx.Query(
		ctx,
		pg.queries.InsertUser,
		u.EthereumAddress,
		u.Name,
		u.Email,
		u.Role,
		u.Status,
		u.MaxPools,
		u.MaxTokens,
	)
	if err != nil {
		return inserted, err
	}

	if err := pgxscan.ScanOne(&inserted, row); err != nil {
		return inserted, err
	}

	return inserted, nil
}

func (pg *Pg) GetUserByAddress(ctx context.Context, tx pgx.Tx, address string) (User, error) {
	var u User

	row, err := tx.Query(ctx, pg.queries.GetUserByAddress, address)
	if err != nil {
		return u, err
	}

	if err := pgxscan.ScanOne(&u, row); err != nil {
		return u, err
	}

	return u, nil
}

func (pg *Pg) GetUserByEmail(ctx context.Context, tx pgx.Tx, email string) (User, error) {
	var u User

	row, err := tx.Query(ctx, pg.queries.GetUserByEmail, email)
	if err != nil {
		return u, err
	}

	if err := pgxscan.ScanOne(&u, row); err != nil {
		return u, err
	}

	return u, nil
}

func (pg *Pg) GetUserByID(ctx context.Context, tx pgx.Tx, id uint64) (User, error) {
	var u User

	row, err := tx.Query(ctx, pg.queries.GetUserByID, id)
	if err != nil {
		return u, err
	}

	if err := pgxscan.ScanOne(&u, row); err != nil {
		return u, err
	}

	return u, nil
}

func (pg *Pg) UpdateUserStatus(ctx context.Context, tx pgx.Tx, address string, status string) (User, error) {
	var u User

	row, err := tx.Query(ctx, pg.queries.UpdateUserStatus, address, status)
	if err != nil {
		return u, err
	}

	if err := pgxscan.ScanOne(&u, row); err != nil {
		return u, err
	}

	return u, nil
}

func (pg *Pg) ApprovePendingUserByAddress(ctx context.Context, tx pgx.Tx, address string) (User, error) {
	var u User

	row, err := tx.Query(ctx, pg.queries.ApprovePendingUserByAddress, address)
	if err != nil {
		return u, err
	}

	if err := pgxscan.ScanOne(&u, row); err != nil {
		return u, err
	}

	return u, nil
}

func (pg *Pg) UpdateUserLimits(ctx context.Context, tx pgx.Tx, address string, maxPools int, maxTokens int) (User, error) {
	var u User

	row, err := tx.Query(ctx, pg.queries.UpdateUserLimits, address, maxPools, maxTokens)
	if err != nil {
		return u, err
	}

	if err := pgxscan.ScanOne(&u, row); err != nil {
		return u, err
	}

	return u, nil
}

func (pg *Pg) UpdateUserProfile(ctx context.Context, tx pgx.Tx, update UserProfileUpdate) (User, error) {
	var u User

	row, err := tx.Query(
		ctx,
		pg.queries.UpdateUserProfile,
		update.Address,
		update.Name,
		update.Email,
		update.Phone,
		update.Country,
		update.ClearLocation,
		update.LocationName,
		update.GeoLat,
		update.GeoLng,
	)
	if err != nil {
		return u, err
	}

	if err := pgxscan.ScanOne(&u, row); err != nil {
		return u, err
	}

	return u, nil
}

func (pg *Pg) ListUsers(ctx context.Context, tx pgx.Tx) ([]User, error) {
	var users []User

	rows, err := tx.Query(ctx, pg.queries.ListUsers)
	if err != nil {
		return nil, err
	}

	if err := pgxscan.ScanAll(&users, rows); err != nil {
		return nil, err
	}

	return users, nil
}

func (pg *Pg) ListUsersByStatus(ctx context.Context, tx pgx.Tx, status string) ([]User, error) {
	var users []User

	rows, err := tx.Query(ctx, pg.queries.ListUsersByStatus, status)
	if err != nil {
		return nil, err
	}

	if err := pgxscan.ScanAll(&users, rows); err != nil {
		return nil, err
	}

	return users, nil
}
