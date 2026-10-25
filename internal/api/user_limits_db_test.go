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

func TestResourceLimitsOffersApplyPerVoucherDB(t *testing.T) {
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
		_, err := a.store.Pool().Exec(ctx, `DELETE FROM offerings WHERE voucher_id IN (SELECT id FROM vouchers WHERE address=ANY($1::TEXT[]))`, vouchers)
		require.NoError(t, err)
		_, err = a.store.Pool().Exec(ctx, `DELETE FROM vouchers WHERE address=ANY($1::TEXT[])`, vouchers)
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
	require.ElementsMatch(t, []int{201, 201}, []int{responses[0].Code, responses[1].Code})
	var savedID uint64
	parent := vouchers[0]
	for i, res := range responses {
		var result struct {
			Result struct {
				Offering struct {
					ID uint64 `json:"id"`
				} `json:"offering"`
			} `json:"result"`
		}
		require.NoError(t, json.Unmarshal(res.Body.Bytes(), &result))
		if i == 0 {
			savedID = result.Result.Offering.ID
		}
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
	usage := curatedRequest(a, "GET", "/api/v2/users/me/resource-usage?voucher="+vouchers[0], token, "")
	require.Equal(t, 200, usage.Code)
	require.Equal(t, "no-store", usage.Header().Get("Cache-Control"))
	require.Contains(t, usage.Body.String(), `"offers":{"used":1,"limit":1,"unlimited":false,"canCreate":false}`)
	otherUsage := curatedRequest(a, "GET", "/api/v2/users/me/resource-usage?voucher="+vouchers[1], token, "")
	require.Equal(t, 200, otherUsage.Code)
	require.Contains(t, otherUsage.Body.String(), `"offers":{"used":1,"limit":1,"unlimited":false,"canCreate":false}`)
	require.Equal(t, 400, curatedRequest(a, "GET", "/api/v2/users/me/resource-usage?voucher=bad", token, "").Code)
	require.Equal(t, 404, curatedRequest(a, "GET", "/api/v2/users/me/resource-usage?voucher=0x0000000000000000000000000000000000000001", token, "").Code)
	stranger := offeringToken(t, key, offeringStrangerAddr, "USER")
	require.Equal(t, 403, curatedRequest(a, "GET", "/api/v2/users/me/resource-usage?voucher="+parent, stranger, "").Code)
	admin := offeringToken(t, key, offeringOperatorAddr, "SUPERADMIN")
	adminUsage := curatedRequest(a, "GET", "/api/v2/users/me/resource-usage?voucher="+parent, admin, "")
	require.Equal(t, 200, adminUsage.Code, adminUsage.Body.String())
	require.Contains(t, adminUsage.Body.String(), `"offers":{"used":1,"limit":100,"unlimited":true,"canCreate":true}`)

	// Two requests for the same voucher serialize on the user then voucher locks;
	// only one can claim the newly opened second slot.
	_, err = a.store.Pool().Exec(ctx, `UPDATE users SET max_offers=2 WHERE id=$1`, id)
	require.NoError(t, err)
	responses = make([]*httptest.ResponseRecorder, 2)
	for i := range responses {
		wg.Go(func() {
			responses[i] = curatedRequest(a, "POST", "/api/v2/vouchers/"+parent+"/offerings", token, body)
		})
	}
	wg.Wait()
	require.ElementsMatch(t, []int{201, 429}, []int{responses[0].Code, responses[1].Code})
	var transferredOfferingID uint64
	for _, response := range responses {
		if response.Code != 201 {
			continue
		}
		var result struct {
			Result struct {
				Offering struct {
					ID uint64 `json:"id"`
				} `json:"offering"`
			} `json:"result"`
		}
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
		transferredOfferingID = result.Result.Offering.ID
	}
	require.NotZero(t, transferredOfferingID)

	// Ownership transfer applies the recipient's allowance without hiding or
	// freezing retained offerings. Only another create is rejected.
	_, err = a.store.Pool().Exec(ctx, `UPDATE users SET max_offers=0 WHERE lower(blockchain_address)=lower($1)`, offeringStrangerAddr)
	require.NoError(t, err)
	_, err = a.store.Pool().Exec(ctx, `UPDATE vouchers SET owner_address=$1 WHERE lower(address)=lower($2)`, offeringStrangerAddr, parent)
	require.NoError(t, err)
	transferredUsage := curatedRequest(a, "GET", "/api/v2/users/me/resource-usage?voucher="+parent, stranger, "")
	require.Equal(t, 200, transferredUsage.Code, transferredUsage.Body.String())
	require.Contains(t, transferredUsage.Body.String(), `"offers":{"used":2,"limit":0,"unlimited":false,"canCreate":false}`)
	require.Equal(t, 429, curatedRequest(a, "POST", "/api/v2/vouchers/"+parent+"/offerings", stranger, body).Code)
	transferredPath := fmt.Sprintf("/api/v2/vouchers/%s/offerings/%d", parent, transferredOfferingID)
	require.Equal(t, 200, curatedRequest(a, "PUT", transferredPath, stranger, body).Code)
	require.Equal(t, 200, curatedRequest(a, "DELETE", transferredPath, stranger, "").Code)
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
