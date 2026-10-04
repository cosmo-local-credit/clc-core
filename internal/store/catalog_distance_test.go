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

func TestNearbyPostsUsePostLocation(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()
	const author = "0xAB00000000000000000000000000000000000123"
	_, err := tx.Exec(ctx, `INSERT INTO users (blockchain_address,name,email,geo_lat,geo_lng) VALUES ($1,'Nearby test user','nearby-' || $1 || '@example.test',0,0)`, author)
	require.NoError(t, err)
	id := seedSearchReport(t, tx, "Zqnearbyreport", "distance fixture", "Art")
	voucher := seedSearchVoucher(t, tx, "Zqnearbyreport voucher", "ZQNRV")
	_, err = tx.Exec(ctx, "INSERT INTO report_vouchers (report_id,voucher_id) VALUES ($1,$2)", id, voucher)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, "UPDATE reports SET author_address=lower($1) WHERE id=$2", author, id)
	require.NoError(t, err)
	boundary := 1_000_000.0 / 6_371_008.8 * 180 / math.Pi
	for _, tc := range []struct {
		name      string
		authorLat float64
		postLng   any
		want      int
	}{
		{"near post far author", 60, 0.0, 1},
		{"author moves nearby", 0, 0.0, 1},
		{"far post near author", 0, 60.0, 0},
		{"boundary included", 60, boundary, 1},
		{"beyond boundary", 0, boundary + 0.000001, 0},
		{"no post location", 0, nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tx.Exec(ctx, "UPDATE users SET geo_lat=$1 WHERE blockchain_address=$2", tc.authorLat, author)
			require.NoError(t, err)
			var lat any = 0.0
			if tc.postLng == nil {
				lat = nil
			}
			_, err = tx.Exec(ctx, "UPDATE reports SET geo_lat=$1,geo_lng=$2 WHERE id=$3", lat, tc.postLng, id)
			require.NoError(t, err)
			filter := mustFilter(t, "Zqnearbyreport", nil, TagMatchAny)
			filter.Nearby = &CatalogLocation{}
			for _, term := range []string{"Zqnearbyreport", ""} {
				filter.Term = term
				rows, err := pg.ListReportsFiltered(ctx, tx, filter, ReportCursor{}, false, 20)
				require.NoError(t, err)
				require.Len(t, rows, tc.want)
				if tc.want == 1 {
					require.Equal(t, id, rows[0].ID)
				}
			}
			filter.Term = "Zqnearbyreport"
			facets, err := pg.ListReportTagFacets(ctx, tx, filter)
			require.NoError(t, err)
			var count uint64
			for _, facet := range facets {
				if facet.Tag == "Art" {
					count = facet.Count
				}
			}
			require.EqualValues(t, tc.want, count)
			vouchers, err := pg.ListReportVoucherFacets(ctx, tx, filter)
			require.NoError(t, err)
			require.Len(t, vouchers, tc.want)
			filter.Nearby = nil
			rows, err := pg.ListReportsFiltered(ctx, tx, filter, ReportCursor{}, false, 20)
			require.NoError(t, err)
			require.Len(t, rows, 1)
		})
	}
}
