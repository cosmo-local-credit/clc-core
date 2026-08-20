package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedEditablePool(t *testing.T, tx pgx.Tx) (poolID uint64, address string) {
	t.Helper()

	address = randomAddress(t, tx)
	require.NoError(t, tx.QueryRow(context.Background(), `
		INSERT INTO pools (chain_id, address, name, symbol, decimals, email, phone, location_name, geo_lat, geo_lng)
		VALUES (42220, $1, 'Editable', 'EDT', 6, 'before@example.org', '+254700', 'Nakuru', 1.0, 2.0)
		RETURNING id`, address).Scan(&poolID))
	return poolID, address
}

func readProfile(t *testing.T, tx pgx.Tx, poolID uint64) (email, phone, location *string, lat, lng *float64) {
	t.Helper()
	require.NoError(t, tx.QueryRow(context.Background(), `
		SELECT email, phone, location_name, geo_lat, geo_lng FROM pools WHERE id = $1`,
		poolID).Scan(&email, &phone, &location, &lat, &lng))
	return email, phone, location, lat, lng
}

func TestUpdatePoolProfileAssignsEveryColumn(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	poolID, address := seedEditablePool(t, tx)

	email, site := "after@example.org", "https://after.example"
	name, lat, lng := "Kilifi", -3.5, 39.9

	got, err := pg.UpdatePoolProfile(ctx, tx, address, PoolProfile{
		Email:        &email,
		Website:      &site,
		LocationName: &name,
		GeoLat:       &lat,
		GeoLng:       &lng,
	})
	require.NoError(t, err)
	assert.Equal(t, poolID, got)

	gotEmail, gotPhone, gotLocation, gotLat, _ := readProfile(t, tx, poolID)
	require.NotNil(t, gotEmail)
	assert.Equal(t, email, *gotEmail)
	require.NotNil(t, gotLocation)
	assert.Equal(t, name, *gotLocation)
	require.NotNil(t, gotLat)
	assert.InDelta(t, lat, *gotLat, 0.0001)
	// Assign-all: a field the caller left out is cleared, not preserved. The
	// handler's contract is that the console sends the whole record.
	assert.Nil(t, gotPhone, "an omitted field must clear rather than persist")
}

// The address is the identity and arrives in whatever casing the caller used;
// the catalog stores EIP-55.
func TestUpdatePoolProfileMatchesAddressCaseInsensitively(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()

	poolID, address := seedEditablePool(t, tx)

	got, err := pg.UpdatePoolProfile(context.Background(), tx, lowerHex(address), PoolProfile{})
	require.NoError(t, err)
	assert.Equal(t, poolID, got)
}

func TestUpdatePoolProfileReportsAnUnknownPool(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()

	_, err := pg.UpdatePoolProfile(
		context.Background(), tx, randomAddress(t, tx), PoolProfile{})
	assert.ErrorIs(t, err, pgx.ErrNoRows, "a missing pool must be a 404, not a silent no-op")
}

// Chain-mirroring columns are not in the statement at all, so no edit can put
// the catalog out of step with SwapPool.
func TestUpdatePoolProfileLeavesChainColumnsAlone(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	poolID, address := seedEditablePool(t, tx)

	_, err := pg.UpdatePoolProfile(ctx, tx, address, PoolProfile{})
	require.NoError(t, err)

	var name, symbol string
	var decimals int
	require.NoError(t, tx.QueryRow(ctx,
		`SELECT name, symbol, decimals FROM pools WHERE id = $1`, poolID).Scan(&name, &symbol, &decimals))
	assert.Equal(t, "Editable", name)
	assert.Equal(t, "EDT", symbol)
	assert.Equal(t, 6, decimals)
}

func TestReplacePoolTagsSwapsTheWholeSet(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	poolID, _ := seedEditablePool(t, tx)

	var first, second string
	rows, err := tx.Query(ctx, `SELECT value FROM token_tag ORDER BY value LIMIT 2`)
	require.NoError(t, err)
	values := []string{}
	for rows.Next() {
		var value string
		require.NoError(t, rows.Scan(&value))
		values = append(values, value)
	}
	rows.Close()
	require.Len(t, values, 2)
	first, second = values[0], values[1]

	require.NoError(t, pg.ReplacePoolTags(ctx, tx, poolID, []string{first, second}))
	assert.ElementsMatch(t, []string{first, second}, poolTags(t, tx, poolID))

	// A replace is not a merge: the tag dropped from the set has to go.
	require.NoError(t, pg.ReplacePoolTags(ctx, tx, poolID, []string{second}))
	assert.Equal(t, []string{second}, poolTags(t, tx, poolID))

	require.NoError(t, pg.ReplacePoolTags(ctx, tx, poolID, nil))
	assert.Empty(t, poolTags(t, tx, poolID))
}

func poolTags(t *testing.T, tx pgx.Tx, poolID uint64) []string {
	t.Helper()
	rows, err := tx.Query(context.Background(),
		`SELECT tag_value FROM pool_tags WHERE pool_id = $1 ORDER BY tag_value`, poolID)
	require.NoError(t, err)
	defer rows.Close()

	tags := []string{}
	for rows.Next() {
		var tag string
		require.NoError(t, rows.Scan(&tag))
		tags = append(tags, tag)
	}
	return tags
}

func lowerHex(address string) string {
	lowered := []rune(address)
	for i, r := range lowered {
		if r >= 'A' && r <= 'F' {
			lowered[i] = r + 32
		}
	}
	return string(lowered)
}

// Images are the one exception to this statement's assign-all rule, so they get
// their own test: the deployed console sends no image fields, and if omission
// cleared them it would blank a pool's images every time an operator fixed a
// typo in its description.
func TestUpdatePoolProfilePreservesImagesWhenOmitted(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	poolID, address := seedEditablePool(t, tx)
	image, banner := "https://content.example/pool_400_a3f91c2e.webp", "https://content.example/pool_1280_b1c2d3e4.webp"
	require.NoError(t, func() error {
		_, err := tx.Exec(ctx, `UPDATE pools SET image_url = $2, banner_url = $3 WHERE id = $1`,
			poolID, image, banner)
		return err
	}())

	// A profile edit that says nothing about images.
	desc := "Edited by an older client"
	_, err := pg.UpdatePoolProfile(ctx, tx, address, PoolProfile{Description: &desc})
	require.NoError(t, err)

	gotImage, gotBanner := readPoolImages(t, tx, poolID)
	require.NotNil(t, gotImage)
	require.NotNil(t, gotBanner)
	assert.Equal(t, image, *gotImage)
	assert.Equal(t, banner, *gotBanner)
}

func TestUpdatePoolProfileWritesAndClearsImages(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	poolID, address := seedEditablePool(t, tx)

	image := "https://content.example/pool_400_a3f91c2e.webp"
	_, err := pg.UpdatePoolProfile(ctx, tx, address, PoolProfile{ImageURL: &image})
	require.NoError(t, err)

	gotImage, gotBanner := readPoolImages(t, tx, poolID)
	require.NotNil(t, gotImage)
	assert.Equal(t, image, *gotImage)
	assert.Nil(t, gotBanner, "a banner nobody set stays unset")

	// Clearing needs a value, not an absence: "" is the only way to say "remove
	// it", because an omitted field preserves.
	empty := ""
	_, err = pg.UpdatePoolProfile(ctx, tx, address, PoolProfile{ImageURL: &empty})
	require.NoError(t, err)

	cleared, _ := readPoolImages(t, tx, poolID)
	assert.Nil(t, cleared, "an empty string must clear the image")
}

// The database refuses what the API refuses, so a direct write cannot leave a
// data URI where every screen expects a URL.
func TestUpdatePoolProfileRejectsANonHttpImage(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	_, address := seedEditablePool(t, tx)

	dataURI := "data:image/jpeg;base64,/9j/4AAQSkZJRg"
	_, err := pg.UpdatePoolProfile(ctx, tx, address, PoolProfile{ImageURL: &dataURI})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pools_image_urls_http")
}

func readPoolImages(t *testing.T, tx pgx.Tx, poolID uint64) (image, banner *string) {
	t.Helper()
	require.NoError(t, tx.QueryRow(context.Background(), `
		SELECT image_url, banner_url FROM pools WHERE id = $1`, poolID).Scan(&image, &banner))
	return image, banner
}
