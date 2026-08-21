package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedEditableVoucher(t *testing.T, tx pgx.Tx) (voucherID uint64, address string) {
	t.Helper()

	address = randomAddress(t, tx)
	require.NoError(t, tx.QueryRow(context.Background(), `
		INSERT INTO vouchers (chain_id, address, name, symbol, decimals, owner_address,
			description, voucher_uoa, voucher_value, email, phone, location_name, geo_lat, geo_lng)
		VALUES (42220, $1, 'Editable', 'EDT', 6, $2,
			'Before', 'KES', 5, 'before@example.org', '+254700', 'Nakuru', 1.0, 2.0)
		RETURNING id`, address, randomAddress(t, tx)).Scan(&voucherID))
	return voucherID, address
}

func readVoucherProfile(t *testing.T, tx pgx.Tx, voucherID uint64) (email, phone, location *string, lat *float64) {
	t.Helper()
	require.NoError(t, tx.QueryRow(context.Background(), `
		SELECT email, phone, location_name, geo_lat FROM vouchers WHERE id = $1`,
		voucherID).Scan(&email, &phone, &location, &lat))
	return email, phone, location, lat
}

func TestUpdateVoucherProfileAssignsEveryColumn(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	voucherID, address := seedEditableVoucher(t, tx)

	email, site := "after@example.org", "https://after.example"
	name, lat, lng := "Kilifi", -3.5, 39.9

	got, err := pg.UpdateVoucherProfile(ctx, tx, address, VoucherProfile{
		Email:        &email,
		Website:      &site,
		LocationName: &name,
		GeoLat:       &lat,
		GeoLng:       &lng,
	})
	require.NoError(t, err)
	assert.Equal(t, voucherID, got)

	gotEmail, gotPhone, gotLocation, gotLat := readVoucherProfile(t, tx, voucherID)
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

// voucher_value is NUMERIC, so a float64 has to survive the round trip as the
// value the caller sent rather than as whatever the driver inferred.
func TestUpdateVoucherProfileWritesTheUnitAndValue(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	voucherID, address := seedEditableVoucher(t, tx)

	uoa, value := "HOUR", 2.5
	_, err := pg.UpdateVoucherProfile(ctx, tx, address, VoucherProfile{
		VoucherUOA:   &uoa,
		VoucherValue: &value,
	})
	require.NoError(t, err)

	var gotUOA *string
	var gotValue *string
	require.NoError(t, tx.QueryRow(ctx,
		`SELECT voucher_uoa, voucher_value::TEXT FROM vouchers WHERE id = $1`,
		voucherID).Scan(&gotUOA, &gotValue))
	require.NotNil(t, gotUOA)
	require.NotNil(t, gotValue)
	assert.Equal(t, "HOUR", *gotUOA)
	assert.Equal(t, "2.5", *gotValue)
}

// The address is the identity and arrives in whatever casing the caller used;
// the catalog stores EIP-55.
func TestUpdateVoucherProfileMatchesAddressCaseInsensitively(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()

	voucherID, address := seedEditableVoucher(t, tx)

	got, err := pg.UpdateVoucherProfile(context.Background(), tx, lowerHex(address), VoucherProfile{})
	require.NoError(t, err)
	assert.Equal(t, voucherID, got)
}

func TestUpdateVoucherProfileReportsAnUnknownVoucher(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()

	_, err := pg.UpdateVoucherProfile(
		context.Background(), tx, randomAddress(t, tx), VoucherProfile{})
	assert.ErrorIs(t, err, pgx.ErrNoRows, "a missing voucher must be a 404, not a silent no-op")
}

// Chain-mirroring columns are not in the statement at all, so no edit can put
// the catalog out of step with the deployed ERC20.
func TestUpdateVoucherProfileLeavesChainColumnsAlone(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	voucherID, address := seedEditableVoucher(t, tx)

	var ownerBefore string
	require.NoError(t, tx.QueryRow(ctx,
		`SELECT owner_address FROM vouchers WHERE id = $1`, voucherID).Scan(&ownerBefore))

	_, err := pg.UpdateVoucherProfile(ctx, tx, address, VoucherProfile{})
	require.NoError(t, err)

	var name, symbol, owner string
	var decimals int
	var active bool
	require.NoError(t, tx.QueryRow(ctx,
		`SELECT name, symbol, decimals, owner_address, active FROM vouchers WHERE id = $1`,
		voucherID).Scan(&name, &symbol, &decimals, &owner, &active))
	assert.Equal(t, "Editable", name)
	assert.Equal(t, "EDT", symbol)
	assert.Equal(t, 6, decimals)
	assert.Equal(t, ownerBefore, owner)
	// Retiring a voucher is a different operation from correcting its prose.
	assert.True(t, active, "a profile edit must not change visibility")
}

func TestReplaceVoucherTagsSwapsTheWholeSet(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	voucherID, _ := seedEditableVoucher(t, tx)

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
	first, second := values[0], values[1]

	require.NoError(t, pg.ReplaceVoucherTags(ctx, tx, voucherID, []string{first, second}))
	assert.ElementsMatch(t, []string{first, second}, voucherTags(t, tx, voucherID))

	// A replace is not a merge: the tag dropped from the set has to go.
	require.NoError(t, pg.ReplaceVoucherTags(ctx, tx, voucherID, []string{second}))
	assert.Equal(t, []string{second}, voucherTags(t, tx, voucherID))

	require.NoError(t, pg.ReplaceVoucherTags(ctx, tx, voucherID, nil))
	assert.Empty(t, voucherTags(t, tx, voucherID))
}

func voucherTags(t *testing.T, tx pgx.Tx, voucherID uint64) []string {
	t.Helper()
	rows, err := tx.Query(context.Background(),
		`SELECT tag_value FROM voucher_tags WHERE voucher_id = $1 ORDER BY tag_value`, voucherID)
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

// Images are the one exception to this statement's assign-all rule, so they get
// their own test: a client that says nothing about images must not blank the
// ones a deploy carried in.
func TestUpdateVoucherProfilePreservesImagesWhenOmitted(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	voucherID, address := seedEditableVoucher(t, tx)
	icon := "https://content.example/voucher_400_a3f91c2e.webp"
	banner := "https://content.example/voucher_1280_b1c2d3e4.webp"
	_, err := tx.Exec(ctx, `UPDATE vouchers SET icon_url = $2, banner_url = $3 WHERE id = $1`,
		voucherID, icon, banner)
	require.NoError(t, err)

	desc := "Edited by a client that sends no images"
	_, err = pg.UpdateVoucherProfile(ctx, tx, address, VoucherProfile{Description: &desc})
	require.NoError(t, err)

	gotIcon, gotBanner := readVoucherImages(t, tx, voucherID)
	require.NotNil(t, gotIcon)
	require.NotNil(t, gotBanner)
	assert.Equal(t, icon, *gotIcon)
	assert.Equal(t, banner, *gotBanner)
}

func TestUpdateVoucherProfileWritesAndClearsImages(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	voucherID, address := seedEditableVoucher(t, tx)

	icon := "https://content.example/voucher_400_a3f91c2e.webp"
	_, err := pg.UpdateVoucherProfile(ctx, tx, address, VoucherProfile{IconURL: &icon})
	require.NoError(t, err)

	gotIcon, gotBanner := readVoucherImages(t, tx, voucherID)
	require.NotNil(t, gotIcon)
	assert.Equal(t, icon, *gotIcon)
	assert.Nil(t, gotBanner, "a banner nobody set stays unset")

	// Clearing needs a value, not an absence: "" is the only way to say "remove
	// it", because an omitted field preserves.
	empty := ""
	_, err = pg.UpdateVoucherProfile(ctx, tx, address, VoucherProfile{IconURL: &empty})
	require.NoError(t, err)

	cleared, _ := readVoucherImages(t, tx, voucherID)
	assert.Nil(t, cleared, "an empty string must clear the image")
}

// The database refuses what the API refuses, so a direct write cannot leave a
// data URI where every screen expects a URL.
func TestUpdateVoucherProfileRejectsANonHttpImage(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	_, address := seedEditableVoucher(t, tx)

	dataURI := "data:image/jpeg;base64,/9j/4AAQSkZJRg"
	_, err := pg.UpdateVoucherProfile(ctx, tx, address, VoucherProfile{IconURL: &dataURI})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vouchers_image_urls_http")
}

// vouchers_geo_valid is both-or-neither, so half a pin is rejected by the
// database as well as by the handler.
func TestUpdateVoucherProfileRejectsHalfAPin(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	_, address := seedEditableVoucher(t, tx)

	lat := -3.5
	_, err := pg.UpdateVoucherProfile(ctx, tx, address, VoucherProfile{GeoLat: &lat})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vouchers_geo_valid")
}

func readVoucherImages(t *testing.T, tx pgx.Tx, voucherID uint64) (icon, banner *string) {
	t.Helper()
	require.NoError(t, tx.QueryRow(context.Background(), `
		SELECT icon_url, banner_url FROM vouchers WHERE id = $1`, voucherID).Scan(&icon, &banner))
	return icon, banner
}
