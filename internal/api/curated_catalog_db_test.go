package api

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

type (
	curatedTestResult struct {
		Catalog    store.CuratedCatalog         `json:"catalog"`
		Catalogs   []store.CuratedCatalog       `json:"catalogs"`
		Items      []store.CuratedCatalogMember `json:"items"`
		NextCursor *string                      `json:"nextCursor"`
		Added      bool                         `json:"added"`
		CatalogID  string                       `json:"catalogId"`
	}
)

func curatedRequest(a *API, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "198.51.100.107:1234"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, req)
	return res
}
func curatedResult(t *testing.T, res *httptest.ResponseRecorder, status int) curatedTestResult {
	t.Helper()
	require.Equal(t, status, res.Code, res.Body.String())
	var body struct {
		Result curatedTestResult `json:"result"`
	}
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &body))
	return body.Result
}
func seedCuratedAPIUser(t *testing.T, a *API, priv ed25519.PrivateKey) (uint64, string) {
	t.Helper()
	ctx := context.Background()
	var id uint64
	var address string
	err := a.store.Pool().QueryRow(ctx, `INSERT INTO users(blockchain_address,name,email,status,geo_lat,geo_lng)
        VALUES('0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),'Catalog API fixture',gen_random_uuid()::text||'@catalog.test','APPROVED',0,0)
        RETURNING id,blockchain_address`).Scan(&id, &address)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := a.store.Pool().Exec(ctx, `DELETE FROM users WHERE id=$1`, id)
		require.NoError(t, err)
	})
	token, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, UserJWTClaims{
		UserID: id, EthereumAddress: address, Role: "USER", RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))},
	}).SignedString(priv)
	require.NoError(t, err)
	return id, token
}

func TestCuratedCatalogAdministratorMayEditAndCurateButOnlyOwnerDeletesDB(t *testing.T) {
	a, priv := newOfferingAPI(t)
	ownerID, ownerToken := seedCuratedAPIUser(t, a, priv)
	adminToken := offeringToken(t, priv, offeringOperatorAddr, "ADMIN")
	superadminToken := offeringToken(t, priv, offeringStrangerAddr, "SUPERADMIN")

	created := curatedResult(t, curatedRequest(a, "POST", "/api/v2/catalogs", ownerToken,
		`{"name":"Administrative fixture","locationName":"Nairobi","geoLat":-1.286,"geoLng":36.817}`), 200).Catalog
	path := fmt.Sprintf("/api/v2/catalogs/%d", created.ID)
	ctx := context.Background()
	var voucherID uint64
	require.NoError(t, a.store.Pool().QueryRow(ctx, `
        INSERT INTO vouchers(chain_id,address,name,active)
        VALUES(100,'0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),'Administrator fixture voucher',true)
        RETURNING id`).Scan(&voucherID))
	t.Cleanup(func() {
		_, err := a.store.Pool().Exec(ctx, `DELETE FROM vouchers WHERE id=$1`, voucherID)
		require.NoError(t, err)
	})

	updatedBody := `{"name":"Edited by administrator","locationName":"Nairobi","geoLat":-1.286,"geoLng":36.817}`
	for _, token := range []string{adminToken, superadminToken} {
		updated := curatedResult(t, curatedRequest(a, "PUT", path, token, updatedBody), 200).Catalog
		require.Equal(t, "Edited by administrator", updated.Name)
		memberPath := fmt.Sprintf("%s/vouchers/%d", path, voucherID)
		require.True(t, curatedResult(t, curatedRequest(a, "PUT", memberPath, token, ""), 200).Added)
		curatedResult(t, curatedRequest(a, "DELETE", memberPath, token, ""), 200)
		curatedResult(t, curatedRequest(a, "DELETE", path, token, ""), 403)
	}

	curatedResult(t, curatedRequest(a, "DELETE", path, ownerToken, ""), 200)
	var remaining int
	require.NoError(t, a.store.Pool().QueryRow(ctx, `SELECT count(*) FROM catalogs WHERE id=$1 AND owner_user_id=$2`, created.ID, ownerID).Scan(&remaining))
	require.Zero(t, remaining)
}

func TestCuratedCatalogOwnershipConcurrentCreateAndMembershipDB(t *testing.T) {
	a, priv := newOfferingAPI(t)
	t.Cleanup(func() { a.store.Pool().Close() })
	owner, token := seedCuratedAPIUser(t, a, priv)
	_, stranger := seedCuratedAPIUser(t, a, priv)
	mine := curatedResult(t, curatedRequest(a, "GET", "/api/v2/users/me/catalogs", token, ""), 200)
	require.NotNil(t, mine.Catalogs)
	require.Empty(t, mine.Catalogs)
	body := `{"name":"Čaršija 🌱","locationName":"Sarajevo","geoLat":0,"geoLng":0,"imageUrl":"","ownerUserId":999}`
	responses := make([]*httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	for i := range responses {
		wg.Go(func() { responses[i] = curatedRequest(a, "POST", "/api/v2/catalogs", token, body) })
	}
	wg.Wait()
	var id uint64
	codes := []int{responses[0].Code, responses[1].Code}
	require.ElementsMatch(t, []int{200, 429}, codes)
	for _, res := range responses {
		if res.Code == 200 {
			saved := curatedResult(t, res, 200)
			id = saved.Catalog.ID
			require.Nil(t, saved.Catalog.ImageURL)
		}
	}
	owned := curatedResult(t, curatedRequest(a, "GET", "/api/v2/users/me/catalogs", token, ""), 200)
	require.Len(t, owned.Catalogs, 1)
	require.Equal(t, id, owned.Catalogs[0].ID)
	other := curatedResult(t, curatedRequest(a, "POST", "/api/v2/catalogs", stranger, `{"name":"Other","locationName":"Sarajevo","geoLat":0,"geoLng":0}`), 200).Catalog.ID
	require.Equal(t, other, curatedResult(t, curatedRequest(a, "GET", "/api/v2/users/me/catalogs", stranger, ""), 200).Catalogs[0].ID)
	require.Equal(t, id, curatedResult(t, curatedRequest(a, "GET", "/api/v2/users/me/catalogs", token, ""), 200).Catalogs[0].ID)
	ctx := context.Background()
	var storedOwner uint64
	require.NoError(t, a.store.Pool().QueryRow(ctx, `SELECT owner_user_id FROM catalogs WHERE id=$1`, id).Scan(&storedOwner))
	require.Equal(t, owner, storedOwner)
	path := fmt.Sprintf("/api/v2/catalogs/%d", id)
	for _, invalid := range []string{
		`{"name":"","locationName":"Sarajevo","geoLat":0,"geoLng":0}`,
		`{"name":"Name","locationName":"Sarajevo","geoLat":91,"geoLng":0}`,
		`{"name":"Name","locationName":"Sarajevo","geoLat":0,"geoLng":0,"imageUrl":"javascript:bad"}`,
		`{"name":"Name","locationName":"Sarajevo","geoLat":0}`,
	} {
		res := curatedRequest(a, "PUT", path, token, invalid)
		require.Equal(t, 400, res.Code, res.Body.String())
		require.Contains(t, res.Body.String(), `"code":"E04"`)
	}
	curatedResult(t, curatedRequest(a, "GET", path, "", ""), 200)
	curatedResult(t, curatedRequest(a, "PUT", path, stranger, body), 403)
	service := offeringServiceToken(t, priv)
	curatedResult(t, curatedRequest(a, "PUT", path, service, body), 403)
	changed := `{"name":"Renamed catalog","locationName":"Equator","geoLat":0,"geoLng":0,"imageUrl":"https://storage.example/catalog.png"}`
	saved := curatedResult(t, curatedRequest(a, "PUT", path, token, changed), 200)
	require.Equal(t, id, saved.Catalog.ID)
	require.Equal(t, "Renamed catalog", saved.Catalog.Name)
	var pool, voucher uint64
	require.NoError(t, a.store.Pool().QueryRow(ctx, `INSERT INTO pools(chain_id,address,name) VALUES(100,'0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),'Pool '||gen_random_uuid()::text) RETURNING id`).Scan(&pool))
	t.Cleanup(func() {
		_, err := a.store.Pool().Exec(ctx, `DELETE FROM pools WHERE id=$1`, pool)
		require.NoError(t, err)
	})
	require.NoError(t, a.store.Pool().QueryRow(ctx, `INSERT INTO vouchers(chain_id,address,name,active) VALUES(100,'0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),'Voucher '||gen_random_uuid()::text,true) RETURNING id`).Scan(&voucher))
	t.Cleanup(func() {
		_, err := a.store.Pool().Exec(ctx, `DELETE FROM vouchers WHERE id=$1`, voucher)
		require.NoError(t, err)
	})
	poolPath := fmt.Sprintf("%s/pools/%d", path, pool)
	voucherPath := fmt.Sprintf("%s/vouchers/%d", path, voucher)
	curatedResult(t, curatedRequest(a, "PUT", poolPath, stranger, ""), 403)
	for range 2 {
		require.True(t, curatedResult(t, curatedRequest(a, "PUT", poolPath, token, ""), 200).Added)
	}
	otherPoolPath := fmt.Sprintf("/api/v2/catalogs/%d/pools/%d", other, pool)
	require.True(t, curatedResult(t, curatedRequest(a, "PUT", otherPoolPath, stranger, ""), 200).Added)
	curatedResult(t, curatedRequest(a, "DELETE", poolPath, stranger, ""), 403)
	detail := curatedResult(t, curatedRequest(a, "GET", path, "", ""), 200)
	require.Equal(t, int64(1), detail.Catalog.PoolCount)
	require.Zero(t, detail.Catalog.VoucherCount)
	var secondPool uint64
	require.NoError(t, a.store.Pool().QueryRow(ctx, `INSERT INTO pools(chain_id,address,name) VALUES(100,'0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),'Pool '||gen_random_uuid()::text) RETURNING id`).Scan(&secondPool))
	t.Cleanup(func() {
		_, cleanupErr := a.store.Pool().Exec(ctx, `DELETE FROM pools WHERE id=$1`, secondPool)
		require.NoError(t, cleanupErr)
	})
	require.True(t, curatedResult(t, curatedRequest(a, "PUT", fmt.Sprintf("%s/pools/%d", path, secondPool), token, ""), 200).Added)
	firstMembers := curatedResult(t, curatedRequest(a, "GET", path+"/pools?perPage=1", "", ""), 200)
	require.Equal(t, pool, firstMembers.Items[0].ID)
	require.NotNil(t, firstMembers.NextCursor)
	memberCursor := url.QueryEscape(*firstMembers.NextCursor)
	secondMembers := curatedResult(t, curatedRequest(a, "GET", path+"/pools?perPage=1&cursor="+memberCursor, "", ""), 200)
	require.Equal(t, secondPool, secondMembers.Items[0].ID)
	require.Nil(t, secondMembers.NextCursor)
	curatedResult(t, curatedRequest(a, "GET", path+"/vouchers?cursor="+memberCursor, "", ""), 400)
	curatedResult(t, curatedRequest(a, "GET", fmt.Sprintf("/api/v2/catalogs/%d/pools?cursor=%s", other, memberCursor), "", ""), 400)
	require.True(t, curatedResult(t, curatedRequest(a, "GET", fmt.Sprintf("%s/membership?kind=pool&resourceId=%d", path, pool), token, ""), 200).Added)
	curatedResult(t, curatedRequest(a, "GET", fmt.Sprintf("%s/membership?kind=pool&resourceId=%d", path, pool), stranger, ""), 403)
	require.True(t, curatedResult(t, curatedRequest(a, "PUT", voucherPath, token, ""), 200).Added)
	for range 2 {
		require.False(t, curatedResult(t, curatedRequest(a, "DELETE", poolPath, token, ""), 200).Added)
	}
	require.True(t, curatedResult(t, curatedRequest(a, "GET", fmt.Sprintf("/api/v2/catalogs/%d/membership?kind=pool&resourceId=%d", other, pool), stranger, ""), 200).Added)
	members := curatedResult(t, curatedRequest(a, "GET", path+"/vouchers", "", ""), 200)
	require.Len(t, members.Items, 1)
	require.Equal(t, voucher, members.Items[0].ID)
	_, err := a.store.Pool().Exec(ctx, `UPDATE vouchers SET active=false WHERE id=$1`, voucher)
	require.NoError(t, err)
	members = curatedResult(t, curatedRequest(a, "GET", path+"/vouchers", "", ""), 200)
	require.NotNil(t, members.Items)
	require.Empty(t, members.Items)
	detail = curatedResult(t, curatedRequest(a, "GET", path, "", ""), 200)
	require.Zero(t, detail.Catalog.VoucherCount)
	curatedResult(t, curatedRequest(a, "PUT", voucherPath, token, ""), 404)
	require.False(t, curatedResult(t, curatedRequest(a, "DELETE", voucherPath, token, ""), 200).Added)
	curatedResult(t, curatedRequest(a, "PUT", path+"/pools/2147483647", token, ""), 404)
	_, err = a.store.Pool().Exec(ctx, `INSERT INTO catalog_pools(catalog_id,pool_id) VALUES($1,2147483647)`, id)
	require.Error(t, err)

	// Catalog deletion is an exact-owner operation. It releases the live quota,
	// removes only catalog-owned junction rows, and leaves referenced resources.
	curatedResult(t, curatedRequest(a, "DELETE", path, "", ""), 401)
	curatedResult(t, curatedRequest(a, "DELETE", path, stranger, ""), 403)
	curatedResult(t, curatedRequest(a, "DELETE", path, service, ""), 403)
	_, err = a.store.Pool().Exec(ctx, `UPDATE vouchers SET active=true WHERE id=$1`, voucher)
	require.NoError(t, err)
	require.True(t, curatedResult(t, curatedRequest(a, "PUT", voucherPath, token, ""), 200).Added)
	a.marketMapCache.set("catalog-delete-regression", store.MarketMapResult{})
	_, cached := a.marketMapCache.get("catalog-delete-regression")
	require.True(t, cached)
	deleted := curatedResult(t, curatedRequest(a, "DELETE", path, token, ""), 200)
	require.Equal(t, fmt.Sprint(id), deleted.CatalogID)
	_, cached = a.marketMapCache.get("catalog-delete-regression")
	require.False(t, cached)
	require.Equal(t, 404, curatedRequest(a, "GET", path, "", "").Code)
	require.Equal(t, 404, curatedRequest(a, "DELETE", path, token, "").Code)
	var catalogCount int
	require.NoError(t, a.store.Pool().QueryRow(ctx, `SELECT count(*) FROM catalogs WHERE id=$1`, id).Scan(&catalogCount))
	require.Zero(t, catalogCount)
	for _, table := range []string{"catalog_pools", "catalog_vouchers", "catalog_tags"} {
		var count int
		require.NoError(t, a.store.Pool().QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE catalog_id=$1`, id).Scan(&count))
		require.Zero(t, count, table)
	}
	for table, resourceID := range map[string]uint64{"pools": secondPool, "vouchers": voucher} {
		var count int
		require.NoError(t, a.store.Pool().QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE id=$1`, resourceID).Scan(&count))
		require.Equal(t, 1, count, table)
	}
	usage := curatedRequest(a, "GET", "/api/v2/users/me/resource-usage", token, "")
	require.Equal(t, 200, usage.Code, usage.Body.String())
	require.Contains(t, usage.Body.String(), `"catalogs":{"used":0,"limit":1,"unlimited":false,"canCreate":true}`)
	require.Equal(t, 200, curatedRequest(a, "POST", "/api/v2/catalogs", token, body).Code)
}

func TestCuratedCatalogNearbyCursorOriginDB(t *testing.T) {
	a, priv := newOfferingAPI(t)
	t.Cleanup(func() { a.store.Pool().Close() })
	owner, token := seedCuratedAPIUser(t, a, priv)
	_, otherToken := seedCuratedAPIUser(t, a, priv)
	for _, session := range []string{token, otherToken} {
		curatedResult(t, curatedRequest(a, "POST", "/api/v2/catalogs", session, `{"name":"Nearby","locationName":"Equator","geoLat":0,"geoLng":0}`), 200)
	}
	first := curatedResult(t, curatedRequest(a, "GET", "/api/v2/catalogs?nearby=true&perPage=1", token, ""), 200)
	require.Len(t, first.Catalogs, 1)
	require.NotNil(t, first.Catalogs[0].DistanceMeters)
	require.NotNil(t, first.NextCursor)
	cursor := url.QueryEscape(*first.NextCursor)
	next := curatedResult(t, curatedRequest(a, "GET", "/api/v2/catalogs?nearby=true&perPage=1&cursor="+cursor, token, ""), 200)
	require.Len(t, next.Catalogs, 1)
	require.NotEqual(t, first.Catalogs[0].ID, next.Catalogs[0].ID)
	curatedResult(t, curatedRequest(a, "GET", "/api/v2/catalogs?nearby=true&cursor="+cursor, otherToken, ""), 400)
	curatedResult(t, curatedRequest(a, "GET", "/api/v2/catalogs?cursor="+cursor, "", ""), 400)
	_, err := a.store.Pool().Exec(context.Background(), `UPDATE users SET geo_lat=1 WHERE id=$1`, owner)
	require.NoError(t, err)
	curatedResult(t, curatedRequest(a, "GET", "/api/v2/catalogs?nearby=true&cursor="+cursor, token, ""), 400)
	_, err = a.store.Pool().Exec(context.Background(), `UPDATE users SET geo_lat=NULL, geo_lng=NULL WHERE id=$1`, owner)
	require.NoError(t, err)
	res := curatedRequest(a, "GET", "/api/v2/catalogs?nearby=true", token, "")
	require.Equal(t, 400, res.Code, res.Body.String())
	require.Contains(t, res.Body.String(), `"code":"E04"`)
}
