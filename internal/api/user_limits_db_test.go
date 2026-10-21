package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResourceLimitsOffersAcrossVouchersDB(t *testing.T) {
	a, key := newOfferingAPI(t)
	t.Cleanup(func() { a.store.Pool().Close() })
	id, token := seedCuratedAPIUser(t, a, key)
	ctx := context.Background()
	var address string
	require.NoError(t, a.store.Pool().QueryRow(ctx, `SELECT blockchain_address FROM users WHERE id=$1`, id).Scan(&address))
	vouchers := make([]string, 2)
	for i := range vouchers {
		require.NoError(t, a.store.Pool().QueryRow(ctx, `INSERT INTO vouchers(chain_id,address,name,symbol,owner_address)
            VALUES(100,'0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),'Quota voucher',gen_random_uuid()::text,$1) RETURNING address`, address).Scan(&vouchers[i]))
	}
	t.Cleanup(func() {
		_, err := a.store.Pool().Exec(ctx, `DELETE FROM offerings WHERE voucher_id IN (SELECT id FROM vouchers WHERE owner_address=$1)`, address)
		require.NoError(t, err)
		_, err = a.store.Pool().Exec(ctx, `DELETE FROM vouchers WHERE owner_address=$1`, address)
		require.NoError(t, err)
	})
	_, err := a.store.Pool().Exec(ctx, `UPDATE users SET max_offers=1 WHERE id=$1`, id)
	require.NoError(t, err)
	body := `{"commodityName":"Apples","description":"Fresh apples","unit":"kg","price":"2"}`
	responses := make([]*httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	for i := range responses {
		wg.Go(func() {
			responses[i] = curatedRequest(a, "POST", "/api/v2/vouchers/"+vouchers[i]+"/offerings", token, body)
		})
	}
	wg.Wait()
	require.ElementsMatch(t, []int{201, 429}, []int{responses[0].Code, responses[1].Code})
	var savedID uint64
	var parent string
	for i, res := range responses {
		if res.Code == 429 {
			require.Contains(t, res.Body.String(), `"code":"E11"`)
			require.Contains(t, res.Body.String(), `"resource":"offers"`)
			continue
		}
		var result struct {
			Result struct {
				Offering struct {
					ID uint64 `json:"id"`
				} `json:"offering"`
			} `json:"result"`
		}
		require.NoError(t, json.Unmarshal(res.Body.Bytes(), &result))
		savedID = result.Result.Offering.ID
		parent = vouchers[i]
	}
	require.NotZero(t, savedID)
	path := fmt.Sprintf("/api/v2/vouchers/%s/offerings/%d", parent, savedID)
	// Lowering the limit must not prevent editing or unlisting existing content.
	_, err = a.store.Pool().Exec(ctx, `UPDATE users SET max_offers=0 WHERE id=$1`, id)
	require.NoError(t, err)
	require.Equal(t, 200, curatedRequest(a, "PUT", path, token, body).Code)
	require.Equal(t, 200, curatedRequest(a, "DELETE", path, token, "").Code)
	require.Equal(t, 429, curatedRequest(a, "POST", "/api/v2/vouchers/"+parent+"/offerings", token, body).Code)
	_, err = a.store.Pool().Exec(ctx, `UPDATE users SET max_offers=1 WHERE id=$1`, id)
	require.NoError(t, err)
	require.Equal(t, 201, curatedRequest(a, "POST", "/api/v2/vouchers/"+parent+"/offerings", token, body).Code)
	usage := curatedRequest(a, "GET", "/api/v2/users/me/resource-usage", token, "")
	require.Equal(t, 200, usage.Code)
	require.Equal(t, "no-store", usage.Header().Get("Cache-Control"))
	require.Contains(t, usage.Body.String(), `"offers":{"used":1,"limit":1,"unlimited":false,"canCreate":false}`)
}

func TestResourceLimitsCatalogIncreaseAndOmittedFieldsDB(t *testing.T) {
	a, key := newOfferingAPI(t)
	t.Cleanup(func() { a.store.Pool().Close() })
	id, token := seedCuratedAPIUser(t, a, key)
	var address string
	require.NoError(t, a.store.Pool().QueryRow(context.Background(), `SELECT blockchain_address FROM users WHERE id=$1`, id).Scan(&address))
	body := `{"name":"Catalog","locationName":"Sarajevo","geoLat":0,"geoLng":0}`
	require.Equal(t, 200, curatedRequest(a, "POST", "/api/v2/catalogs", token, body).Code)
	require.Equal(t, 429, curatedRequest(a, "POST", "/api/v2/catalogs", token, body).Code)
	admin := offeringToken(t, key, offeringStrangerAddr, "SUPERADMIN")
	path := "/api/v2/admin/users/" + address + "/limits"
	require.Equal(t, 403, curatedRequest(a, "PUT", path, token, `{"maxCatalogs":2}`).Code)
	require.Equal(t, 200, curatedRequest(a, "PUT", path, admin, `{"maxCatalogs":2}`).Code)
	require.Equal(t, 200, curatedRequest(a, "POST", "/api/v2/catalogs", token, body).Code)
	require.Equal(t, 200, curatedRequest(a, "PUT", path, admin, `{"maxTokens":4,"maxPools":5}`).Code)
	var catalogs, offers int
	require.NoError(t, a.store.Pool().QueryRow(context.Background(), `SELECT max_catalogs,max_offers FROM users WHERE id=$1`, id).Scan(&catalogs, &offers))
	require.Equal(t, 2, catalogs)
	require.Equal(t, 3, offers)
	require.Equal(t, 400, curatedRequest(a, "PUT", path, admin, `{"maxOffers":-1}`).Code)
	mine := curatedResult(t, curatedRequest(a, "GET", "/api/v2/users/me/catalogs", token, ""), 200)
	require.Len(t, mine.Catalogs, 2)
}
