package store

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests exercise the real users_geo_valid constraint and the
// update-user-profile COALESCE behavior against a live database. They are
// skipped unless CORE_TEST_DSN points at one, so `go test ./...` stays
// hermetic on machines without Postgres.
//
//	CORE_TEST_DSN=postgres://postgres:postgres@127.0.0.1:5432/clc_core go test ./internal/store/

func TestUpdateUserProfilePreservesGeoWhenOmitted(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	address := "0x" + strings.Repeat("a", 40)
	email := "geo-preserve-test@example.com"

	_, err := pg.InsertUser(ctx, tx, User{
		EthereumAddress: address,
		Name:            "Geo Test",
		Email:           email,
		Role:            "USER",
		Status:          "APPROVED",
		MaxPools:        10,
		MaxTokens:       20,
	})
	require.NoError(t, err)

	lat := -1.2921
	lng := 36.8219
	locName := "Nairobi"

	set, err := pg.UpdateUserProfile(ctx, tx, UserProfileUpdate{
		Address: address, Name: "Geo Test", Email: email, Country: "KE",
		LocationName: &locName, GeoLat: &lat, GeoLng: &lng,
	})
	require.NoError(t, err)
	require.NotNil(t, set.GeoLat)
	require.NotNil(t, set.GeoLng)
	require.NotNil(t, set.LocationName)
	assert.Equal(t, lat, *set.GeoLat)
	assert.Equal(t, lng, *set.GeoLng)
	assert.Equal(t, "Nairobi", *set.LocationName)

	// A later update that only touches an unrelated field (phone) and omits
	// geo entirely must preserve the previously captured location, not null
	// it out.
	again, err := pg.UpdateUserProfile(ctx, tx, UserProfileUpdate{
		Address: address, Name: "Geo Test", Email: email, Phone: "+254700000000", Country: "KE",
	})
	require.NoError(t, err)
	require.NotNil(t, again.GeoLat, "geo_lat must be preserved when omitted from an update")
	require.NotNil(t, again.GeoLng, "geo_lng must be preserved when omitted from an update")
	require.NotNil(t, again.LocationName, "location_name must be preserved when omitted from an update")
	assert.Equal(t, lat, *again.GeoLat)
	assert.Equal(t, lng, *again.GeoLng)
	assert.Equal(t, "Nairobi", *again.LocationName)

	// A later update that provides new coordinates still overwrites, as
	// expected.
	movedLat := 51.5072
	movedLng := -0.1276
	movedName := "London"
	moved, err := pg.UpdateUserProfile(ctx, tx, UserProfileUpdate{
		Address: address, Name: "Geo Test", Email: email, Phone: "+254700000000", Country: "KE",
		LocationName: &movedName, GeoLat: &movedLat, GeoLng: &movedLng,
	})
	require.NoError(t, err)
	assert.Equal(t, movedLat, *moved.GeoLat)
	assert.Equal(t, movedLng, *moved.GeoLng)
	assert.Equal(t, "London", *moved.LocationName)

	cleared, err := pg.UpdateUserProfile(ctx, tx, UserProfileUpdate{
		Address: address, Name: "Geo Test", Email: email, Phone: "+254700000000", Country: "KE",
		ClearLocation: true,
	})
	require.NoError(t, err)
	assert.Nil(t, cleared.LocationName)
	assert.Nil(t, cleared.GeoLat)
	assert.Nil(t, cleared.GeoLng)
}

func TestUsersGeoValidRejectsPartialCoordinates(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	address := "0x" + strings.Repeat("b", 40)
	email := "geo-partial-test@example.com"

	_, err := pg.InsertUser(ctx, tx, User{
		EthereumAddress: address,
		Name:            "Geo Partial Test",
		Email:           email,
		Role:            "USER",
		Status:          "APPROVED",
		MaxPools:        10,
		MaxTokens:       20,
	})
	require.NoError(t, err)

	lat := 1.0
	_, err = pg.UpdateUserProfile(ctx, tx, UserProfileUpdate{
		Address: address, Name: "Geo Partial Test", Email: email, Country: "KE", GeoLat: &lat,
	})
	require.Error(t, err, "users_geo_valid must reject a lat without a matching lng")
}

// The avatar is COALESCEd for the same reason the geo columns are: an older
// client editing a name must not wipe a picture it knows nothing about.
func TestUpdateUserProfilePreservesAvatarWhenOmitted(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	address, email := seedProfileUser(t, pg, tx)
	avatar := "https://content.example/profile/abc_400_a3f91c2e.webp"

	base := UserProfileUpdate{Address: address, Name: "Avatar Test", Email: email, Country: "KE"}

	set := base
	set.AvatarURL = &avatar
	stored, err := pg.UpdateUserProfile(ctx, tx, set)
	require.NoError(t, err)
	require.NotNil(t, stored.AvatarURL)
	assert.Equal(t, avatar, *stored.AvatarURL)

	again, err := pg.UpdateUserProfile(ctx, tx, base)
	require.NoError(t, err)
	require.NotNil(t, again.AvatarURL, "an omitted avatar must not clear the stored one")
	assert.Equal(t, avatar, *again.AvatarURL)

	clear := base
	clear.ClearAvatar = true
	cleared, err := pg.UpdateUserProfile(ctx, tx, clear)
	require.NoError(t, err)
	assert.Nil(t, cleared.AvatarURL, "clearAvatar must remove the stored avatar")
}

func TestUpdateUserProfileRejectsANonHttpAvatar(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	address, email := seedProfileUser(t, pg, tx)
	dataURI := "data:image/jpeg;base64,/9j/4AAQSkZJRg"

	_, err := pg.UpdateUserProfile(ctx, tx, UserProfileUpdate{
		Address: address, Name: "Avatar Test", Email: email, Country: "KE", AvatarURL: &dataURI,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "users_avatar_url_http")
}

// One approved account, addressed uniquely so two tests in one run cannot collide
// on the users_blockchain_address_unique index.
func seedProfileUser(t *testing.T, pg *Pg, tx pgx.Tx) (address, email string) {
	t.Helper()

	address = randomAddress(t, tx)
	email = strings.ToLower(strings.TrimPrefix(address, "0x")) + "@example.com"
	_, err := pg.InsertUser(context.Background(), tx, User{
		EthereumAddress: address,
		Name:            "Avatar Test",
		Email:           email,
		Role:            "USER",
		Status:          "APPROVED",
		MaxPools:        10,
		MaxTokens:       20,
	})
	require.NoError(t, err)
	return address, email
}
