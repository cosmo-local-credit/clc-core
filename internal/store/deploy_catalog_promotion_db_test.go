package store

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests exercise the confirm-path promotion that moves a confirmed deploy
// into the network catalog (MIGRATION.md §2.4 / §3.5). They run against a live
// database and are skipped unless CORE_TEST_DSN points at one, so
// `go test ./...` stays hermetic on machines without Postgres.
//
//	CORE_TEST_DSN=postgres://postgres:postgres@127.0.0.1:5432/clc_core go test ./internal/store/
//
// Every fixture is created inside the harness transaction and rolled back, so
// these never leave catalog or pipeline rows behind.

// seedSignerKey inserts a keystore row and returns its ID, which otx.signer_account
// requires as a real reference.
func seedSignerKey(t *testing.T, tx pgx.Tx) uint64 {
	t.Helper()

	var id uint64
	err := tx.QueryRow(context.Background(), `
		INSERT INTO keystore (public_key, private_key, active)
		VALUES ('0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'), 'testkey', true)
		RETURNING id`).Scan(&id)
	require.NoError(t, err, "seed keystore")
	return id
}

// seedDeployOTX inserts an otx + dispatch pair of the given type and returns the
// OTX ID, mirroring what the deploy workers write before dispatch.
func seedDeployOTX(t *testing.T, tx pgx.Tx, otxType string) uint64 {
	t.Helper()
	ctx := context.Background()
	signerID := seedSignerKey(t, tx)

	var otxID uint64
	err := tx.QueryRow(ctx, `
		INSERT INTO otx (tracking_id, otx_type, signer_account, raw_tx, tx_hash, nonce)
		VALUES (gen_random_uuid(), $1, $2, '0xraw',
		        '0x' || md5(random()::text) || md5(random()::text), 0)
		RETURNING id`, otxType, signerID).Scan(&otxID)
	require.NoError(t, err, "seed otx")

	_, err = tx.Exec(ctx, `INSERT INTO dispatch (otx_id, status) VALUES ($1, 'PENDING')`, otxID)
	require.NoError(t, err, "seed dispatch")

	return otxID
}

// platformContractID resolves a real seeded factory so promotion derives chain_id
// from the pipeline rather than a hardcoded chain.
func platformContractID(t *testing.T, tx pgx.Tx, contractType string) uint64 {
	t.Helper()

	var id uint64
	err := tx.QueryRow(context.Background(),
		`SELECT id FROM platform_contracts WHERE contract_type = $1::contract_type LIMIT 1`,
		contractType).Scan(&id)
	require.NoError(t, err, "resolve platform contract %s", contractType)
	return id
}

// randomAddress returns a unique lower-case hex address for fixtures.
func randomAddress(t *testing.T, tx pgx.Tx) string {
	t.Helper()

	var addr string
	err := tx.QueryRow(context.Background(),
		`SELECT '0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0')`).Scan(&addr)
	require.NoError(t, err, "generate address")
	return addr
}

func TestUpsertVoucherFromDeployCreatesCatalogRowAndLink(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	otxID := seedDeployOTX(t, tx, TOKEN_PROXY_DEPLOY)
	admin := randomAddress(t, tx)
	uoa := "KES"
	value := 12.5
	locName := "Nairobi"
	lat, lng := -1.2921, 36.8219
	desc, site, mail, phone := "A test voucher", "https://example.org", "a@example.org", "+254700"
	terms := "https://example.org/terms"

	deployedID, err := pg.InsertDeployedToken(ctx, tx, DeployedToken{
		OTXID:              otxID,
		PlatformContractID: platformContractID(t, tx, "ERC20"),
		Symbol:             "PROMO",
		Name:               "Promotion Voucher",
		Decimals:           6,
		Admin:              admin,
		Description:        &desc,
		Website:            &site,
		Email:              &mail,
		Phone:              &phone,
		Terms:              &terms,
		VoucherUoa:         &uoa,
		VoucherValue:       &value,
		LocationName:       &locName,
		GeoLat:             &lat,
		GeoLng:             &lng,
	})
	require.NoError(t, err)

	for _, tag := range []string{"Market", "Youth"} {
		require.NoError(t, pg.InsertDeployedTokenTag(ctx, tx, deployedID, tag))
	}

	// The confirm path as the subscriber runs it: confirm, then promote, in one
	// transaction.
	tokenAddress := randomAddress(t, tx)
	confirmedID, err := pg.ConfirmDeployedToken(ctx, tx, otxID, tokenAddress)
	require.NoError(t, err)
	require.Equal(t, deployedID, confirmedID)

	voucherID, err := pg.UpsertVoucherFromDeploy(ctx, tx, deployedID)
	require.NoError(t, err)
	require.NotZero(t, voucherID, "promotion must return a catalog voucher ID")

	var (
		gotChainID             int64
		gotAddress, gotName    string
		gotSymbol, gotType     string
		gotDecimals            int32
		gotOwner               string
		gotUoa, gotVersion     *string
		gotTerms               *string
		gotValue               *float64
		gotLat, gotLng         *float64
		gotPlatformContractID  *uint64
		gotActive, gotInternal bool
	)
	err = tx.QueryRow(ctx, `
		SELECT chain_id, address, name, symbol, voucher_type, decimals, owner_address,
		       voucher_uoa, contract_version, voucher_value, geo_lat, geo_lng,
		       platform_contract_id, active, internal, terms
		FROM vouchers WHERE id = $1`, voucherID).Scan(
		&gotChainID, &gotAddress, &gotName, &gotSymbol, &gotType, &gotDecimals, &gotOwner,
		&gotUoa, &gotVersion, &gotValue, &gotLat, &gotLng,
		&gotPlatformContractID, &gotActive, &gotInternal, &gotTerms)
	require.NoError(t, err, "promoted voucher must exist")

	// chain_id and contract_version come from the factory that was actually
	// used, never from app config.
	assert.NotZero(t, gotChainID, "chain_id must be derived from the platform contract")
	assert.Equal(t, tokenAddress, gotAddress)
	assert.Equal(t, "Promotion Voucher", gotName)
	assert.Equal(t, "PROMO", gotSymbol)
	assert.Equal(t, "GIFTABLE", gotType, "clc-core deploys the giftable implementation")
	assert.Equal(t, int32(6), gotDecimals)
	assert.Equal(t, admin, gotOwner, "deploy admin becomes the catalog owner")
	require.NotNil(t, gotUoa)
	assert.Equal(t, "KES", *gotUoa)
	require.NotNil(t, gotValue)
	assert.Equal(t, 12.5, *gotValue)
	require.NotNil(t, gotLat)
	require.NotNil(t, gotLng)
	assert.Equal(t, lat, *gotLat)
	assert.Equal(t, lng, *gotLng)
	require.NotNil(t, gotVersion, "contract_version copied from the platform contract")
	assert.NotEmpty(t, *gotVersion)
	require.NotNil(t, gotPlatformContractID, "the real factory must be recorded")
	assert.True(t, gotActive, "new catalog rows default to active")
	assert.False(t, gotInternal)
	require.NotNil(t, gotTerms)
	assert.Equal(t, terms, *gotTerms)

	// The pipeline row links to the catalog row it produced.
	var linkedVoucherID *uint64
	require.NoError(t, tx.QueryRow(ctx,
		`SELECT voucher_id FROM deployed_tokens WHERE id = $1`, deployedID).Scan(&linkedVoucherID))
	require.NotNil(t, linkedVoucherID, "deployed_tokens.voucher_id must be set by promotion")
	assert.Equal(t, voucherID, *linkedVoucherID)

	// Tags move onto the catalog row.
	var tags []string
	rows, err := tx.Query(ctx,
		`SELECT tag_value FROM voucher_tags WHERE voucher_id = $1 ORDER BY tag_value`, voucherID)
	require.NoError(t, err)
	for rows.Next() {
		var tag string
		require.NoError(t, rows.Scan(&tag))
		tags = append(tags, tag)
	}
	rows.Close()
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"Market", "Youth"}, tags)
}

func TestUpsertVoucherFromDeployIsIdempotent(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	otxID := seedDeployOTX(t, tx, TOKEN_PROXY_DEPLOY)
	deployedID, err := pg.InsertDeployedToken(ctx, tx, DeployedToken{
		OTXID:              otxID,
		PlatformContractID: platformContractID(t, tx, "ERC20"),
		Symbol:             "IDEM",
		Name:               "Idempotent Voucher",
		Decimals:           6,
		Admin:              randomAddress(t, tx),
	})
	require.NoError(t, err)
	require.NoError(t, pg.InsertDeployedTokenTag(ctx, tx, deployedID, "Market"))

	_, err = pg.ConfirmDeployedToken(ctx, tx, otxID, randomAddress(t, tx))
	require.NoError(t, err)

	first, err := pg.UpsertVoucherFromDeploy(ctx, tx, deployedID)
	require.NoError(t, err)
	second, err := pg.UpsertVoucherFromDeploy(ctx, tx, deployedID)
	require.NoError(t, err)
	third, err := pg.UpsertVoucherFromDeploy(ctx, tx, deployedID)
	require.NoError(t, err)

	assert.Equal(t, first, second, "re-promoting must resolve to the same catalog row")
	assert.Equal(t, first, third)

	var tagCount int
	require.NoError(t, tx.QueryRow(ctx,
		`SELECT count(*) FROM voucher_tags WHERE voucher_id = $1`, first).Scan(&tagCount))
	assert.Equal(t, 1, tagCount, "tag copy must not duplicate on repeat promotion")
}

// A catalog row imported with different address casing must be matched and
// updated, not duplicated — which is why the conflict target is the
// lower(address) index rather than UNIQUE (chain_id, address).
func TestUpsertVoucherFromDeployMatchesExistingRowIgnoringAddressCase(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	platformID := platformContractID(t, tx, "ERC20")
	var chainID int64
	require.NoError(t, tx.QueryRow(ctx,
		`SELECT chain_id FROM platform_contracts WHERE id = $1`, platformID).Scan(&chainID))

	// An existing catalog row: lower-case address, plus curated fields the
	// deploy snapshot knows nothing about.
	lowerAddress := randomAddress(t, tx)
	var existingID uint64
	err := tx.QueryRow(ctx, `
		INSERT INTO vouchers (chain_id, address, name, symbol, decimals,
		                      icon_url, terms, sink_address, active, internal)
		VALUES ($1, $2, 'Imported Name', 'IMP', 6,
		        'https://cdn/icon.png', 'Curated terms', $3, false, true)
		RETURNING id`, chainID, lowerAddress, randomAddress(t, tx)).Scan(&existingID)
	require.NoError(t, err)

	// The same contract confirmed through the deploy pipeline, upper-case hex.
	otxID := seedDeployOTX(t, tx, TOKEN_PROXY_DEPLOY)
	admin := randomAddress(t, tx)
	desc := "Deploy description"
	deployedID, err := pg.InsertDeployedToken(ctx, tx, DeployedToken{
		OTXID:              otxID,
		PlatformContractID: platformID,
		Symbol:             "DEPL",
		Name:               "Deploy Name",
		Decimals:           6,
		Admin:              admin,
		Description:        &desc,
	})
	require.NoError(t, err)

	upperAddress := "0x" + strings.ToUpper(strings.TrimPrefix(lowerAddress, "0x"))
	_, err = pg.ConfirmDeployedToken(ctx, tx, otxID, upperAddress)
	require.NoError(t, err)

	voucherID, err := pg.UpsertVoucherFromDeploy(ctx, tx, deployedID)
	require.NoError(t, err)
	assert.Equal(t, existingID, voucherID, "must update the existing row, not insert a new one")

	var count int
	require.NoError(t, tx.QueryRow(ctx,
		`SELECT count(*) FROM vouchers WHERE chain_id = $1 AND lower(address) = lower($2)`,
		chainID, lowerAddress).Scan(&count))
	assert.Equal(t, 1, count, "no duplicate catalog row for the same contract")

	// Merge policy: the fresh deploy wins where it has a value; fields the
	// deploy has no opinion about survive untouched.
	var (
		gotName, gotSymbol string
		gotDesc            *string
		gotIcon, gotTerms  *string
		gotSink            *string
		gotActive          bool
		gotInternal        bool
		gotOwner           *string
	)
	require.NoError(t, tx.QueryRow(ctx, `
		SELECT name, symbol, description, icon_url, terms, sink_address, active, internal, owner_address
		FROM vouchers WHERE id = $1`, voucherID).Scan(
		&gotName, &gotSymbol, &gotDesc, &gotIcon, &gotTerms, &gotSink,
		&gotActive, &gotInternal, &gotOwner))

	assert.Equal(t, "Deploy Name", gotName, "deploy wins on name")
	assert.Equal(t, "DEPL", gotSymbol, "deploy wins on symbol")
	require.NotNil(t, gotDesc)
	assert.Equal(t, "Deploy description", *gotDesc)
	require.NotNil(t, gotOwner)
	assert.Equal(t, admin, *gotOwner)
	require.NotNil(t, gotIcon, "curated icon must survive promotion")
	assert.Equal(t, "https://cdn/icon.png", *gotIcon)
	require.NotNil(t, gotTerms, "curated terms must survive promotion")
	assert.Equal(t, "Curated terms", *gotTerms)
	require.NotNil(t, gotSink, "sink the deploy knows nothing about must survive")
	assert.False(t, gotActive, "promotion must not resurrect a deactivated voucher")
	assert.True(t, gotInternal, "promotion must not clear the internal flag")
}

func TestUpsertPoolFromDeployCreatesCatalogRowAndLink(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	otxID := seedDeployOTX(t, tx, POOL_PROXY_DEPLOY)
	admin := randomAddress(t, tx)
	registry := randomAddress(t, tx)
	limiter := randomAddress(t, tx)
	feePolicy := randomAddress(t, tx)
	quoter := randomAddress(t, tx)
	feeController := randomAddress(t, tx)
	desc, site, mail, phone := "A test pool", "https://pool.example.org", "p@example.org", "+254701"
	terms := "https://pool.example.org/terms"
	unitOfAccount := "BAM"
	locName := "Kisumu"
	lat, lng := -0.0917, 34.7680
	image, banner := "https://img", "https://banner"
	quoterType := "oracle"

	deployedID, err := pg.InsertDeployedPool(ctx, tx, DeployedPool{
		OTXID:                        otxID,
		PlatformContractID:           platformContractID(t, tx, "SWAPPOOL"),
		Name:                         "Promotion Pool",
		Symbol:                       "PRMP",
		Decimals:                     6,
		TokenRegistryAddress:         registry,
		LimiterAddress:               limiter,
		FeePolicyAddress:             feePolicy,
		QuoterAddress:                quoter,
		QuoterType:                   &quoterType,
		ProtocolFeeControllerAddress: feeController,
		FeesDecoupled:                true,
		Admin:                        admin,
		Description:                  &desc,
		Website:                      &site,
		Email:                        &mail,
		Phone:                        &phone,
		Terms:                        &terms,
		UnitOfAccount:                &unitOfAccount,
		LocationName:                 &locName,
		GeoLat:                       &lat,
		GeoLng:                       &lng,
		ImageURL:                     &image,
		BannerURL:                    &banner,
	})
	require.NoError(t, err)
	require.NoError(t, pg.InsertDeployedPoolTag(ctx, tx, deployedID, "Lending"))

	poolAddress := randomAddress(t, tx)
	confirmedID, err := pg.ConfirmDeployedPool(ctx, tx, otxID, poolAddress)
	require.NoError(t, err)
	require.Equal(t, deployedID, confirmedID)

	poolID, err := pg.UpsertPoolFromDeploy(ctx, tx, deployedID)
	require.NoError(t, err)
	require.NotZero(t, poolID)

	var (
		gotChainID                     int64
		gotAddress, gotName, gotSymbol string
		gotOwner                       *string
		gotRegistry, gotLimiter        *string
		gotQuoter, gotFeePolicy        *string
		gotFeeController               *string
		gotDecoupled                   *bool
		gotImage, gotBanner            *string
		gotPlatformID                  *uint64
		gotFeePPM, gotUoa, gotTerms    *string
		gotDefaultVoucherID            *uint64
		gotQuoterType                  *string
	)
	err = tx.QueryRow(ctx, `
		SELECT chain_id, address, name, symbol, owner_address,
		       token_registry_address, limiter_address, quoter_address, fee_policy_address,
		       protocol_fee_controller_address, fees_decoupled, image_url, banner_url,
		       platform_contract_id, fee_ppm::TEXT, unit_of_account, terms, default_voucher_id,
		       quoter_type
		FROM pools WHERE id = $1`, poolID).Scan(
		&gotChainID, &gotAddress, &gotName, &gotSymbol, &gotOwner,
		&gotRegistry, &gotLimiter, &gotQuoter, &gotFeePolicy,
		&gotFeeController, &gotDecoupled, &gotImage, &gotBanner,
		&gotPlatformID, &gotFeePPM, &gotUoa, &gotTerms, &gotDefaultVoucherID,
		&gotQuoterType)
	require.NoError(t, err, "promoted pool must exist")

	assert.NotZero(t, gotChainID)
	assert.Equal(t, poolAddress, gotAddress)
	assert.Equal(t, "Promotion Pool", gotName)
	assert.Equal(t, "PRMP", gotSymbol)
	require.NotNil(t, gotOwner)
	assert.Equal(t, admin, *gotOwner, "deploy admin becomes the catalog owner")

	// Protocol wiring must survive the copy: these are what make the catalog row
	// usable without reading back through the pipeline.
	require.NotNil(t, gotRegistry)
	assert.Equal(t, registry, *gotRegistry)
	require.NotNil(t, gotLimiter)
	assert.Equal(t, limiter, *gotLimiter)
	require.NotNil(t, gotQuoter)
	assert.Equal(t, quoter, *gotQuoter, "quoter must not be swapped with fee policy")
	// The address alone cannot say which quoter this is — all three implementations
	// answer the same ERC-165 ids — so the kind has to ride along with it.
	require.NotNil(t, gotQuoterType)
	assert.Equal(t, "oracle", *gotQuoterType)
	require.NotNil(t, gotFeePolicy)
	assert.Equal(t, feePolicy, *gotFeePolicy)
	require.NotNil(t, gotFeeController)
	assert.Equal(t, feeController, *gotFeeController)
	require.NotNil(t, gotDecoupled)
	assert.True(t, *gotDecoupled)
	require.NotNil(t, gotImage)
	assert.Equal(t, image, *gotImage)
	require.NotNil(t, gotBanner)
	assert.Equal(t, banner, *gotBanner)
	require.NotNil(t, gotPlatformID)

	// Columns with no counterpart on the deploy row are left for the importer
	// and pool sync to own. The requested unit of account is deploy metadata and
	// must survive promotion.
	assert.Nil(t, gotFeePPM, "fee_ppm has no deploy-side source")
	require.NotNil(t, gotUoa)
	assert.Equal(t, "BAM", *gotUoa)
	require.NotNil(t, gotTerms)
	assert.Equal(t, terms, *gotTerms)
	assert.Nil(t, gotDefaultVoucherID, "default_voucher_id has no deploy-side source")

	var linkedPoolID *uint64
	require.NoError(t, tx.QueryRow(ctx,
		`SELECT pool_id FROM deployed_pools WHERE id = $1`, deployedID).Scan(&linkedPoolID))
	require.NotNil(t, linkedPoolID, "deployed_pools.pool_id must be set by promotion")
	assert.Equal(t, poolID, *linkedPoolID)

	var tagCount int
	require.NoError(t, tx.QueryRow(ctx,
		`SELECT count(*) FROM pool_tags WHERE pool_id = $1 AND tag_value = 'Lending'`,
		poolID).Scan(&tagCount))
	assert.Equal(t, 1, tagCount, "deploy tags must move to pool_tags")
}

func TestUpsertPoolFromDeployIsIdempotent(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	otxID := seedDeployOTX(t, tx, POOL_PROXY_DEPLOY)
	deployedID, err := pg.InsertDeployedPool(ctx, tx, DeployedPool{
		OTXID:                        otxID,
		PlatformContractID:           platformContractID(t, tx, "SWAPPOOL"),
		Name:                         "Idempotent Pool",
		Symbol:                       "IDMP",
		Decimals:                     6,
		TokenRegistryAddress:         randomAddress(t, tx),
		LimiterAddress:               randomAddress(t, tx),
		FeePolicyAddress:             randomAddress(t, tx),
		QuoterAddress:                randomAddress(t, tx),
		ProtocolFeeControllerAddress: randomAddress(t, tx),
		Admin:                        randomAddress(t, tx),
	})
	require.NoError(t, err)
	require.NoError(t, pg.InsertDeployedPoolTag(ctx, tx, deployedID, "Lending"))

	_, err = pg.ConfirmDeployedPool(ctx, tx, otxID, randomAddress(t, tx))
	require.NoError(t, err)

	first, err := pg.UpsertPoolFromDeploy(ctx, tx, deployedID)
	require.NoError(t, err)
	second, err := pg.UpsertPoolFromDeploy(ctx, tx, deployedID)
	require.NoError(t, err)

	assert.Equal(t, first, second)

	var tagCount int
	require.NoError(t, tx.QueryRow(ctx,
		`SELECT count(*) FROM pool_tags WHERE pool_id = $1`, first).Scan(&tagCount))
	assert.Equal(t, 1, tagCount)
}

// A deploy row with no address cannot be described in the catalog. Promotion
// reports ErrNoRows rather than writing a partial row, which is the signal the
// subscriber would surface as a failed event.
func TestUpsertFromDeployReturnsNoRowsWhenAddressMissing(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	tokenOTX := seedDeployOTX(t, tx, TOKEN_PROXY_DEPLOY)
	pendingToken, err := pg.InsertDeployedToken(ctx, tx, DeployedToken{
		OTXID:              tokenOTX,
		PlatformContractID: platformContractID(t, tx, "ERC20"),
		Symbol:             "NOADDR",
		Name:               "Unconfirmed Voucher",
		Decimals:           6,
		Admin:              randomAddress(t, tx),
	})
	require.NoError(t, err)

	// Tags matter here: the tag-copy branch would otherwise try to insert a NULL
	// voucher_id and fail on the NOT NULL rather than reporting ErrNoRows.
	require.NoError(t, pg.InsertDeployedTokenTag(ctx, tx, pendingToken, "Market"))

	_, err = pg.UpsertVoucherFromDeploy(ctx, tx, pendingToken)
	assert.ErrorIs(t, err, pgx.ErrNoRows, "promotion of an unaddressed token deploy must not write")

	// The failed promotion must leave no trace behind.
	var linked *uint64
	require.NoError(t, tx.QueryRow(ctx,
		`SELECT voucher_id FROM deployed_tokens WHERE id = $1`, pendingToken).Scan(&linked))
	assert.Nil(t, linked, "voucher_id must stay NULL when promotion finds no address")

	// Same contract on the pool side.
	poolOTX := seedDeployOTX(t, tx, POOL_PROXY_DEPLOY)
	pendingPool, err := pg.InsertDeployedPool(ctx, tx, DeployedPool{
		OTXID:                        poolOTX,
		PlatformContractID:           platformContractID(t, tx, "SWAPPOOL"),
		Name:                         "Unconfirmed Pool",
		Symbol:                       "NOAP",
		Decimals:                     6,
		TokenRegistryAddress:         randomAddress(t, tx),
		LimiterAddress:               randomAddress(t, tx),
		FeePolicyAddress:             randomAddress(t, tx),
		QuoterAddress:                randomAddress(t, tx),
		ProtocolFeeControllerAddress: randomAddress(t, tx),
		Admin:                        randomAddress(t, tx),
	})
	require.NoError(t, err)
	require.NoError(t, pg.InsertDeployedPoolTag(ctx, tx, pendingPool, "Lending"))

	_, err = pg.UpsertPoolFromDeploy(ctx, tx, pendingPool)
	assert.ErrorIs(t, err, pgx.ErrNoRows, "promotion of an unaddressed pool deploy must not write")

	var linkedPool *uint64
	require.NoError(t, tx.QueryRow(ctx,
		`SELECT pool_id FROM deployed_pools WHERE id = $1`, pendingPool).Scan(&linkedPool))
	assert.Nil(t, linkedPool, "pool_id must stay NULL when promotion finds no address")
}

// The whole point of the change: promotion is what makes a confirmed deploy
// visible to the catalog read paths the public API serves.
func TestPromotedVoucherIsVisibleToCatalogReads(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	otxID := seedDeployOTX(t, tx, TOKEN_PROXY_DEPLOY)
	deployedID, err := pg.InsertDeployedToken(ctx, tx, DeployedToken{
		OTXID:              otxID,
		PlatformContractID: platformContractID(t, tx, "ERC20"),
		Symbol:             "VISIB",
		Name:               "Visible Voucher",
		Decimals:           6,
		Admin:              randomAddress(t, tx),
	})
	require.NoError(t, err)

	tokenAddress := randomAddress(t, tx)
	_, err = pg.ConfirmDeployedToken(ctx, tx, otxID, tokenAddress)
	require.NoError(t, err)

	// Before promotion the catalog cannot describe the token at all.
	before, err := pg.ListVoucherMetadataByAddresses(ctx, tx, []string{tokenAddress})
	require.NoError(t, err)
	assert.Empty(t, before, "a confirmed deploy is invisible to the catalog until promoted")

	_, err = pg.UpsertVoucherFromDeploy(ctx, tx, deployedID)
	require.NoError(t, err)

	after, err := pg.ListVoucherMetadataByAddresses(ctx, tx, []string{tokenAddress})
	require.NoError(t, err)
	require.Len(t, after, 1, "promotion must make the token resolvable by the catalog")
	require.NotNil(t, after[0].Symbol)
	assert.Equal(t, "VISIB", *after[0].Symbol)
	require.NotNil(t, after[0].Decimals)
	assert.Equal(t, int32(6), *after[0].Decimals)
	assert.Equal(t, strings.ToLower(tokenAddress), strings.ToLower(after[0].Address))
}

// The icon and banner a creator chose are recorded on the deploy row and must
// reach the catalog when the address exists. Before this, both were dropped at
// confirmation while the catalog columns that should receive them sat empty.
func TestUpsertVoucherFromDeployCarriesTheDeployImages(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	otxID := seedDeployOTX(t, tx, TOKEN_PROXY_DEPLOY)
	icon := "https://content.example/voucher/abc_400_a3f91c2e.webp"
	banner := "https://content.example/voucher/def_1280_b1c2d3e4.webp"

	deployedID, err := pg.InsertDeployedToken(ctx, tx, DeployedToken{
		OTXID:              otxID,
		PlatformContractID: platformContractID(t, tx, "ERC20"),
		Symbol:             "IMG",
		Name:               "Illustrated Voucher",
		Decimals:           6,
		Admin:              randomAddress(t, tx),
		IconURL:            &icon,
		BannerURL:          &banner,
	})
	require.NoError(t, err)

	_, err = pg.ConfirmDeployedToken(ctx, tx, otxID, randomAddress(t, tx))
	require.NoError(t, err)

	voucherID, err := pg.UpsertVoucherFromDeploy(ctx, tx, deployedID)
	require.NoError(t, err)
	require.NotZero(t, voucherID)

	var gotIcon, gotBanner *string
	require.NoError(t, tx.QueryRow(ctx,
		`SELECT icon_url, banner_url FROM vouchers WHERE id = $1`, voucherID).Scan(&gotIcon, &gotBanner))
	require.NotNil(t, gotIcon)
	require.NotNil(t, gotBanner)
	assert.Equal(t, icon, *gotIcon)
	assert.Equal(t, banner, *gotBanner)
}

// A deploy with no images must not blank the images an imported catalog row
// already had: the merge is COALESCE, matching every other field.
func TestUpsertVoucherFromDeployKeepsExistingImagesWhenTheDeployHasNone(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	address := randomAddress(t, tx)
	existing := "https://content.example/voucher/imported_400_aaaaaaaa.webp"
	_, err := tx.Exec(ctx, `
		INSERT INTO vouchers (chain_id, address, name, symbol, decimals, icon_url)
		VALUES (42220, $1, 'Imported', 'IMP', 6, $2)`, address, existing)
	require.NoError(t, err)

	otxID := seedDeployOTX(t, tx, TOKEN_PROXY_DEPLOY)
	deployedID, err := pg.InsertDeployedToken(ctx, tx, DeployedToken{
		OTXID:              otxID,
		PlatformContractID: platformContractID(t, tx, "ERC20"),
		Symbol:             "IMP",
		Name:               "Imported",
		Decimals:           6,
		Admin:              randomAddress(t, tx),
	})
	require.NoError(t, err)

	_, err = pg.ConfirmDeployedToken(ctx, tx, otxID, address)
	require.NoError(t, err)

	voucherID, err := pg.UpsertVoucherFromDeploy(ctx, tx, deployedID)
	require.NoError(t, err)

	var gotIcon *string
	require.NoError(t, tx.QueryRow(ctx,
		`SELECT icon_url FROM vouchers WHERE id = $1`, voucherID).Scan(&gotIcon))
	require.NotNil(t, gotIcon, "an image-less deploy must not clear an imported icon")
	assert.Equal(t, existing, *gotIcon)
}
