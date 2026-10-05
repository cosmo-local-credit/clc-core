package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNearbyCatalogRoutes(t *testing.T) {
	a, private := newOfferingAPI(t)
	ctx := context.Background()
	const address = "0x0000000000000000000000000000000000010101"
	_, err := a.store.Pool().Exec(ctx, "INSERT INTO users (blockchain_address,name,email,geo_lat,geo_lng) VALUES ($1,'Nearby test user','nearby-' || $1 || '@example.test',0,0)", address)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := a.store.Pool().Exec(ctx, "DELETE FROM users WHERE blockchain_address=$1", address)
		require.NoError(t, err)
	})
	ids, cleanup := seedSearchFixtures(t, a, "NearbyRoute", []searchFixture{
		{name: "Zqnearbyroute local", symbol: "ZQNR1", tags: []string{"Art"}},
		{name: "Zqnearbyroute distant", symbol: "ZQNR2", tags: []string{"Art"}},
	})
	defer cleanup()
	_, err = a.store.Pool().Exec(ctx, "UPDATE vouchers SET geo_lat=0,geo_lng=0 WHERE id=$1", ids[0])
	require.NoError(t, err)
	var postIDs []uint64
	for _, post := range []struct {
		title, status string
		lat           float64
	}{
		{"Zqnearbyroute local", "APPROVED", 0},
		{"Zqnearbyroute distant", "APPROVED", 60},
		{"Zqnearbyroute draft", "DRAFT", 0},
	} {
		var id uint64
		err := a.store.Pool().QueryRow(ctx, `INSERT INTO reports
			(title,status,author_address,geo_lat,geo_lng) VALUES ($1,$2,$3,$4,0) RETURNING id`,
			post.title, post.status, address, post.lat).Scan(&id)
		require.NoError(t, err)
		postIDs = append(postIDs, id)
		t.Cleanup(func() {
			_, err := a.store.Pool().Exec(ctx, "DELETE FROM reports WHERE id=$1", id)
			require.NoError(t, err)
		})
	}
	token := offeringToken(t, private, address, "USER")
	request := func(path string, signed bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if signed {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res := httptest.NewRecorder()
		a.router.ServeHTTP(res, req)
		return res
	}
	for _, endpoint := range []string{"vouchers", "pools", "reports"} {
		t.Run(endpoint, func(t *testing.T) {
			path := "/api/v2/" + endpoint + "?nearby=true&q=Zqnearbyroute&facets=true"
			res := request(path, false)
			require.Equal(t, http.StatusUnauthorized, res.Code, res.Body.String())
			res = request(path, true)
			require.Equal(t, http.StatusOK, res.Code, res.Body.String())
			require.Equal(t, "private, no-store", res.Header().Get("Cache-Control"))
			if endpoint == "vouchers" || endpoint == "reports" {
				require.Contains(t, res.Body.String(), "Zqnearbyroute local")
				require.NotContains(t, res.Body.String(), "Zqnearbyroute distant")
			}
			res = request("/api/v2/"+endpoint+"?nearby=invalid", true)
			require.Equal(t, http.StatusBadRequest, res.Code)
		})
	}
	res := request("/api/v2/reports?q=Zqnearbyroute&facets=true", false)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	require.Contains(t, res.Body.String(), "Zqnearbyroute local")
	require.Contains(t, res.Body.String(), "Zqnearbyroute distant")
	require.NotContains(t, res.Body.String(), "Zqnearbyroute draft")
	res = request(fmt.Sprintf("/api/v2/reports/%d", postIDs[0]), false)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	res = request(fmt.Sprintf("/api/v2/reports/%d", postIDs[2]), false)
	require.Equal(t, http.StatusNotFound, res.Code, res.Body.String())
	// Ordinary public reads and their cache remain separate.
	res = request("/api/v2/vouchers?q=Zqnearbyroute&facets=true", false)
	require.Equal(t, http.StatusOK, res.Code)
	require.Contains(t, res.Body.String(), "Zqnearbyroute distant")
	require.Contains(t, res.Header().Get("Cache-Control"), "public")
	// A profile edit changes the next request without a token refresh.
	_, err = a.store.Pool().Exec(ctx, "UPDATE users SET geo_lat=-1.29,geo_lng=36.82 WHERE blockchain_address=$1", address)
	require.NoError(t, err)
	res = request("/api/v2/vouchers?nearby=true&q=Zqnearbyroute&facets=true", true)
	require.Equal(t, http.StatusOK, res.Code)
	require.NotContains(t, res.Body.String(), "Zqnearbyroute local")
	require.Contains(t, res.Body.String(), "Zqnearbyroute distant")
	_, err = a.store.Pool().Exec(ctx, "UPDATE users SET geo_lat=NULL,geo_lng=NULL WHERE blockchain_address=$1", address)
	require.NoError(t, err)
	res = request("/api/v2/vouchers?nearby=true", true)
	require.Equal(t, http.StatusBadRequest, res.Code)
	require.Contains(t, res.Body.String(), "saved profile location")
}
