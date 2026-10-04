package store

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCatalogLocationValidation(t *testing.T) {
	zero, nan, inf, lat, lng := 0.0, math.NaN(), math.Inf(1), 91.0, 181.0
	for _, pair := range [][2]*float64{{nil, nil}, {&zero, nil}, {&nan, &zero}, {&zero, &inf}, {&lat, &zero}, {&zero, &lng}} {
		_, err := NewCatalogLocation(pair[0], pair[1])
		require.Error(t, err)
	}
	location, err := NewCatalogLocation(&zero, &zero)
	require.NoError(t, err)
	require.Equal(t, &CatalogLocation{}, location)
	require.True(t, (CatalogFilter{Nearby: location}).Active())
}

func TestNearbyBounds(t *testing.T) {
	for _, tc := range []struct {
		name         string
		viewer       CatalogLocation
		fullLng      bool
		minLat, maxL float64
	}{
		{"equator", CatalogLocation{Lat: 0, Lng: 0}, false, -9, 9},
		{"nairobi", CatalogLocation{Lat: -1.29, Lng: 36.82}, false, -10.3, 7.7},
		{"antimeridian", CatalogLocation{Lat: 0, Lng: 179}, true, -9, 9},
		{"pole", CatalogLocation{Lat: 89, Lng: 0}, true, 80, 90},
		{"south pole", CatalogLocation{Lat: -90, Lng: 10}, true, -90, -81},
	} {
		t.Run(tc.name, func(t *testing.T) {
			box := tc.viewer.bounds()
			require.InDelta(t, tc.minLat, box.MinLat, 0.1)
			require.InDelta(t, tc.maxL, box.MaxLat, 0.1)
			if tc.fullLng {
				require.Equal(t, -180.0, box.MinLng)
				require.Equal(t, 180.0, box.MaxLng)
				return
			}
			require.Less(t, box.MinLng, tc.viewer.Lng)
			require.Greater(t, box.MaxLng, tc.viewer.Lng)
			require.Greater(t, box.MinLng, -180.0)
			require.Less(t, box.MaxLng, 180.0)
		})
	}
}

// Execute the actual SQL predicate, including null handling and the inclusive
// boundary. This catches errors that a second Go distance implementation would miss.
func TestNearbyDistanceSQL(t *testing.T) {
	_, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	boundary := 1_000_000.0 / 6_371_008.8 * 180 / math.Pi
	for _, tc := range []struct {
		name     string
		viewer   CatalogLocation
		lat, lng any
		want     bool
	}{
		{"same point", CatalogLocation{}, 0.0, 0.0, true},
		{"inside", CatalogLocation{}, 0.0, boundary - 0.000001, true},
		{"boundary", CatalogLocation{}, 0.0, boundary, true},
		{"outside", CatalogLocation{}, 0.0, boundary + 0.000001, false},
		{"antimeridian", CatalogLocation{Lat: 0, Lng: 179}, 0.0, -179.0, true},
		{"pole", CatalogLocation{Lat: 89, Lng: 0}, 89.0, 180.0, true},
		{"missing", CatalogLocation{}, nil, nil, false},
		{"partial", CatalogLocation{}, 0.0, nil, false},
		{"invalid", CatalogLocation{}, 91.0, 0.0, false},
		{"nan", CatalogLocation{}, math.NaN(), 0.0, false},
		{"infinity", CatalogLocation{}, 0.0, math.Inf(1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := &sqlArgs{}
			predicate := nearbyPredicate(args, &tc.viewer, "point.lat", "point.lng")
			lat, lng := args.bind(tc.lat), args.bind(tc.lng)
			var actual bool
			err := tx.QueryRow(context.Background(), "SELECT "+predicate+
				" FROM (SELECT "+lat+"::double precision AS lat, "+lng+"::double precision AS lng) point", args.values...).Scan(&actual)
			require.NoError(t, err)
			require.Equal(t, tc.want, actual)
		})
	}
}

func TestNearbyCatalogPagesAndFacets(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()
	const marker = "Zqnearbycatalog"
	near := seedSearchVoucher(t, tx, marker+" near", "ZQNC1")
	far := seedSearchVoucher(t, tx, marker+" far", "ZQNC2")
	nearer := seedSearchVoucher(t, tx, marker+" nearer", "ZQNC3")
	_, err := tx.Exec(ctx, "UPDATE vouchers SET geo_lat = 60, geo_lng = 0 WHERE id = $1", far)
	require.NoError(t, err)
	for _, id := range []uint64{near, far, nearer} {
		seedTaggedOffering(t, tx, id, true, "Art")
	}
	filter := mustFilter(t, marker, nil, TagMatchAny)
	filter.Nearby = &CatalogLocation{Lat: -1.29, Lng: 36.82}
	for _, term := range []string{marker, ""} {
		filter.Term = term
		rows, err := pg.ListMarketVouchersFiltered(ctx, tx, nil, filter, CatalogFirstPage, 0, 1)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, nearer, rows[0].ID)
		rows, err = pg.ListMarketVouchersFiltered(ctx, tx, nil, filter, CatalogNextPage, nearer, 1)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, near, rows[0].ID)
		rows, err = pg.ListMarketVouchersFiltered(ctx, tx, nil, filter, CatalogPrevPage, near, 1)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, nearer, rows[0].ID)
	}
	filter.Term = marker
	facets, err := pg.ListMarketVoucherTagFacets(ctx, tx, nil, filter)
	require.NoError(t, err)
	for _, facet := range facets {
		if facet.Tag == "Art" {
			require.EqualValues(t, 2, facet.Count)
		}
	}
	// Viewport and distance combine; a box around only the far fixture stays empty.
	rows, err := pg.ListMarketVouchersFiltered(ctx, tx, &MarketVoucherBounds{MinLat: 59, MaxLat: 61, MinLng: -1, MaxLng: 1}, filter, CatalogFirstPage, 0, 20)
	require.NoError(t, err)
	require.Empty(t, rows)
	ordinary := mustFilter(t, marker, nil, TagMatchAny)
	rows, err = pg.ListMarketVouchersFiltered(ctx, tx, nil, ordinary, CatalogFirstPage, 0, 20)
	require.NoError(t, err)
	require.Len(t, rows, 3)

	for i, lat := range []float64{-1.29, 60} {
		var id uint64
		err := tx.QueryRow(ctx, `INSERT INTO pools (chain_id,address,name,symbol,decimals,geo_lat,geo_lng)
			VALUES (42220, '0x' || lpad(to_hex((random()*1e18)::bigint),40,'0'), $1, $2, 6, $3, 36.82) RETURNING id`, marker+string(rune('a'+i)), "ZQNCP"+string(rune('a'+i)), lat).Scan(&id)
		require.NoError(t, err)
		_, err = tx.Exec(ctx, "INSERT INTO pool_tags (pool_id,tag_value) VALUES ($1,'Art')", id)
		require.NoError(t, err)
	}
	pools, err := pg.ListPoolsFiltered(ctx, tx, filter, CatalogFirstPage, 0, 20)
	require.NoError(t, err)
	require.Len(t, pools, 1)
	facets, err = pg.ListPoolTagFacets(ctx, tx, filter)
	require.NoError(t, err)
	for _, facet := range facets {
		if facet.Tag == "Art" {
			require.EqualValues(t, 1, facet.Count)
		}
	}
}

func TestNearbyPostsUseCurrentCreatorLocation(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()
	const author = "0xAB00000000000000000000000000000000000123"
	_, err := tx.Exec(ctx, `INSERT INTO users (blockchain_address,name,email,geo_lat,geo_lng) VALUES ($1,'Nearby test user','nearby-' || $1 || '@example.test',0,0)`, author)
	require.NoError(t, err)
	id := seedSearchReport(t, tx, "Zqnearbyreport", "distance fixture", "Art")
	_, err = tx.Exec(ctx, "UPDATE reports SET author_address=lower($1),geo_lat=60,geo_lng=0 WHERE id=$2", author, id)
	require.NoError(t, err)
	filter := mustFilter(t, "Zqnearbyreport", nil, TagMatchAny)
	filter.Nearby = &CatalogLocation{}
	for _, term := range []string{"Zqnearbyreport", ""} {
		filter.Term = term
		rows, err := pg.ListReportsFiltered(ctx, tx, filter, ReportCursor{}, false, 20)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, id, rows[0].ID)
	}
	_, err = tx.Exec(ctx, "UPDATE reports SET geo_lat=NULL,geo_lng=NULL WHERE id=$1", id)
	require.NoError(t, err)
	rows, err := pg.ListReportsFiltered(ctx, tx, filter, ReportCursor{}, false, 20)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	// Moving the creator excludes the post, irrespective of its own location.
	_, err = tx.Exec(ctx, "UPDATE users SET geo_lat=60 WHERE blockchain_address=$1", author)
	require.NoError(t, err)
	rows, err = pg.ListReportsFiltered(ctx, tx, filter, ReportCursor{}, false, 20)
	require.NoError(t, err)
	require.Empty(t, rows)
	facets, err := pg.ListReportTagFacets(ctx, tx, filter)
	require.NoError(t, err)
	for _, facet := range facets {
		require.Zero(t, facet.Count)
	}
	filter.Nearby = nil
	rows, err = pg.ListReportsFiltered(ctx, tx, filter, ReportCursor{}, false, 20)
	require.NoError(t, err)
	require.NotEmpty(t, rows)
}
