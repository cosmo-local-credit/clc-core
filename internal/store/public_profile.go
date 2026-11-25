package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

var (
	ErrUnsupportedProfileSection = errors.New("unsupported public profile section")
)

type (
	// ProfileVisibility is an assign-all preference object. It deliberately keeps
	// contact/location choices separate from resource sections: the former are
	// available only through the authenticated details read.
	ProfileVisibility struct {
		Enabled           bool `db:"public_profile_enabled"            json:"enabled"`
		Avatar            bool `db:"public_profile_avatar"             json:"avatar"`
		Name              bool `db:"public_profile_name"               json:"name"`
		Bio               bool `db:"public_profile_bio"                json:"bio"`
		Website           bool `db:"public_profile_website"            json:"website"`
		Email             bool `db:"public_profile_email"              json:"email"`
		Phone             bool `db:"public_profile_phone"              json:"phone"`
		Country           bool `db:"public_profile_country"            json:"country"`
		LocationName      bool `db:"public_profile_location_name"      json:"locationName"`
		PreciseLocation   bool `db:"public_profile_precise_location"   json:"preciseLocation"`
		PreferredCurrency bool `db:"public_profile_preferred_currency" json:"preferredCurrency"`
		Vouchers          bool `db:"public_profile_vouchers"           json:"vouchers"`
		Offers            bool `db:"public_profile_offers"             json:"offers"`
		Wants             bool `db:"public_profile_wants"              json:"wants"`
		Pools             bool `db:"public_profile_pools"              json:"pools"`
		Posts             bool `db:"public_profile_posts"              json:"posts"`
		Catalogs          bool `db:"public_profile_catalogs"           json:"catalogs"`
		// Legacy compatibility field; always false because public transaction activity is retired.
		Activity bool `db:"public_profile_activity"           json:"activity"`
	}

	PublicProfile struct {
		ProfileVisibility

		ID                uint64    `db:"id"`
		Address           string    `db:"address" json:"address"`
		Username          *string   `db:"username" json:"username"`
		Name              string    `db:"name"`
		Email             *string   `db:"email"`
		Phone             *string   `db:"phone"`
		Country           string    `db:"country"`
		PreferredCurrency *string   `db:"preferred_currency"`
		Bio               *string   `db:"bio"`
		Website           *string   `db:"website"`
		LocationName      *string   `db:"location_name"`
		GeoLat            *float64  `db:"geo_lat"`
		GeoLng            *float64  `db:"geo_lng"`
		AvatarURL         *string   `db:"avatar_url"`
		CreatedAt         time.Time `db:"created_at"`
	}

	// PublicProfileResource is the deliberately small card shared by section
	// pages. Internal database ids drive the opaque numeric cursor but callers
	// navigate with ResourceID (an address for on-chain resources, an id otherwise).
	PublicProfileResource struct {
		ID          uint64  `db:"id"          json:"id"`
		ResourceID  string  `db:"resource_id" json:"resourceId"`
		Kind        string  `db:"kind"        json:"kind"`
		Name        string  `db:"name"        json:"name"`
		ImageURL    *string `db:"image_url"   json:"imageUrl,omitempty"`
		Description *string `db:"description" json:"description,omitempty"`
		Symbol      *string `db:"symbol"      json:"symbol,omitempty"`
		Management  string  `db:"management" json:"management"`
	}

	PublicProfileReference struct {
		Address   string  `db:"address"    json:"address"`
		Username  *string `db:"username"   json:"username"`
		AvatarURL *string `db:"avatar_url" json:"avatarUrl,omitempty"`
	}
)

func (pg *Pg) GetProfileVisibility(ctx context.Context, tx pgx.Tx, address string) (ProfileVisibility, error) {
	var visibility ProfileVisibility
	err := pgxscan.Get(ctx, tx, &visibility, pg.queries.GetProfileVisibility, address)
	return visibility, err
}

func (pg *Pg) UpdateProfileVisibility(ctx context.Context, tx pgx.Tx, address string, visibility ProfileVisibility) error {
	command, err := tx.Exec(ctx, pg.queries.UpdateProfileVisibility,
		address, visibility.Enabled, visibility.Avatar, visibility.Name,
		visibility.Bio, visibility.Website, visibility.Email, visibility.Phone,
		visibility.Country, visibility.LocationName, visibility.PreciseLocation,
		visibility.PreferredCurrency, visibility.Vouchers, visibility.Offers,
		visibility.Wants, visibility.Pools, visibility.Posts, visibility.Catalogs)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (pg *Pg) GetPublicProfile(ctx context.Context, tx pgx.Tx, address string) (PublicProfile, error) {
	var profile PublicProfile
	err := pgxscan.Get(ctx, tx, &profile, pg.queries.GetPublicProfile, address)
	return profile, err
}

func (pg *Pg) ListPublicProfileResources(
	ctx context.Context,
	tx pgx.Tx,
	address string,
	section string,
	cursor uint64,
	limit uint64,
) ([]PublicProfileResource, error) {
	var query string
	switch strings.ToLower(section) {
	case "vouchers":
		query = pg.queries.ListPublicProfileVouchers
	case "offers":
		query = pg.queries.ListPublicProfileOffers
	case "wants":
		query = pg.queries.ListPublicProfileWants
	case "pools":
		query = pg.queries.ListPublicProfilePools
	case "posts":
		query = pg.queries.ListPublicProfilePosts
	case "catalogs":
		query = pg.queries.ListPublicProfileCatalogs
	default:
		return nil, ErrUnsupportedProfileSection
	}
	var resources []PublicProfileResource
	if err := pgxscan.Select(ctx, tx, &resources, query, address, cursor, limit); err != nil {
		return nil, err
	}
	return resources, nil
}

func (pg *Pg) ResolvePublicResourceProfiles(ctx context.Context, tx pgx.Tx, kind, address string, id, chainID int64) ([]PublicProfileReference, error) {
	var profiles []PublicProfileReference
	if err := pgxscan.Select(ctx, tx, &profiles, pg.queries.ResolvePublicResourceProfiles, kind, address, id, chainID); err != nil {
		return nil, err
	}
	return profiles, nil
}
