package store

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
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
	_, err := tx.Exec(ctx, `UPDATE vouchers SET owner_address=$1,geo_lat=-3.68,geo_lng=39.83 WHERE id=$2`, ownerAddress, voucherID)
	require.NoError(t, err)

	var poolID uint64
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO pools(chain_id,address,name,symbol,geo_lat,geo_lng)
		VALUES(100,'0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),$1,'ZMP',-3.68,39.83)
		RETURNING id`, term+" pool").Scan(&poolID))
	_, err = tx.Exec(ctx, `INSERT INTO pool_tokens(pool_id,voucher_id,is_allowed) VALUES($1,$2,true)`, poolID, voucherID)
	require.NoError(t, err)

	var wantID uint64
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO wants(owner_user_id,name,description,location_name,geo_lat,geo_lng,status,contact_text)
		VALUES($1,$2,'Map fixture','Map fixture',-3.68,39.83,'OPEN','Public contact')
		RETURNING id`, ownerID, term+" want").Scan(&wantID))

	var catalogID uint64
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO catalogs(owner_user_id,name,description,location_name,geo_lat,geo_lng)
		VALUES($1,$2,'Map fixture','Map fixture',-3.68,39.83)
		RETURNING id`, ownerID, term+" catalog").Scan(&catalogID))
	_, err = tx.Exec(ctx, `INSERT INTO catalog_vouchers(catalog_id,voucher_id) VALUES($1,$2)`, catalogID, voucherID)
	require.NoError(t, err)

	var reportID uint64
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO reports(title,description,author_address,status,geo_lat,geo_lng)
		VALUES($1,'Map fixture',$2,'APPROVED',-3.68,39.83)
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
	// Both latitude edges used to be truncated to -3 by inferred integer
	// parameters, collapsing an ordinary neighborhood viewport into a line.
	filter.Bounds = []CatalogBounds{{MinLng: 39.7, MinLat: -3.8, MaxLng: 40.2, MaxLat: -3.2}}
	filter, err = filter.WithPoolIDsMatch([]uint64{poolID}, TagMatchAny)
	require.NoError(t, err)

	markers, err := pg.ListMarketMap(ctx, tx, filter, []string{"catalog", "pool", "post", "voucher", "want"}, 20, true)
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

	// The All list uses a separate statement with the same viewport precision
	// contract. Exercise facets and both cursor pages against these fixtures.
	page, err := pg.ListMarketplace(ctx, tx, filter, nil, nil, MarketplaceCursor{}, 2, true)
	require.NoError(t, err)
	require.Len(t, page, 2)
	last := page[len(page)-1]
	next, err := pg.ListMarketplace(ctx, tx, filter, nil, nil, MarketplaceCursor{
		Rank: last.Rank, UpdatedAt: last.UpdatedAt, Kind: last.Kind, ID: last.ID,
	}, 2, false)
	require.NoError(t, err)
	require.Len(t, next, 2)
	listKinds := map[string]bool{}
	for _, row := range append(page, next...) {
		require.False(t, listKinds[row.Kind], "duplicate %s across pages", row.Kind)
		listKinds[row.Kind] = true
	}
	require.Equal(t, map[string]bool{"voucher": true, "want": true, "pool": true, "catalog": true}, listKinds)

	// Posts stay out of the legacy omitted-kinds list, but an explicit desktop
	// viewport selection returns them and a total for every underlying resource.
	explicit, err := pg.ListMarketplace(ctx, tx, filter,
		[]string{"catalog", "pool", "post", "voucher", "want"}, nil,
		MarketplaceCursor{}, 10, false)
	require.NoError(t, err)
	require.Len(t, explicit, 5)
	require.Equal(t, uint64(5), explicit[0].TotalCount)
	explicitKinds := map[string]bool{}
	for _, row := range explicit {
		explicitKinds[row.Kind] = true
		require.Equal(t, uint64(5), row.TotalCount)
	}
	for _, kind := range []string{"catalog", "pool", "post", "voucher", "want"} {
		require.True(t, explicitKinds[kind], "explicit marketplace missing %s", kind)
	}

	// Schedule timing must select the same Post set in the clustered map query
	// and the exact paginated viewport list without narrowing commerce kinds.
	var endedReportID uint64
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO reports(title,description,author_address,status,geo_lat,geo_lng,period_from,period_to)
		VALUES($1,'Map fixture',$2,'APPROVED',-3.68,39.83,CURRENT_TIMESTAMP - interval '2 hours',CURRENT_TIMESTAMP - interval '1 hour')
		RETURNING id`, term+" ended post", ownerAddress).Scan(&endedReportID))
	_, err = tx.Exec(ctx, `INSERT INTO report_vouchers(report_id,voucher_id) VALUES($1,$2)`, endedReportID, voucherID)
	require.NoError(t, err)

	filter.ReportTiming = ReportTimingFuture
	futureMap, err := pg.ListMarketMap(ctx, tx, filter,
		[]string{"catalog", "pool", "post", "voucher", "want"}, 20, false)
	require.NoError(t, err)
	require.Len(t, futureMap.Features, 5)
	futureList, err := pg.ListMarketplace(ctx, tx, filter,
		[]string{"catalog", "pool", "post", "voucher", "want"}, nil,
		MarketplaceCursor{}, 10, false)
	require.NoError(t, err)
	require.Len(t, futureList, 5)
	mapPostIDs := []string{}
	for _, feature := range futureMap.Features {
		if feature.Kind != nil && *feature.Kind == "post" && feature.ResourceID != nil {
			mapPostIDs = append(mapPostIDs, *feature.ResourceID)
		}
	}
	listPostIDs := []string{}
	for _, row := range futureList {
		if row.Kind == "post" {
			listPostIDs = append(listPostIDs, strconv.FormatUint(row.ID, 10))
		}
	}
	require.Equal(t, []string{strconv.FormatUint(reportID, 10)}, mapPostIDs)
	require.Equal(t, mapPostIDs, listPostIDs)
	for _, row := range futureList {
		require.NotEqual(t, endedReportID, row.ID)
	}

	// Every kind must also respect a nearby, non-overlapping decimal box.
	filter.Bounds = []CatalogBounds{{MinLng: 39.84, MinLat: -3.8, MaxLng: 39.85, MaxLat: -3.2}}
	emptyMap, err := pg.ListMarketMap(ctx, tx, filter, nil, 16, true)
	require.NoError(t, err)
	require.Empty(t, emptyMap.Features)
	emptyList, err := pg.ListMarketplace(ctx, tx, filter, nil, nil, MarketplaceCursor{}, 20, true)
	require.NoError(t, err)
	require.Empty(t, emptyList)
}

func TestDiscoveryBoundsParametersRetainFloatPrecisionDB(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()

	for _, test := range []struct {
		name      string
		sql       string
		positions []int
	}{
		{"map", pg.queries.ListMarketMap, []int{10, 11, 12, 13, 15, 16, 17, 18}},
		{"marketplace", pg.queries.ListMarketplace, []int{22, 23, 24, 25, 27, 28, 29, 30}},
		{"map discovery", pg.queries.ListMarketMapDiscovery, []int{9, 10, 11, 12, 14, 15, 16, 17}},
	} {
		t.Run(test.name, func(t *testing.T) {
			statement, err := tx.Conn().Prepare(context.Background(), "precision_"+test.name, test.sql)
			require.NoError(t, err)
			for _, position := range test.positions {
				require.Equal(t, uint32(pgtype.Float8OID), statement.ParamOIDs[position-1],
					"coordinate $%d must preserve decimals", position)
			}
		})
	}
}

func TestMarketMapFractionalBoundsQuadrantsAndEdgesDB(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()
	const term = "Zqvxprecision"
	for index, coordinate := range []struct{ lat, lng float64 }{
		{-3.68, 39.83}, {3.68, 39.83}, {-3.68, -39.83}, {3.68, -39.83},
	} {
		id := seedSearchVoucher(t, tx, fmt.Sprintf("%s%d", term, index), fmt.Sprintf("ZPREC%d", index))
		_, err := tx.Exec(ctx, `UPDATE vouchers SET geo_lat=$1,geo_lng=$2 WHERE id=$3`, coordinate.lat, coordinate.lng, id)
		require.NoError(t, err)
		filter, err := NewCatalogFilter(term, nil, TagMatchAny)
		require.NoError(t, err)
		filter.Bounds = []CatalogBounds{{
			MinLng: coordinate.lng, MinLat: coordinate.lat,
			MaxLng: coordinate.lng + 0.01, MaxLat: coordinate.lat + 0.01,
		}}
		result, err := pg.ListMarketMap(ctx, tx, filter, []string{"voucher"}, 16, false)
		require.NoError(t, err)
		require.Len(t, result.Features, 1, "fractional minimum edge at %+v", coordinate)
		require.Equal(t, coordinate.lat, result.Features[0].GeoLat)
		require.Equal(t, coordinate.lng, result.Features[0].GeoLng)
		filter.Bounds = []CatalogBounds{{
			MinLng: coordinate.lng - 0.01, MinLat: coordinate.lat - 0.01,
			MaxLng: coordinate.lng, MaxLat: coordinate.lat,
		}}
		result, err = pg.ListMarketMap(ctx, tx, filter, []string{"voucher"}, 16, false)
		require.NoError(t, err)
		require.Len(t, result.Features, 1, "fractional maximum edge at %+v", coordinate)
	}
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

func TestMarketMapFeatureLimitAtMaximumZoomDB(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()
	const term = "Zqvxmaplimit"

	_, err := tx.Exec(ctx, `
		INSERT INTO vouchers(chain_id,address,name,geo_lat,geo_lng)
		SELECT 100,
			'0x'||lpad(to_hex(900000000000000000+series),40,'0'),
			$1||series::text,
			-4.0+(series::FLOAT8/100000.0),
			39.0+(series::FLOAT8/100000.0)
		FROM generate_series(1,2005) series`, term)
	require.NoError(t, err)

	filter, err := NewCatalogFilter(term, nil, TagMatchAny)
	require.NoError(t, err)
	filter.Bounds = []CatalogBounds{{MinLng: 38.9, MinLat: -4.1, MaxLng: 39.1, MaxLat: -3.9}}
	result, err := pg.ListMarketMap(ctx, tx, filter, []string{"voucher"}, 20, false)
	require.NoError(t, err)
	require.Len(t, result.Features, MarketMapMaxFeatures)
	for _, feature := range result.Features {
		require.Equal(t, "marker", feature.Type)
	}
}

func TestMarketMapFractionalAntimeridianBoundsDB(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()
	const term = "Zqvxsplitprecision"
	for index, longitude := range []float64{-179.98, 179.98, -179.94, 179.94} {
		id := seedSearchVoucher(t, tx, fmt.Sprintf("%s%d", term, index), fmt.Sprintf("ZSPLIT%d", index))
		_, err := tx.Exec(ctx, `UPDATE vouchers SET geo_lat=-3.68,geo_lng=$1 WHERE id=$2`, longitude, id)
		require.NoError(t, err)
	}
	filter, err := NewCatalogFilter(term, nil, TagMatchAny)
	require.NoError(t, err)
	filter.Bounds = []CatalogBounds{
		{MinLng: -180, MinLat: -3.69, MaxLng: -179.97, MaxLat: -3.67},
		{MinLng: 179.97, MinLat: -3.69, MaxLng: 180, MaxLat: -3.67},
	}
	result, err := pg.ListMarketMap(ctx, tx, filter, []string{"voucher"}, 16, false)
	require.NoError(t, err)
	require.Len(t, result.Features, 2)
	for _, feature := range result.Features {
		require.Contains(t, []float64{-179.98, 179.98}, feature.GeoLng)
	}
	list, err := pg.ListMarketplace(ctx, tx, filter, []string{"voucher"}, nil, MarketplaceCursor{}, 20, false)
	require.NoError(t, err)
	require.Len(t, list, 2)
}

func TestMarketMapPointsEveryKindFiltersAndStableOrderDB(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()

	const term = "Zqvxmappoints"
	var ownerID uint64
	var ownerAddress string
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO users(blockchain_address,name,email)
		VALUES('0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),$1,gen_random_uuid()::text||'@map.test')
		RETURNING id,blockchain_address`, term+" owner").Scan(&ownerID, &ownerAddress))

	// Fractional coordinates in every quadrant, far apart: no viewport holds them all.
	voucherID := seedSearchVoucher(t, tx, term+" voucher", "ZPTS")
	_, err := tx.Exec(ctx, `UPDATE vouchers SET owner_address=$1,geo_lat=-3.6884991293904363,geo_lng=39.830381973540625 WHERE id=$2`, ownerAddress, voucherID)
	require.NoError(t, err)
	var poolID uint64
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO pools(chain_id,address,name,symbol,geo_lat,geo_lng)
		VALUES(100,'0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),$1,'ZPP',51.507351,-0.127758)
		RETURNING id`, term+" pool").Scan(&poolID))
	_, err = tx.Exec(ctx, `INSERT INTO pool_tokens(pool_id,voucher_id,is_allowed) VALUES($1,$2,true)`, poolID, voucherID)
	require.NoError(t, err)
	var wantID uint64
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO wants(owner_user_id,name,description,location_name,geo_lat,geo_lng,status,contact_text)
		VALUES($1,$2,'Map fixture','Map fixture',-33.868820,151.209296,'OPEN','Public contact')
		RETURNING id`, ownerID, term+" want").Scan(&wantID))
	var catalogID uint64
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO catalogs(owner_user_id,name,description,location_name,geo_lat,geo_lng)
		VALUES($1,$2,'Map fixture','Map fixture',40.712776,-74.005974)
		RETURNING id`, ownerID, term+" catalog").Scan(&catalogID))
	_, err = tx.Exec(ctx, `INSERT INTO catalog_vouchers(catalog_id,voucher_id) VALUES($1,$2)`, catalogID, voucherID)
	require.NoError(t, err)
	var reportID, endedReportID uint64
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO reports(title,description,author_address,status,geo_lat,geo_lng)
		VALUES($1,'Map fixture',$2,'APPROVED',-1.292066,36.821945)
		RETURNING id`, term+" post", ownerAddress).Scan(&reportID))
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO reports(title,description,author_address,status,geo_lat,geo_lng,period_from,period_to)
		VALUES($1,'Map fixture',$2,'APPROVED',-1.292066,36.821945,CURRENT_TIMESTAMP - interval '2 hours',CURRENT_TIMESTAMP - interval '1 hour')
		RETURNING id`, term+" ended post", ownerAddress).Scan(&endedReportID))
	for _, id := range []uint64{reportID, endedReportID} {
		_, err = tx.Exec(ctx, `INSERT INTO report_vouchers(report_id,voucher_id) VALUES($1,$2)`, id, voucherID)
		require.NoError(t, err)
	}
	var tag string
	require.NoError(t, tx.QueryRow(ctx, `SELECT value FROM token_tag ORDER BY value LIMIT 1`).Scan(&tag))
	_, err = tx.Exec(ctx, `INSERT INTO voucher_tags(voucher_id,tag_value) VALUES($1,$2)`, voucherID, tag)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO want_tags(want_id,tag_value) VALUES($1,$2)`, wantID, tag)
	require.NoError(t, err)

	allKinds := []string{"catalog", "pool", "post", "voucher", "want"}
	filter, err := NewCatalogFilter(term, nil, TagMatchAny)
	require.NoError(t, err)
	filter.ReportTiming = ReportTimingFuture
	// A viewport on the filter must not narrow the points.
	filter.Bounds = []CatalogBounds{{MinLng: 39.7, MinLat: -3.8, MaxLng: 40.2, MaxLat: -3.2}}

	result, err := pg.ListMarketMapPoints(ctx, tx, filter, allKinds)
	require.NoError(t, err)
	require.False(t, result.Truncated)
	located := make([]MarketMapPoint, 0, len(result.Points))
	for _, point := range result.Points {
		if point.Kind == "post" || point.Kind == "want" {
			require.NotNil(t, point.MarkerIcon, "%s keeps its default marker", point.Kind)
		}
		point.MarkerIcon, point.MarkerColor = nil, nil
		located = append(located, point)
	}
	require.Equal(t, []MarketMapPoint{
		{Kind: "catalog", ResourceID: strconv.FormatUint(catalogID, 10), Name: term + " catalog", GeoLat: 40.712776, GeoLng: -74.005974},
		{Kind: "pool", ResourceID: pointResourceID(t, tx, "pools", poolID), Name: term + " pool", GeoLat: 51.507351, GeoLng: -0.127758},
		{Kind: "post", ResourceID: strconv.FormatUint(reportID, 10), Name: term + " post", GeoLat: -1.292066, GeoLng: 36.821945},
		{Kind: "voucher", ResourceID: pointResourceID(t, tx, "vouchers", voucherID), Name: term + " voucher", GeoLat: -3.6884991293904363, GeoLng: 39.830381973540625},
		{Kind: "want", ResourceID: strconv.FormatUint(wantID, 10), Name: term + " want", GeoLat: -33.868820, GeoLng: 151.209296},
	}, located, "every kind, ordered by kind then id, with fractional coordinates intact")

	again, err := pg.ListMarketMapPoints(ctx, tx, filter, allKinds)
	require.NoError(t, err)
	require.Equal(t, result.Points, again.Points, "order must be stable: a stack is keyed by its first member")

	kindsOf := func(points []MarketMapPoint) []string {
		kinds := []string{}
		for _, point := range points {
			kinds = append(kinds, point.Kind)
		}
		return kinds
	}

	subset, err := pg.ListMarketMapPoints(ctx, tx, filter, []string{"want", "pool"})
	require.NoError(t, err)
	require.Equal(t, []string{"pool", "want"}, kindsOf(subset.Points))

	tagged := filter
	tagged.Tags = []string{tag}
	taggedPoints, err := pg.ListMarketMapPoints(ctx, tx, tagged, allKinds)
	require.NoError(t, err)
	require.Equal(t, []string{"voucher", "want"}, kindsOf(taggedPoints.Points))

	credit, err := filter.WithDiscoveryPoolIDs([]uint64{poolID})
	require.NoError(t, err)
	creditPoints, err := pg.ListMarketMapPoints(ctx, tx, credit, allKinds)
	require.NoError(t, err)
	require.Equal(t, []string{"catalog", "pool", "post", "voucher", "want"}, kindsOf(creditPoints.Points))

	past := filter
	past.ReportTiming = ReportTimingPast
	pastPoints, err := pg.ListMarketMapPoints(ctx, tx, past, []string{"post"})
	require.NoError(t, err)
	require.Len(t, pastPoints.Points, 1)
	require.Equal(t, strconv.FormatUint(endedReportID, 10), pastPoints.Points[0].ResourceID)

	byVoucher, err := filter.WithVoucherIDs([]uint64{voucherID}, TagMatchAny)
	require.NoError(t, err)
	byVoucher.ReportTiming = ReportTimingAll
	voucherPosts, err := pg.ListMarketMapPoints(ctx, tx, byVoucher, []string{"post"})
	require.NoError(t, err)
	require.Len(t, voucherPosts.Points, 2)

	none, err := NewCatalogFilter(term+"nothing", nil, TagMatchAny)
	require.NoError(t, err)
	empty, err := pg.ListMarketMapPoints(ctx, tx, none, allKinds)
	require.NoError(t, err)
	require.NotNil(t, empty.Points, "an empty result must serialize as []")
	require.Empty(t, empty.Points)
	require.False(t, empty.Truncated)
}

func pointResourceID(t *testing.T, tx pgx.Tx, table string, id uint64) string {
	t.Helper()
	var address string
	require.NoError(t, tx.QueryRow(context.Background(), `SELECT address FROM `+table+` WHERE id=$1`, id).Scan(&address))
	return address
}

func TestMarketMapPointsCeilingSetsTruncatedDB(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()
	const term = "Zqvxmapceiling"

	_, err := tx.Exec(ctx, `
		INSERT INTO vouchers(chain_id,address,name,geo_lat,geo_lng)
		SELECT 100,'0x'||lpad(to_hex(800000000000000000+series),40,'0'),$1||series::text,
			-4.0+(series::FLOAT8/1000000.0),39.0+(series::FLOAT8/1000000.0)
		FROM generate_series(1,$2::INT) series`, term, MarketMapPointsCeiling)
	require.NoError(t, err)
	filter, err := NewCatalogFilter(term, nil, TagMatchAny)
	require.NoError(t, err)

	atCeiling, err := pg.ListMarketMapPoints(ctx, tx, filter, []string{"voucher"})
	require.NoError(t, err)
	require.Len(t, atCeiling.Points, MarketMapPointsCeiling)
	require.False(t, atCeiling.Truncated, "exactly the ceiling is complete")

	_, err = tx.Exec(ctx, `
		INSERT INTO vouchers(chain_id,address,name,geo_lat,geo_lng)
		VALUES(100,'0x'||lpad(to_hex(800000000000000000+$2::BIGINT),40,'0'),$1,-3.5,39.5)`,
		term+"extra", MarketMapPointsCeiling+1)
	require.NoError(t, err)
	over, err := pg.ListMarketMapPoints(ctx, tx, filter, []string{"voucher"})
	require.NoError(t, err)
	require.Len(t, over.Points, MarketMapPointsCeiling)
	require.True(t, over.Truncated, "one point over the ceiling must be reported, not dropped silently")
}

func TestMarketMapTagFacetsAreScopedToTheViewportDB(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()
	const term = "Zqvxmapfacets"

	var tag string
	require.NoError(t, tx.QueryRow(ctx, `SELECT value FROM token_tag ORDER BY value LIMIT 1`).Scan(&tag))
	for index, coordinate := range []struct{ lat, lng float64 }{
		{-3.68, 179.98}, {-3.68, -179.98}, {-3.68, 39.83},
	} {
		id := seedSearchVoucher(t, tx, fmt.Sprintf("%s%d", term, index), fmt.Sprintf("ZFAC%d", index))
		_, err := tx.Exec(ctx, `UPDATE vouchers SET geo_lat=$1,geo_lng=$2 WHERE id=$3`, coordinate.lat, coordinate.lng, id)
		require.NoError(t, err)
		_, err = tx.Exec(ctx, `INSERT INTO voucher_tags(voucher_id,tag_value) VALUES($1,$2)`, id, tag)
		require.NoError(t, err)
	}
	filter, err := NewCatalogFilter(term, nil, TagMatchAny)
	require.NoError(t, err)

	world, err := pg.ListMarketMapTagFacets(ctx, tx, filter, []string{"voucher"})
	require.NoError(t, err)
	require.Equal(t, []TagFacet{{Tag: tag, Count: 3}}, world)

	filter.Bounds = []CatalogBounds{{MinLng: 39.82, MinLat: -3.69, MaxLng: 39.84, MaxLat: -3.67}}
	box, err := pg.ListMarketMapTagFacets(ctx, tx, filter, []string{"voucher"})
	require.NoError(t, err)
	require.Equal(t, []TagFacet{{Tag: tag, Count: 1}}, box)

	filter.Bounds = []CatalogBounds{
		{MinLng: -180, MinLat: -3.69, MaxLng: -179.97, MaxLat: -3.67},
		{MinLng: 179.97, MinLat: -3.69, MaxLng: 180, MaxLat: -3.67},
	}
	antimeridian, err := pg.ListMarketMapTagFacets(ctx, tx, filter, []string{"voucher"})
	require.NoError(t, err)
	require.Equal(t, []TagFacet{{Tag: tag, Count: 2}}, antimeridian)

	filter.Bounds = []CatalogBounds{{MinLng: 10, MinLat: 10, MaxLng: 10.5, MaxLat: 10.5}}
	none, err := pg.ListMarketMapTagFacets(ctx, tx, filter, []string{"voucher"})
	require.NoError(t, err)
	require.NotNil(t, none)
	require.Empty(t, none)
}
