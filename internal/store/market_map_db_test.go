package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMarketMapMixedMarkersClustersFacetsAndCreditDB(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()

	const term = "Zqvxmapscale"
	var ownerID uint64
	var ownerAddress string
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO users(blockchain_address,name,email)
		VALUES('0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),$1,gen_random_uuid()::text||'@map.test')
		RETURNING id,blockchain_address`, term+" owner").Scan(&ownerID, &ownerAddress))

	voucherID := seedSearchVoucher(t, tx, term+" voucher", "ZMAP")
	_, err := tx.Exec(ctx, `UPDATE vouchers SET owner_address=$1,geo_lat=80,geo_lng=170 WHERE id=$2`, ownerAddress, voucherID)
	require.NoError(t, err)

	var poolID uint64
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO pools(chain_id,address,name,symbol,geo_lat,geo_lng)
		VALUES(100,'0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),$1,'ZMP',80,170)
		RETURNING id`, term+" pool").Scan(&poolID))
	_, err = tx.Exec(ctx, `INSERT INTO pool_tokens(pool_id,voucher_id,is_allowed) VALUES($1,$2,true)`, poolID, voucherID)
	require.NoError(t, err)

	var wantID uint64
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO wants(owner_user_id,name,description,location_name,geo_lat,geo_lng,status,contact_text)
		VALUES($1,$2,'Map fixture','Map fixture',80,170,'OPEN','Public contact')
		RETURNING id`, ownerID, term+" want").Scan(&wantID))

	var catalogID uint64
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO catalogs(owner_user_id,name,description,location_name,geo_lat,geo_lng)
		VALUES($1,$2,'Map fixture','Map fixture',80,170)
		RETURNING id`, ownerID, term+" catalog").Scan(&catalogID))
	_, err = tx.Exec(ctx, `INSERT INTO catalog_vouchers(catalog_id,voucher_id) VALUES($1,$2)`, catalogID, voucherID)
	require.NoError(t, err)

	var reportID uint64
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO reports(title,description,author_address,status,geo_lat,geo_lng)
		VALUES($1,'Map fixture',$2,'APPROVED',80,170)
		RETURNING id`, term+" post", ownerAddress).Scan(&reportID))
	_, err = tx.Exec(ctx, `INSERT INTO report_vouchers(report_id,voucher_id) VALUES($1,$2)`, reportID, voucherID)
	require.NoError(t, err)

	var tag string
	require.NoError(t, tx.QueryRow(ctx, `SELECT value FROM token_tag ORDER BY value LIMIT 1`).Scan(&tag))
	for _, statement := range []struct {
		query string
		id    uint64
	}{
		{`INSERT INTO voucher_tags(voucher_id,tag_value) VALUES($1,$2)`, voucherID},
		{`INSERT INTO pool_tags(pool_id,tag_value) VALUES($1,$2)`, poolID},
		{`INSERT INTO want_tags(want_id,tag_value) VALUES($1,$2)`, wantID},
		{`INSERT INTO catalog_tags(catalog_id,tag_value) VALUES($1,$2)`, catalogID},
		{`INSERT INTO report_tags(report_id,tag_value) VALUES($1,$2)`, reportID},
	} {
		_, insertErr := tx.Exec(ctx, statement.query, statement.id, tag)
		require.NoError(t, insertErr)
	}

	filter, err := NewCatalogFilter(term, []string{tag}, TagMatchAny)
	require.NoError(t, err)
	filter.Bounds = []CatalogBounds{{MinLng: 169, MinLat: 79, MaxLng: 171, MaxLat: 81}}
	filter, err = filter.WithPoolIDsMatch([]uint64{poolID}, TagMatchAny)
	require.NoError(t, err)

	markers, err := pg.ListMarketMap(ctx, tx, filter, []string{"catalog", "pool", "post", "voucher", "want"}, 16, true)
	require.NoError(t, err)
	require.Len(t, markers.Features, 5)
	kinds := map[string]bool{}
	for _, feature := range markers.Features {
		require.Equal(t, "marker", feature.Type)
		require.NotNil(t, feature.Kind)
		kinds[*feature.Kind] = true
	}
	for _, kind := range []string{"catalog", "pool", "post", "voucher", "want"} {
		require.True(t, kinds[kind], "missing %s marker", kind)
	}
	require.Contains(t, markers.TagFacets, TagFacet{Tag: tag, Count: 5})

	clustered, err := pg.ListMarketMap(ctx, tx, filter, nil, 1, false)
	require.NoError(t, err)
	require.Len(t, clustered.Features, 1)
	cluster := clustered.Features[0]
	require.Equal(t, "cluster", cluster.Type)
	require.Equal(t, uint64(5), cluster.Count)
	for _, kind := range []string{"catalog", "pool", "post", "voucher", "want"} {
		require.Equal(t, 1, cluster.Kinds[kind], "cluster %s count", kind)
	}
	require.Greater(t, cluster.ExpansionZoom, float64(1))
}

func TestMarketMapAntimeridianBoundsExecuteDB(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()

	filter := CatalogFilter{Bounds: []CatalogBounds{
		{MinLng: -180, MinLat: -10, MaxLng: -170, MaxLat: 10},
		{MinLng: 170, MinLat: -10, MaxLng: 180, MaxLat: 10},
	}}
	_, err := pg.ListMarketMap(context.Background(), tx, filter, []string{"voucher"}, 4, false)
	require.NoError(t, err)
}
