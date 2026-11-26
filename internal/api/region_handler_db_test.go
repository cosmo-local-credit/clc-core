package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/stretchr/testify/require"
)

func TestRegionalAuthorityDB(t *testing.T) {
	a, _ := newAdminAPI(t)
	ctx := context.Background()
	cleanupRegions := func() {
		if _, err := a.store.Pool().Exec(ctx, `
			DELETE FROM marketplace_region_events WHERE region_id IN (
				SELECT id FROM marketplace_regions WHERE name LIKE 'zq-regional-%'
			)
		`); err != nil {
			t.Errorf("delete regional history: %v", err)
		}
		if _, err := a.store.Pool().Exec(ctx, `DELETE FROM marketplace_regions WHERE name LIKE 'zq-regional-%'`); err != nil {
			t.Errorf("delete fixture regions: %v", err)
		}
		cleanupRoleTestUsers(t, a)
	}
	cleanupRegions()
	t.Cleanup(cleanupRegions)

	super := seedRoleTestUser(t, a, "0x6200000000000000000000000000000000000001", "SUPERADMIN", "APPROVED")
	global := seedRoleTestUser(t, a, "0x6200000000000000000000000000000000000002", "ADMIN", "APPROVED")
	target := seedRoleTestUser(t, a, "0x6200000000000000000000000000000000000003", "USER", "APPROVED")
	pending := seedRoleTestUser(t, a, "0x6200000000000000000000000000000000000004", "USER", "PENDING")
	convert := seedRoleTestUser(t, a, "0x6200000000000000000000000000000000000005", "ADMIN", "APPROVED")
	signed := func(user store.User) string {
		t.Helper()
		token, err := a.generateUserToken(user)
		require.NoError(t, err)
		return token
	}
	globalToken, superToken, userToken := signed(global), signed(super), signed(target)
	convertToken := signed(convert)

	type result struct {
		Region  store.MarketplaceRegion   `json:"region"`
		Regions []store.MarketplaceRegion `json:"regions"`
	}
	adminRegions := "/api/v2/admin/regions"
	myRegions := "/api/v2/regions/me"
	request := func(token, method, path, body string, status int) result {
		t.Helper()
		response := roleHTTP(t, a, token, method, path, body)
		require.Equal(t, status, response.Code, "%s %s %s", method, path, response.Body.String())
		if status >= 400 {
			return result{}
		}
		require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"))
		var envelope struct {
			Result result `json:"result"`
		}
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
		return envelope.Result
	}
	area := func(name string, west, south, east, north float64) string {
		return fmt.Sprintf(`{"name":"zq-regional-%s","active":true,"boundary":{"type":"Polygon","coordinates":[[[%[2]f,%[3]f],[%[4]f,%[3]f],[%[4]f,%[5]f],[%[2]f,%[5]f],[%[2]f,%[3]f]]]}}`, name, west, south, east, north)
	}
	require.Empty(t, request(userToken, http.MethodGet, myRegions, "", http.StatusOK).Regions)
	request(userToken, http.MethodPost, adminRegions, area("user", 66, 24, 67, 26), http.StatusForbidden)
	request(globalToken, http.MethodPost, adminRegions, `{"name":"zq-regional-unwired","active":true,"poolApproval":true}`, http.StatusConflict)
	request(globalToken, http.MethodPost, adminRegions, `{"name":"zq-regional-unknown","active":true,"placementRestricted":true}`, http.StatusBadRequest)
	request(globalToken, http.MethodPost, adminRegions,
		`{"name":"zq-regional-null","active":true,"boundary":{"type":"Polygon","coordinates":[[[null,0],[1,0],[1,1],[0,1],[0,0]]]}}`, http.StatusBadRequest)
	request(globalToken, http.MethodPost, adminRegions,
		`{"name":"zq-regional-bow-tie","active":true,"boundary":{"type":"Polygon","coordinates":[[[0,0],[1,1],[0,1],[1,0],[0,0]]]}}`, http.StatusBadRequest)
	request(globalToken, http.MethodPost, adminRegions,
		`{"name":"zq-regional-unsplit","active":true,"boundary":{"type":"Polygon","coordinates":[[[179,0],[-179,0],[-179,1],[179,1],[179,0]]]}}`, http.StatusBadRequest)

	first := request(globalToken, http.MethodPost, adminRegions, area("first", 66, 24, 67, 26), http.StatusCreated).Region
	require.NotEmpty(t, first.ID)
	require.Empty(t, first.AssignedAdmins)
	require.False(t, first.PoolApproval)
	firstPath := adminRegions + "/" + first.ID
	request(globalToken, http.MethodPost, adminRegions, area("overlap", 66.5, 25, 67.5, 26), http.StatusConflict)
	request(superToken, http.MethodPost, adminRegions, area("contained", 66.2, 24.5, 66.5, 25.9), http.StatusConflict)
	second := request(superToken, http.MethodPost, adminRegions, area("adjacent", 67, 24.5, 67.5, 25.9), http.StatusCreated).Region
	secondPath := adminRegions + "/" + second.ID

	// Simultaneous writers must compare against the winner's committed
	// geometry under the advisory transaction lock.
	codes := make(chan int, 2)
	for _, name := range []string{"concurrent-a", "concurrent-b"} {
		go func() {
			response := roleHTTP(t, a, globalToken, http.MethodPost, adminRegions, area(name, 71.1, 27.2, 71.3, 27.5))
			codes <- response.Code
		}()
	}
	require.ElementsMatch(t, []int{http.StatusCreated, http.StatusConflict}, []int{<-codes, <-codes})
	country := request(globalToken, http.MethodPost, adminRegions,
		`{"name":"zq-regional-bhutan","active":true,"countryCode":"BTN"}`, http.StatusCreated).Region
	require.NotNil(t, country.CountryCode)
	require.Equal(t, "BTN", *country.CountryCode)
	require.NotEmpty(t, country.Boundary)
	request(globalToken, http.MethodPost, adminRegions,
		`{"name":"zq-regional-fake-bhutan","active":true,"countryCode":"BTN","boundary":{"type":"Polygon","coordinates":[]}}`, http.StatusBadRequest)

	tx, err := a.store.Pool().Begin(ctx)
	require.NoError(t, err)
	matches, lookupErr := a.store.RegionsCoveringPoint(ctx, tx, 25.4321, 67)
	require.NoError(t, tx.Rollback(ctx))
	require.NoError(t, lookupErr)
	require.Equal(t, []store.RegionReference{first.RegionReference, second.RegionReference}, matches)

	assignPath := func(regionPath, address string) string { return regionPath + "/assignments/" + address }
	require.Len(t, request(globalToken, http.MethodPut, assignPath(firstPath, target.EthereumAddress), `{"assigned":true}`, http.StatusOK).Region.AssignedAdmins, 1)
	request(globalToken, http.MethodPut, assignPath(firstPath, pending.EthereumAddress), `{"assigned":true}`, http.StatusConflict)
	request(globalToken, http.MethodPut, assignPath(firstPath, super.EthereumAddress), `{"assigned":true}`, http.StatusConflict)
	request(globalToken, http.MethodPut, assignPath(firstPath, convert.EthereumAddress), `{"assigned":true,"confirmGlobalConversion":true}`, http.StatusConflict)
	request(superToken, http.MethodPut, assignPath(firstPath, convert.EthereumAddress), `{"assigned":true}`, http.StatusConflict)
	request(superToken, http.MethodPut, assignPath(firstPath, convert.EthereumAddress), `{"assigned":true,"confirmGlobalConversion":true}`, http.StatusOK)
	request(convertToken, http.MethodGet, adminRegions, "", http.StatusForbidden)

	request(superToken, http.MethodPut, "/api/v2/admin/users/"+target.EthereumAddress+"/role", `{"role":"ADMIN"}`, http.StatusConflict)
	live, err := a.store.GetUserAccessByID(ctx, target.ID)
	require.NoError(t, err)
	require.Equal(t, string(store.UserRoleRegionalAdmin), live.Role)
	refreshed := roleHTTP(t, a, userToken, http.MethodGet, "/auth/session", "")
	require.Equal(t, http.StatusOK, refreshed.Code, refreshed.Body.String())
	require.Contains(t, refreshed.Body.String(), `"role":"REGIONAL_ADMIN"`)
	var regionalToken string
	for _, cookie := range refreshed.Result().Cookies() {
		if cookie.Name == userAuthCookieName {
			regionalToken = cookie.Value
		}
	}
	require.NotEmpty(t, regionalToken)
	me := request(regionalToken, http.MethodGet, myRegions, "", http.StatusOK).Regions
	require.Len(t, me, 1)
	require.Equal(t, first.ID, me[0].ID)
	require.Len(t, me[0].AssignedAdmins, 1)
	require.Equal(t, target.EthereumAddress, me[0].AssignedAdmins[0].Address)
	request(regionalToken, http.MethodGet, adminRegions, "", http.StatusForbidden)
	request(regionalToken, http.MethodGet, "/api/v2/admin/users?perPage=1", "", http.StatusForbidden)
	request(globalToken, http.MethodPut, assignPath(secondPath, target.EthereumAddress), `{"assigned":true}`, http.StatusOK)
	require.Len(t, request(regionalToken, http.MethodGet, myRegions, "", http.StatusOK).Regions, 2)

	request(globalToken, http.MethodPut, assignPath(firstPath, target.EthereumAddress), `{"assigned":false}`, http.StatusOK)
	require.Len(t, request(regionalToken, http.MethodGet, myRegions, "", http.StatusOK).Regions, 1)
	request(globalToken, http.MethodPut, secondPath,
		fmt.Sprintf(`{"name":"zq-regional-adjacent","active":false,"revision":%d,"boundary":%s}`, second.Revision+1, second.Boundary), http.StatusConflict)
	deactivated := request(globalToken, http.MethodPut, secondPath,
		fmt.Sprintf(`{"name":"zq-regional-adjacent","active":false,"revision":%d,"boundary":%s}`, second.Revision, second.Boundary), http.StatusOK).Region
	require.Empty(t, deactivated.AssignedAdmins)
	require.Empty(t, request(regionalToken, http.MethodGet, myRegions, "", http.StatusOK).Regions)
	live, err = a.store.GetUserAccessByID(ctx, target.ID)
	require.NoError(t, err)
	require.Equal(t, string(store.UserRoleUser), live.Role)
	request(globalToken, http.MethodPut, assignPath(secondPath, target.EthereumAddress), `{"assigned":true}`, http.StatusConflict)

	var historyCount int
	require.NoError(t, a.store.Pool().QueryRow(ctx, `
		SELECT count(*) FROM marketplace_region_events
		WHERE region_id::TEXT = $1 AND event = 'REMOVED'
	`, second.ID).Scan(&historyCount))
	require.Equal(t, 1, historyCount)
}
