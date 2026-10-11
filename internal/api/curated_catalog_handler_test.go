package api

import (
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/stretchr/testify/require"
)

func TestCuratedCatalogValidation(t *testing.T) {
	zero := 0.0
	valid := func() store.CuratedCatalogWrite {
		return store.CuratedCatalogWrite{Name: "  Čaršija 🌱  ", LocationName: "Sarajevo", GeoLat: &zero, GeoLng: &zero}
	}
	w := valid()
	w.Description = "  Community growers and repairers  "
	require.NoError(t, validateCuratedWrite(&w))
	require.Equal(t, "Čaršija 🌱", w.Name)
	require.Equal(t, "Community growers and repairers", w.Description)
	blank := ""
	w.ImageURL = &blank
	require.NoError(t, validateCuratedWrite(&w))
	require.Nil(t, w.ImageURL)
	for _, tc := range []struct {
		name   string
		change func(*store.CuratedCatalogWrite)
	}{
		{"empty name", func(w *store.CuratedCatalogWrite) { w.Name = "  " }},
		{"too long", func(w *store.CuratedCatalogWrite) { w.Name = strings.Repeat("ž", 121) }},
		{"description too long", func(w *store.CuratedCatalogWrite) { w.Description = strings.Repeat("ž", 501) }},
		{"missing location", func(w *store.CuratedCatalogWrite) { w.GeoLat = nil }},
		{"nan", func(w *store.CuratedCatalogWrite) { n := math.NaN(); w.GeoLat = &n }},
		{"invalid latitude", func(w *store.CuratedCatalogWrite) { n := 91.0; w.GeoLat = &n }},
		{"empty label", func(w *store.CuratedCatalogWrite) { w.LocationName = "" }},
		{"relative image", func(w *store.CuratedCatalogWrite) { s := "/image.png"; w.ImageURL = &s }},
		{"script image", func(w *store.CuratedCatalogWrite) { s := "javascript:alert(1)"; w.ImageURL = &s }},
	} {
		t.Run(tc.name, func(t *testing.T) { w := valid(); tc.change(&w); require.Error(t, validateCuratedWrite(&w)) })
	}
}

func TestCuratedCursorIsBoundToViewerLocationAndList(t *testing.T) {
	origin := curatedOrigin(7, &store.CatalogLocation{Lat: 43, Lng: 18}, "")
	raw := encodeCuratedCursor(store.CuratedCatalogCursor{ID: 4, Distance: 20, Origin: origin})
	got, err := curatedCursor(raw, origin)
	require.NoError(t, err)
	require.Equal(t, uint64(4), got.ID)
	for _, scope := range []string{"public", curatedOrigin(8, &store.CatalogLocation{Lat: 43, Lng: 18}, ""), curatedOrigin(7, &store.CatalogLocation{Lat: 43.000001, Lng: 18}, ""), curatedOrigin(7, &store.CatalogLocation{Lat: 43, Lng: 18}, "different"), "members:1:false"} {
		_, err = curatedCursor(raw, scope)
		require.Error(t, err)
	}
	_, err = curatedCursor("not-base64!", origin)
	require.Error(t, err)
	_, err = curatedCursor(encodeCuratedCursor(store.CuratedCatalogCursor{ID: 4, Distance: -1, Origin: origin}), origin)
	require.Error(t, err)
}

func TestCuratedCatalogRoutesPublicReadsAndAuthenticatedWrites(t *testing.T) {
	a := New(APIOpts{Logg: slog.New(slog.DiscardHandler)})
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/api/v2/catalogs/invalid", 400},
		{http.MethodGet, "/api/v2/catalogs/invalid/pools", 400},
		{http.MethodGet, "/api/v2/catalogs/invalid/vouchers", 400},
		{http.MethodGet, "/api/v2/catalogs/1/membership?kind=other&resourceId=1", 401},
		{http.MethodGet, "/api/v2/catalogs?perPage=0", 400},
		{http.MethodGet, "/api/v2/catalogs?nearby=true", 401},
		{http.MethodGet, "/api/v2/users/me/catalogs", 401},
		{http.MethodPost, "/api/v2/catalogs", 401},
		{http.MethodPut, "/api/v2/catalogs/1", 401},
		{http.MethodPut, "/api/v2/catalogs/1/pools/1", 401},
		{http.MethodDelete, "/api/v2/catalogs/1/pools/1", 401},
		{http.MethodPut, "/api/v2/catalogs/1/vouchers/1", 401},
		{http.MethodDelete, "/api/v2/catalogs/1/vouchers/1", 401},
		{http.MethodGet, "/api/v2/catalogs/1/membership?kind=pool&resourceId=1", 401},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.RemoteAddr = "198.51.100.22:1234"
		res := httptest.NewRecorder()
		a.router.ServeHTTP(res, req)
		require.Equal(t, tc.status, res.Code, "%s %s: %s", tc.method, tc.path, res.Body.String())
	}
}
