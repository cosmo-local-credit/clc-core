package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/stretchr/testify/require"
)

func TestMarketMapFractionalViewportEndpointDB(t *testing.T) {
	a := newMarketAPIWithStore(t)
	marker := fmt.Sprintf("%sMapPrecision%d", searchTokenAPI, time.Now().UnixNano())
	ids, cleanup := seedSearchFixtures(t, a, marker, []searchFixture{
		{name: marker + " First", symbol: "MPRFIRST"},
		{name: marker + " Second", symbol: "MPRSECOND"},
		{name: marker + " Outside", symbol: "MPROUTSIDE"},
	})
	t.Cleanup(cleanup)

	for i, id := range ids {
		lat, lng := -3.6884991293904363, 39.830381973540625
		switch i {
		case 1:
			lat, lng = -3.689533599376162, 39.82907096058713
		case 2:
			lat, lng = -3.72, 39.87
		}
		_, err := a.store.Pool().Exec(t.Context(), `UPDATE vouchers SET geo_lat=$1,geo_lng=$2 WHERE id=$3`, lat, lng, id)
		require.NoError(t, err)
	}

	// Freeze only the cache clock so slow CI cannot turn a cache assertion into
	// an accidental database read. The HTTP route and database remain real.
	now := time.Now()
	a.marketMapCache = newMarketMapCache(func() time.Time { return now })
	get := func(bbox, zoom string) (*httptest.ResponseRecorder, []store.MarketMapFeature) {
		t.Helper()
		target := searchURL("/api/v2/market-map", map[string][]string{
			"bbox": {bbox}, "zoom": {zoom}, "q": {marker}, "kinds": {"voucher"},
		})
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.RemoteAddr = "198.51.100.91:5000"
		res := httptest.NewRecorder()
		a.router.ServeHTTP(res, req)
		require.Equal(t, http.StatusOK, res.Code, res.Body.String())
		require.Equal(t, "public, max-age=15, stale-while-revalidate=60", res.Header().Get("Cache-Control"))
		require.Empty(t, res.Header().Values("Set-Cookie"))
		var envelope struct {
			OK     bool `json:"ok"`
			Result struct {
				Features []store.MarketMapFeature `json:"features"`
			} `json:"result"`
		}
		require.NoError(t, json.Unmarshal(res.Body.Bytes(), &envelope))
		require.True(t, envelope.OK)
		require.NotNil(t, envelope.Result.Features, "empty results must serialize as []")
		return res, envelope.Result.Features
	}

	const bounds = "39.82,-3.70,39.84,-3.67"
	_, overlapping := get(bounds, "14.25")
	require.Len(t, overlapping, 1)
	require.Equal(t, "cluster", overlapping[0].Type)
	require.EqualValues(t, 2, overlapping[0].Count)
	require.Equal(t, map[string]int{"voucher": 2}, overlapping[0].Kinds)
	require.Greater(t, overlapping[0].ExpansionZoom, 14.25)
	require.LessOrEqual(t, overlapping[0].ExpansionZoom, 14.75)

	_, separated := get(bounds, "14.75")
	require.Len(t, separated, 2, "the real Want/Catalog separation must split before zoom 16")
	for _, feature := range separated {
		require.Equal(t, "marker", feature.Type)
	}

	initial, markers := get(bounds, "16")
	require.Len(t, markers, 2, "fractional bounds must not truncate to an empty latitude line")
	names := make([]string, 0, len(markers))
	for _, feature := range markers {
		require.Equal(t, "marker", feature.Type)
		require.NotNil(t, feature.Kind)
		require.Equal(t, "voucher", *feature.Kind)
		require.NotNil(t, feature.ResourceID)
		require.NotEmpty(t, *feature.ResourceID)
		require.NotNil(t, feature.Name)
		names = append(names, *feature.Name)
		switch *feature.Name {
		case marker + " First":
			require.InDelta(t, -3.6884991293904363, feature.GeoLat, 0.0000001)
			require.InDelta(t, 39.830381973540625, feature.GeoLng, 0.0000001)
		case marker + " Second":
			require.InDelta(t, -3.689533599376162, feature.GeoLat, 0.0000001)
			require.InDelta(t, 39.82907096058713, feature.GeoLng, 0.0000001)
		default:
			t.Fatalf("unexpected marker %q", *feature.Name)
		}
	}
	require.ElementsMatch(t, []string{marker + " First", marker + " Second"}, names)

	_, clusters := get(bounds, "1")
	require.Len(t, clusters, 1)
	require.Equal(t, "cluster", clusters[0].Type)
	require.EqualValues(t, 2, clusters[0].Count)
	require.Equal(t, map[string]int{"voucher": 2}, clusters[0].Kinds)
	require.Greater(t, clusters[0].ExpansionZoom, 1.0)
	require.LessOrEqual(t, clusters[0].ExpansionZoom, 20.0)

	_, empty := get("39.90,-3.90,39.92,-3.88", "16")
	require.Empty(t, empty, "a different viewport must have a separate cache entry")

	_, err := a.store.Pool().Exec(t.Context(), `UPDATE vouchers SET name=$1 WHERE id=$2`, marker+" Renamed", ids[0])
	require.NoError(t, err)
	cached, _ := get(bounds, "16")
	require.JSONEq(t, initial.Body.String(), cached.Body.String(), "refresh must reuse the cached response")

	now = now.Add(marketMapCacheTTL)
	_, refreshed := get(bounds, "16")
	require.Len(t, refreshed, 2)
	names = names[:0]
	for _, feature := range refreshed {
		require.NotNil(t, feature.Name)
		names = append(names, *feature.Name)
	}
	require.ElementsMatch(t, []string{marker + " Renamed", marker + " Second"}, names)
}

func TestMarketMapPostedWindowHidesOldPostPinsDB(t *testing.T) {
	a := newMarketAPIWithStore(t)
	marker := fmt.Sprintf("%sPostedPin%d", searchTokenAPI, time.Now().UnixNano())
	now := time.Now().UTC()
	const lat, lng = -3.68, 39.83

	insert := func(title string, created time.Time) uint64 {
		t.Helper()
		var id uint64
		err := a.store.Pool().QueryRow(t.Context(), `
			INSERT INTO reports (title, description, status, author_address, geo_lat, geo_lng, created_at, updated_at)
			VALUES ($1, 'x', 'APPROVED', '0x0000000000000000000000000000000000000002', $2, $3, $4, $4)
			RETURNING id`, title, lat, lng, created).Scan(&id)
		require.NoError(t, err)
		return id
	}
	recentID := insert(marker+" recent", now)
	oldID := insert(marker+" old", now.AddDate(-2, 0, 0))
	t.Cleanup(func() {
		_, _ = a.store.Pool().Exec(t.Context(), `DELETE FROM reports WHERE id = ANY($1)`, []uint64{recentID, oldID})
	})

	names := func(posted string) []string {
		t.Helper()
		params := map[string][]string{
			"bbox": {"39.7,-3.8,40.2,-3.2"}, "zoom": {"20"}, "kinds": {"post"}, "q": {marker},
		}
		if posted != "" {
			params["posted"] = []string{posted}
		}
		req := httptest.NewRequest(http.MethodGet, searchURL("/api/v2/market-map", params), nil)
		req.RemoteAddr = "198.51.100.92:5000"
		res := httptest.NewRecorder()
		a.router.ServeHTTP(res, req)
		require.Equal(t, http.StatusOK, res.Code, res.Body.String())
		var envelope struct {
			Result struct {
				Features []store.MarketMapFeature `json:"features"`
			} `json:"result"`
		}
		require.NoError(t, json.Unmarshal(res.Body.Bytes(), &envelope))
		got := make([]string, 0, len(envelope.Result.Features))
		for _, feature := range envelope.Result.Features {
			if feature.Name != nil {
				got = append(got, *feature.Name)
			}
		}
		return got
	}

	require.Equal(t, []string{marker + " recent"}, names(""))
	require.ElementsMatch(t, []string{marker + " recent", marker + " old"}, names("all"))
}
