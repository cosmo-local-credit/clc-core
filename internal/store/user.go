package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

type (
	User struct {
		ID                uint64    `db:"id"               json:"id"`
		EthereumAddress   string    `db:"ethereum_address" json:"ethereumAddress"`
		Username          *string   `db:"username" json:"username"`
		Name              string    `db:"name"             json:"name"`
		Email             string    `db:"email"            json:"email"`
		Phone             string    `db:"phone"            json:"phone"`
		Country           string    `db:"country"          json:"country"`
		PreferredCurrency *string   `db:"preferred_currency" json:"preferredCurrency,omitempty"`
		Bio               *string   `db:"bio"              json:"bio,omitempty"`
		Website           *string   `db:"website"          json:"website,omitempty"`
		LocationName      *string   `db:"location_name"    json:"locationName,omitempty"`
		GeoLat            *float64  `db:"geo_lat"          json:"geoLat,omitempty"`
		GeoLng            *float64  `db:"geo_lng"          json:"geoLng,omitempty"`
		AvatarURL         *string   `db:"avatar_url"       json:"avatarUrl,omitempty"`
		Role              string    `db:"role"             json:"role"`
		Status            string    `db:"status"           json:"status"`
		MaxPools          int       `db:"max_pools"        json:"maxPools"`
		MaxTokens         int       `db:"max_tokens"       json:"maxTokens"`
		MaxCatalogs       int       `db:"max_catalogs" json:"maxCatalogs"`
		MaxOffers         int       `db:"max_offers" json:"maxOffers"`
		MaxWants          int       `db:"max_wants" json:"maxWants"`
		CreatedAt         time.Time `db:"created_at"       json:"createdAt"`
		UpdatedAt         time.Time `db:"updated_at"       json:"updatedAt"`
	}

	UserProfileUpdate struct {
		Address  string
		Username *string
		Name     string
		Email    string
		Phone    string
		Country  string
		// These fields use patch semantics so an older app that omits them does
		// not erase a newer profile. A non-nil empty bio or website clears it.
		PreferredCurrency *string
		Bio               *string
		Website           *string
		ClearLocation     bool
		LocationName      *string
		GeoLat            *float64
		GeoLng            *float64
		// The avatar follows the same shape as the location: omitted preserves,
		// and clearing is an explicit flag rather than a magic value, so a client
		// that knows nothing about avatars cannot remove one.
		ClearAvatar bool
		AvatarURL   *string
	}
)

// InsertUser uses database defaults for all resource allowances. Subsequent
// changes go through UpdateUserLimits so signup cannot choose its own quota.
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

func (pg *Pg) UpdateUserLimits(ctx context.Context, tx pgx.Tx, address string, limits UserLimitsUpdate) (User, error) {
	var u User

	row, err := tx.Query(ctx, pg.queries.UpdateUserLimits, address, limits.MaxPools, limits.MaxTokens, limits.MaxCatalogs, limits.MaxOffers, limits.MaxWants)
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
	if update.Username == nil && strings.TrimSpace(update.Name) != "" {
		var current *string
		if err := tx.QueryRow(ctx, `SELECT username FROM users WHERE lower(blockchain_address) = lower($1) FOR UPDATE`, update.Address).Scan(&current); err != nil {
			return u, err
		}
		if current == nil {
			name, err := pg.assignUsername(ctx, tx, update.Address, update.Name)
			if err != nil && !errors.Is(err, ErrNoUsernameAvailable) {
				return u, err
			}
			// Exhausted suggestions must not prevent profile completion. A later
			// profile save can retry or supply an explicitly chosen username.
			if err == nil {
				update.Username = &name
			}
		}
	}

	row, err := tx.Query(
		ctx,
		pg.queries.UpdateUserProfile,
		update.Address,
		update.Name,
		update.Email,
		update.Phone,
		update.Country,
		update.PreferredCurrency,
		update.Bio,
		update.Website,
		update.ClearLocation,
		update.LocationName,
		update.GeoLat,
		update.GeoLng,
		update.ClearAvatar,
		update.AvatarURL,
		update.Username,
	)
	if err != nil {
		return u, err
	}

	if err := pgxscan.ScanOne(&u, row); err != nil {
		return u, err
	}

	return u, nil
}

// Listing the directory lives in user_search.go: the endpoint that reads it now
// pages and searches, and the whole-table statements this file used to hold had
// no other caller.
