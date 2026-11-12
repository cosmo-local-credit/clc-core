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
	"golang.org/x/time/rate"
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

func TestMarketMapPointsEndpointIgnoresViewportDB(t *testing.T) {
	a := newMarketAPIWithStore(t)
	marker := fmt.Sprintf("%sMapPoints%d", searchTokenAPI, time.Now().UnixNano())
	ids, cleanup := seedSearchFixtures(t, a, marker, []searchFixture{
		{name: marker + " Nairobi", symbol: "MPTNBO"},
		{name: marker + " Mombasa", symbol: "MPTMBA"},
	})
	t.Cleanup(cleanup)
	coordinates := [][2]float64{{-1.2920659, 36.8219462}, {-4.0434771, 39.6682065}}
	for i, id := range ids {
		_, err := a.store.Pool().Exec(t.Context(), `UPDATE vouchers SET geo_lat=$1,geo_lng=$2 WHERE id=$3`,
			coordinates[i][0], coordinates[i][1], id)
		require.NoError(t, err)
	}
	now := time.Now()
	a.marketMapCache = newMarketMapCache(func() time.Time { return now })

	type pointsEnvelope struct {
		OK     bool `json:"ok"`
		Result struct {
			Points    []store.MarketMapPoint `json:"points"`
			Truncated *bool                  `json:"truncated"`
			Features  any                    `json:"features"`
		} `json:"result"`
	}
	get := func(params map[string][]string) (*httptest.ResponseRecorder, pointsEnvelope) {
		t.Helper()
		params["q"] = []string{marker}
		params["kinds"] = []string{"voucher"}
		req := httptest.NewRequest(http.MethodGet, searchURL("/api/v2/market-map/points", params), nil)
		req.RemoteAddr = "198.51.100.93:5000"
		res := httptest.NewRecorder()
		a.router.ServeHTTP(res, req)
		require.Equal(t, http.StatusOK, res.Code, res.Body.String())
		require.Equal(t, "public, max-age=15, stale-while-revalidate=60", res.Header().Get("Cache-Control"))
		require.Empty(t, res.Header().Values("Set-Cookie"))
		var envelope pointsEnvelope
		require.NoError(t, json.Unmarshal(res.Body.Bytes(), &envelope))
		require.True(t, envelope.OK)
		return res, envelope
	}

	// A viewport over Nairobi only, at a zoom that would cluster: both points.
	first, body := get(map[string][]string{"bbox": {"36.7,-1.4,36.9,-1.2"}, "zoom": {"5.5"}})
	require.NotNil(t, body.Result.Truncated)
	require.False(t, *body.Result.Truncated)
	require.Nil(t, body.Result.Features, "points carry no server clusters")
	require.Len(t, body.Result.Points, 2)
	byName := map[string]store.MarketMapPoint{}
	for _, point := range body.Result.Points {
		require.Equal(t, "voucher", point.Kind)
		require.NotEmpty(t, point.ResourceID)
		byName[point.Name] = point
	}
	require.InDelta(t, -1.2920659, byName[marker+" Nairobi"].GeoLat, 1e-9)
	require.InDelta(t, 36.8219462, byName[marker+" Nairobi"].GeoLng, 1e-9)
	require.InDelta(t, -4.0434771, byName[marker+" Mombasa"].GeoLat, 1e-9)
	require.InDelta(t, 39.6682065, byName[marker+" Mombasa"].GeoLng, 1e-9)

	// Another viewport and zoom is the same cache entry: a rename is not seen
	// until the entry expires.
	_, err := a.store.Pool().Exec(t.Context(), `UPDATE vouchers SET name=$1 WHERE id=$2`, marker+" Renamed", ids[0])
	require.NoError(t, err)
	moved, _ := get(map[string][]string{"bbox": {"39.5,-4.2,39.8,-3.9"}, "zoom": {"13"}})
	require.JSONEq(t, first.Body.String(), moved.Body.String())
	unbounded, _ := get(map[string][]string{})
	require.JSONEq(t, first.Body.String(), unbounded.Body.String())

	now = now.Add(marketMapCacheTTL)
	_, refreshed := get(map[string][]string{})
	names := []string{}
	for _, point := range refreshed.Result.Points {
		names = append(names, point.Name)
	}
	require.ElementsMatch(t, []string{marker + " Renamed", marker + " Mombasa"}, names)
}

func TestMarketMapFacetsEndpointCountsTheViewportDB(t *testing.T) {
	a := newMarketAPIWithStore(t)
	marker := fmt.Sprintf("%sMapFacets%d", searchTokenAPI, time.Now().UnixNano())
	var tag string
	require.NoError(t, a.store.Pool().QueryRow(t.Context(), `SELECT value FROM token_tag ORDER BY value LIMIT 1`).Scan(&tag))
	ids, cleanup := seedSearchFixtures(t, a, marker, []searchFixture{
		{name: marker + " Inside", symbol: "MFCIN", tags: []string{tag}},
		{name: marker + " Outside", symbol: "MFCOUT", tags: []string{tag}},
	})
	t.Cleanup(cleanup)
	_, err := a.store.Pool().Exec(t.Context(), `UPDATE vouchers SET geo_lat=-4.0434771,geo_lng=39.6682065 WHERE id=$1`, ids[1])
	require.NoError(t, err)

	get := func(params map[string][]string) map[string]json.RawMessage {
		t.Helper()
		params["q"] = []string{marker}
		req := httptest.NewRequest(http.MethodGet, searchURL("/api/v2/market-map/facets", params), nil)
		req.RemoteAddr = "198.51.100.94:5000"
		res := httptest.NewRecorder()
		a.router.ServeHTTP(res, req)
		require.Equal(t, http.StatusOK, res.Code, res.Body.String())
		var envelope struct {
			Result map[string]json.RawMessage `json:"result"`
		}
		require.NoError(t, json.Unmarshal(res.Body.Bytes(), &envelope))
		return envelope.Result
	}

	inside := get(map[string][]string{"bbox": {"36.7,-1.4,36.9,-1.2"}, "kinds": {"voucher"}})
	require.JSONEq(t, fmt.Sprintf(`[{"tag":%q,"count":1}]`, tag), string(inside["tagFacets"]))
	require.JSONEq(t, `[]`, string(inside["postVoucherFacets"]))
	require.JSONEq(t, `[]`, string(inside["postPoolFacets"]))
	require.NotContains(t, inside, "points")

	both := get(map[string][]string{"bbox": {"36,-5,40,-1"}, "kinds": {"voucher", "post"}})
	require.JSONEq(t, fmt.Sprintf(`[{"tag":%q,"count":2}]`, tag), string(both["tagFacets"]))
	require.JSONEq(t, `[]`, string(both["postVoucherFacets"]), "no Post links these vouchers")

	none := get(map[string][]string{"bbox": {"10,10,10.5,10.5"}, "kinds": {"voucher"}})
	require.JSONEq(t, `[]`, string(none["tagFacets"]))
}

func TestMarketMapPointsRateLimitAndCapacityDB(t *testing.T) {
	a := newMarketAPIWithStore(t)
	a.marketMapGuestLimiter = newMarketRateLimiterWithCapacity(rate.Limit(0), 1, 10, true, time.Now)
	request := func(path, term string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, searchURL(path, map[string][]string{"q": {term}}), nil)
		req.RemoteAddr = "198.51.100.95:5000"
		res := httptest.NewRecorder()
		a.router.ServeHTTP(res, req)
		return res
	}
	marker := fmt.Sprintf("%sMapLimit%d", searchTokenAPI, time.Now().UnixNano())
	require.Equal(t, http.StatusOK, request("/api/v2/market-map/points", marker+"a").Code)
	// The cached response spends no token.
	require.Equal(t, http.StatusOK, request("/api/v2/market-map/points", marker+"a").Code)
	limited := request("/api/v2/market-map/facets", marker+"b")
	require.Equal(t, http.StatusTooManyRequests, limited.Code, limited.Body.String())
	require.NotEmpty(t, limited.Header().Get("Retry-After"))

	a.marketMapGuestLimiter = newMarketRateLimiterWithCapacity(rate.Inf, 1, 10, true, time.Now)
	a.marketMapExecutor.pending.Store(marketMapConcurrency + marketMapQueue)
	full := request("/api/v2/market-map/points", marker+"c")
	require.Equal(t, http.StatusServiceUnavailable, full.Code, full.Body.String())
	require.Equal(t, "1", full.Header().Get("Retry-After"))
}
