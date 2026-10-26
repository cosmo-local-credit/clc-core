package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/stretchr/testify/require"
)

type (
	wantTestResult struct {
		Want      store.Want                 `json:"want"`
		Wants     []store.Want               `json:"wants"`
		Vouchers  []store.WantCreatorVoucher `json:"vouchers"`
		Pools     []store.WantCreatorPool    `json:"pools"`
		Results   []store.MarketplaceResult  `json:"results"`
		First     uint64                     `json:"first"`
		Last      uint64                     `json:"last"`
		TagFacets []store.TagFacet           `json:"tagFacets"`
	}
)

var (
	wantPrivateResponseFields = []string{"ownerUserId", "ownerName", "ownerUsername", "effectiveImageUrl"}
)

func wantResult(t *testing.T, res *httptest.ResponseRecorder, status int) wantTestResult {
	t.Helper()
	require.Equal(t, status, res.Code, res.Body.String())
	var body struct {
		Result wantTestResult `json:"result"`
	}
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &body))
	return body.Result
}

func requireWantPrivacyBoundary(t *testing.T, res *httptest.ResponseRecorder, resultKey string) {
	t.Helper()
	var body struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &body))
	raw, ok := body.Result[resultKey]
	require.True(t, ok, "missing result.%s in %s", resultKey, res.Body.String())
	if len(raw) > 0 && raw[0] == '[' {
		var rows []map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &rows))
		for _, row := range rows {
			for _, field := range wantPrivateResponseFields {
				require.NotContains(t, row, field)
			}
		}
		return
	}
	var row map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &row))
	for _, field := range wantPrivateResponseFields {
		require.NotContains(t, row, field)
	}
}

func requireNoWantContacts(t *testing.T, res *httptest.ResponseRecorder, resultKey string) {
	t.Helper()
	var body struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &body))
	raw := body.Result[resultKey]
	rows := []map[string]json.RawMessage{}
	if len(raw) > 0 && raw[0] == '[' {
		require.NoError(t, json.Unmarshal(raw, &rows))
	} else {
		var row map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &row))
		rows = append(rows, row)
	}
	for _, row := range rows {
		for _, field := range []string{"contactPermission", "contactText", "email", "phone"} {
			require.NotContains(t, row, field)
		}
	}
}

func requireOnlyJSONFields(t *testing.T, res *httptest.ResponseRecorder, resultKey string, allowed ...string) {
	t.Helper()
	var body struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &body))
	var rows []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body.Result[resultKey], &rows))
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, field := range allowed {
		allowedSet[field] = struct{}{}
	}
	for _, row := range rows {
		for field := range row {
			_, ok := allowedSet[field]
			require.True(t, ok, "unexpected field %q in result.%s", field, resultKey)
		}
	}
}

func wantBody(t *testing.T, name string, starts time.Time, expires *time.Time, contactText string, shareEmail, sharePhone bool) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"name": name, "description": "Need help with a bicycle wheel", "locationName": "Nairobi",
		"geoLat": -1.286, "geoLng": 36.817, "startsAt": starts.UTC(), "expiresAt": expires,
		"contactText": contactText, "shareEmail": shareEmail, "sharePhone": sharePhone,
		"markerIcon": "question", "markerColor": "orange", "tags": []string{"Bicycle Help"},
	})
	require.NoError(t, err)
	return string(body)
}

func wantBodyWithImage(t *testing.T, name string, starts time.Time, imageURL string) string {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(wantBody(t, name, starts, nil, "Meet by the market", true, false)), &body))
	body["imageUrl"] = imageURL
	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	return string(encoded)
}

func TestWantLifecyclePrivacyQuotaDiscoveryAndPoolDerivationDB(t *testing.T) {
	a, priv := newOfferingAPI(t)
	t.Cleanup(func() { a.store.Pool().Close() })
	userID, token := seedCuratedAPIUser(t, a, priv)
	ctx := context.Background()
	var address string
	privateName := fmt.Sprintf("Private Want Creator %d", userID)
	privateUsername := fmt.Sprintf("private-want-%d", userID)
	require.NoError(t, a.store.Pool().QueryRow(ctx, `UPDATE users SET name=$2,username=$3,email='amina@example.test',phone='+254700000000',avatar_url='https://storage.example/avatar.webp',max_wants=3 WHERE id=$1 RETURNING blockchain_address`, userID, privateName, privateUsername).Scan(&address))
	now := time.Now().UTC().Truncate(time.Second)
	createResponse := curatedRequest(a, http.MethodPost, "/api/v2/users/me/wants", token,
		wantBody(t, "Bicycle repair", now.Add(-time.Hour), nil, "Meet by the market", true, false))
	active := wantResult(t, createResponse, http.StatusCreated).Want
	requireWantPrivacyBoundary(t, createResponse, "want")
	require.Equal(t, "active", active.State)
	require.Equal(t, []string{"Bicycle Help"}, active.Tags)
	require.Equal(t, 1, active.ContactPermission)

	publicResponse := curatedRequest(a, http.MethodGet, fmt.Sprintf("/api/v2/wants/%d", active.ID), "", "")
	public := wantResult(t, publicResponse, http.StatusOK).Want
	requireWantPrivacyBoundary(t, publicResponse, "want")
	requireNoWantContacts(t, publicResponse, "want")
	require.Zero(t, public.ContactPermission)
	require.Nil(t, public.Email)
	require.Nil(t, public.Phone)
	require.Nil(t, public.ContactText)
	require.Nil(t, public.ImageURL)

	contactResponse := curatedRequest(a, http.MethodGet, fmt.Sprintf("/api/v2/wants/%d/contact", active.ID), token, "")
	require.Equal(t, http.StatusOK, contactResponse.Code, contactResponse.Body.String())
	require.Equal(t, "private, no-store", contactResponse.Header().Get("Cache-Control"))
	var contactEnvelope struct {
		Result struct {
			Contact wantContactResponse `json:"contact"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(contactResponse.Body.Bytes(), &contactEnvelope))
	require.Equal(t, "Meet by the market", *contactEnvelope.Result.Contact.Text)
	require.Equal(t, "amina@example.test", *contactEnvelope.Result.Contact.Email)
	require.Nil(t, contactEnvelope.Result.Contact.Phone)

	tx, err := a.store.Pool().Begin(ctx)
	require.NoError(t, err)
	filter, err := store.NewCatalogFilter("wheel", []string{"Bicycle Help"}, store.TagMatchAny)
	require.NoError(t, err)
	_, err = a.store.ListWants(ctx, tx, filter, nil, store.CatalogFirstPage, 0, 20)
	require.NoError(t, err)
	_, err = a.store.ListWantTagFacets(ctx, tx, filter, nil)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback(ctx))

	listResponse := curatedRequest(a, http.MethodGet, "/api/v2/wants?q=wheel&tags=Bicycle%20Help&facets=true", "", "")
	list := wantResult(t, listResponse, http.StatusOK)
	requireWantPrivacyBoundary(t, listResponse, "wants")
	requireNoWantContacts(t, listResponse, "wants")
	require.Len(t, list.Wants, 1)
	require.Zero(t, list.Wants[0].ContactPermission)
	require.Nil(t, list.Wants[0].ContactText)
	require.Nil(t, list.Wants[0].Email)
	require.Nil(t, list.Wants[0].Phone)
	require.NotEmpty(t, list.TagFacets)

	for _, query := range []string{privateName, privateUsername} {
		encoded := url.QueryEscape(query)
		identityListResponse := curatedRequest(a, http.MethodGet, "/api/v2/wants?q="+encoded, "", "")
		identityList := wantResult(t, identityListResponse, http.StatusOK)
		require.Empty(t, identityList.Wants)
		requireWantPrivacyBoundary(t, identityListResponse, "wants")
		marketplaceResponse := curatedRequest(a, http.MethodGet, "/api/v2/marketplace?q="+encoded+"&kinds=want", "", "")
		marketplace := wantResult(t, marketplaceResponse, http.StatusOK)
		require.Empty(t, marketplace.Results)
	}

	_, err = a.store.Pool().Exec(ctx, `UPDATE users SET email='new@example.test',avatar_url='https://storage.example/new.webp' WHERE id=$1`, userID)
	require.NoError(t, err)
	public = wantResult(t, curatedRequest(a, http.MethodGet, fmt.Sprintf("/api/v2/wants/%d", active.ID), "", ""), http.StatusOK).Want
	require.Nil(t, public.Email)
	require.Nil(t, public.ImageURL)
	contactResponse = curatedRequest(a, http.MethodGet, fmt.Sprintf("/api/v2/wants/%d/contact", active.ID), token, "")
	require.NoError(t, json.Unmarshal(contactResponse.Body.Bytes(), &contactEnvelope))
	require.Equal(t, "new@example.test", *contactEnvelope.Result.Contact.Email)

	const explicitWantImage = "https://storage.example/want.webp"
	updateResponse := curatedRequest(a, http.MethodPut, fmt.Sprintf("/api/v2/users/me/wants/%d", active.ID), token,
		wantBodyWithImage(t, "Bicycle repair", now.Add(-time.Hour), explicitWantImage))
	public = wantResult(t, updateResponse, http.StatusOK).Want
	requireWantPrivacyBoundary(t, updateResponse, "want")
	require.Equal(t, explicitWantImage, *public.ImageURL)
	_, err = a.store.Pool().Exec(ctx, `UPDATE users SET avatar_url='https://storage.example/later.webp' WHERE id=$1`, userID)
	require.NoError(t, err)
	public = wantResult(t, curatedRequest(a, http.MethodGet, fmt.Sprintf("/api/v2/wants/%d", active.ID), "", ""), http.StatusOK).Want
	require.Equal(t, explicitWantImage, *public.ImageURL)

	scheduled := wantResult(t, curatedRequest(a, http.MethodPost, "/api/v2/users/me/wants", token,
		wantBody(t, "Scheduled request", now.Add(time.Hour), nil, "Text me later", false, true)), http.StatusCreated).Want
	require.Equal(t, "scheduled", scheduled.State)
	wantResult(t, curatedRequest(a, http.MethodGet, fmt.Sprintf("/api/v2/wants/%d", scheduled.ID), "", ""), http.StatusNotFound)
	ownedResponse := curatedRequest(a, http.MethodGet, "/api/v2/users/me/wants?perPage=1", token, "")
	owned := wantResult(t, ownedResponse, http.StatusOK)
	requireWantPrivacyBoundary(t, ownedResponse, "wants")
	require.Len(t, owned.Wants, 1)
	require.Equal(t, scheduled.ID, owned.Wants[0].ID)
	require.Equal(t, scheduled.ID, owned.First)
	require.NotZero(t, owned.Last)

	third := wantResult(t, curatedRequest(a, http.MethodPost, "/api/v2/users/me/wants", token,
		wantBody(t, "Third request", now.Add(-time.Hour), nil, "Call first", false, true)), http.StatusCreated).Want
	limit := curatedRequest(a, http.MethodPost, "/api/v2/users/me/wants", token,
		wantBody(t, "Over limit", now.Add(-time.Hour), nil, "Email first", true, false))
	require.Equal(t, http.StatusTooManyRequests, limit.Code, limit.Body.String())

	closeResponse := curatedRequest(a, http.MethodPost, fmt.Sprintf("/api/v2/users/me/wants/%d/close", active.ID), token, "")
	wantResult(t, closeResponse, http.StatusOK)
	fourth := wantResult(t, curatedRequest(a, http.MethodPost, "/api/v2/users/me/wants", token,
		wantBody(t, "Freed slot", now.Add(-time.Hour), nil, "Email first", true, false)), http.StatusCreated).Want
	require.NotZero(t, fourth.ID)
	reopen := curatedRequest(a, http.MethodPost, fmt.Sprintf("/api/v2/users/me/wants/%d/reopen", active.ID), token,
		wantBody(t, "Bicycle repair", now.Add(-time.Hour), nil, "Meet by the market", true, false))
	require.Equal(t, http.StatusTooManyRequests, reopen.Code, reopen.Body.String())
	wantResult(t, curatedRequest(a, http.MethodPost, fmt.Sprintf("/api/v2/users/me/wants/%d/close", third.ID), token, ""), http.StatusOK)
	pastExpiry := now.Add(-time.Minute)
	expiredReopen := curatedRequest(a, http.MethodPost, fmt.Sprintf("/api/v2/users/me/wants/%d/reopen", active.ID), token,
		wantBody(t, "Bicycle repair", now.Add(-time.Hour), &pastExpiry, "Meet by the market", true, false))
	require.Equal(t, http.StatusBadRequest, expiredReopen.Code, expiredReopen.Body.String())
	reopenResponse := curatedRequest(a, http.MethodPost, fmt.Sprintf("/api/v2/users/me/wants/%d/reopen", active.ID), token,
		wantBody(t, "Bicycle repair", now.Add(-time.Hour), nil, "Meet by the market", true, false))
	reopened := wantResult(t, reopenResponse, http.StatusOK).Want
	requireWantPrivacyBoundary(t, reopenResponse, "want")
	require.Equal(t, store.WantStatusOpen, reopened.Status)

	var voucherID, secondVoucherID, poolID, unrelatedPoolID uint64
	var poolAddress string
	require.NoError(t, a.store.Pool().QueryRow(ctx, `INSERT INTO vouchers(chain_id,address,name,symbol,active,owner_address)
		VALUES(100,'0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),'Want owner voucher '||gen_random_uuid()::text,'WNT'||substr(md5(random()::text),1,5),true,$1) RETURNING id`, address).Scan(&voucherID))
	require.NoError(t, a.store.Pool().QueryRow(ctx, `INSERT INTO pools(chain_id,address,name)
		VALUES(100,'0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),'Want pool '||gen_random_uuid()::text) RETURNING id,address`).Scan(&poolID, &poolAddress))
	_, err = a.store.Pool().Exec(ctx, `INSERT INTO pool_tokens(pool_id,voucher_id,is_allowed) VALUES($1,$2,true)`, poolID, voucherID)
	require.NoError(t, err)
	require.NoError(t, a.store.Pool().QueryRow(ctx, `INSERT INTO vouchers(chain_id,address,name,symbol,active,owner_address)
		VALUES(100,'0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),'Second Want owner voucher '||gen_random_uuid()::text,'WN2'||substr(md5(random()::text),1,5),true,$1) RETURNING id`, address).Scan(&secondVoucherID))
	_, err = a.store.Pool().Exec(ctx, `INSERT INTO pool_tokens(pool_id,voucher_id,is_allowed) VALUES($1,$2,true)`, poolID, secondVoucherID)
	require.NoError(t, err)
	require.NoError(t, a.store.Pool().QueryRow(ctx, `INSERT INTO pools(chain_id,address,name,owner_address)
		VALUES(100,'0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),'Unrelated owned pool '||gen_random_uuid()::text,$1) RETURNING id`, address).Scan(&unrelatedPoolID))
	t.Cleanup(func() {
		_, cleanupErr := a.store.Pool().Exec(context.Background(), `DELETE FROM pools WHERE id=ANY($1::BIGINT[])`, []uint64{poolID, unrelatedPoolID})
		require.NoError(t, cleanupErr)
		_, cleanupErr = a.store.Pool().Exec(context.Background(), `DELETE FROM vouchers WHERE id=ANY($1::BIGINT[])`, []uint64{voucherID, secondVoucherID})
		require.NoError(t, cleanupErr)
	})

	tx, err = a.store.Pool().Begin(ctx)
	require.NoError(t, err)
	_, err = a.store.ListPoolWants(ctx, tx, poolAddress, store.CatalogFirstPage, 0, 20)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback(ctx))

	poolWantsResponse := curatedRequest(a, http.MethodGet, fmt.Sprintf("/api/v2/pools/%s/wants", poolAddress), "", "")
	poolWants := wantResult(t, poolWantsResponse, http.StatusOK)
	requireWantPrivacyBoundary(t, poolWantsResponse, "wants")
	requireNoWantContacts(t, poolWantsResponse, "wants")
	require.NotEmpty(t, poolWants.Wants)
	relatedVouchersResponse := curatedRequest(a, http.MethodGet, fmt.Sprintf("/api/v2/wants/%d/vouchers?perPage=10", active.ID), "", "")
	relatedVouchers := wantResult(t, relatedVouchersResponse, http.StatusOK)
	require.Len(t, relatedVouchers.Vouchers, 2)
	require.Nil(t, relatedVouchers.Vouchers[0].IconURL)
	requireOnlyJSONFields(t, relatedVouchersResponse, "vouchers", "id", "chainId", "address", "name", "symbol", "decimals", "iconUrl", "active")
	relatedPoolsResponse := curatedRequest(a, http.MethodGet, fmt.Sprintf("/api/v2/wants/%d/pools?perPage=10", active.ID), "", "")
	relatedPools := wantResult(t, relatedPoolsResponse, http.StatusOK)
	require.Len(t, relatedPools.Pools, 1)
	require.Equal(t, poolID, relatedPools.Pools[0].ID)
	requireOnlyJSONFields(t, relatedPoolsResponse, "pools", "id", "chainId", "address", "name", "symbol", "locationName", "imageUrl", "voucherCount", "tags")
	creditURL := "/api/v2/wants?pools=" + url.QueryEscape("100:"+poolAddress)
	creditWants := wantResult(t, curatedRequest(a, http.MethodGet, creditURL, "", ""), http.StatusOK)
	require.NotEmpty(t, creditWants.Wants)
	_, err = a.store.Pool().Exec(ctx, `UPDATE pool_tokens SET is_allowed=false WHERE pool_id=$1 AND voucher_id=$2`, poolID, voucherID)
	require.NoError(t, err)
	_, err = a.store.Pool().Exec(ctx, `UPDATE pool_tokens SET is_allowed=false WHERE pool_id=$1 AND voucher_id=$2`, poolID, secondVoucherID)
	require.NoError(t, err)
	poolWants = wantResult(t, curatedRequest(a, http.MethodGet, fmt.Sprintf("/api/v2/pools/%s/wants", poolAddress), "", ""), http.StatusOK)
	require.Empty(t, poolWants.Wants)
	relatedPools = wantResult(t, curatedRequest(a, http.MethodGet, fmt.Sprintf("/api/v2/wants/%d/pools", active.ID), "", ""), http.StatusOK)
	require.Empty(t, relatedPools.Pools)
	_, err = a.store.Pool().Exec(ctx, `UPDATE pool_tokens SET is_allowed=true WHERE pool_id=$1 AND voucher_id=$2`, poolID, voucherID)
	require.NoError(t, err)
	_, err = a.store.Pool().Exec(ctx, `UPDATE vouchers SET owner_address='0x0000000000000000000000000000000000000001' WHERE id=$1`, voucherID)
	require.NoError(t, err)
	poolWants = wantResult(t, curatedRequest(a, http.MethodGet, fmt.Sprintf("/api/v2/pools/%s/wants", poolAddress), "", ""), http.StatusOK)
	require.Empty(t, poolWants.Wants)
	relatedVouchers = wantResult(t, curatedRequest(a, http.MethodGet, fmt.Sprintf("/api/v2/wants/%d/vouchers", active.ID), "", ""), http.StatusOK)
	require.Len(t, relatedVouchers.Vouchers, 1)
	_, err = a.store.Pool().Exec(ctx, `UPDATE vouchers SET owner_address=$1 WHERE id=$2`, address, voucherID)
	require.NoError(t, err)
	_, err = a.store.Pool().Exec(ctx, `UPDATE vouchers SET active=false WHERE id=$1`, voucherID)
	require.NoError(t, err)
	poolWants = wantResult(t, curatedRequest(a, http.MethodGet, fmt.Sprintf("/api/v2/pools/%s/wants", poolAddress), "", ""), http.StatusOK)
	require.Empty(t, poolWants.Wants)

	_, stranger := seedCuratedAPIUser(t, a, priv)
	for _, path := range []string{
		fmt.Sprintf("/api/v2/users/me/wants/%d/close", active.ID),
		fmt.Sprintf("/api/v2/users/me/wants/%d", active.ID),
	} {
		method := http.MethodPost
		if path == fmt.Sprintf("/api/v2/users/me/wants/%d", active.ID) {
			method = http.MethodDelete
		}
		require.Equal(t, http.StatusForbidden, curatedRequest(a, method, path, stranger, "").Code)
	}
	wantResult(t, curatedRequest(a, http.MethodDelete, fmt.Sprintf("/api/v2/users/me/wants/%d", active.ID), token, ""), http.StatusOK)
	wantResult(t, curatedRequest(a, http.MethodGet, fmt.Sprintf("/api/v2/wants/%d", active.ID), "", ""), http.StatusNotFound)
	wantResult(t, curatedRequest(a, http.MethodGet, fmt.Sprintf("/api/v2/wants/%d/vouchers", active.ID), "", ""), http.StatusNotFound)
	wantResult(t, curatedRequest(a, http.MethodGet, fmt.Sprintf("/api/v2/wants/%d/pools", active.ID), "", ""), http.StatusNotFound)
}
