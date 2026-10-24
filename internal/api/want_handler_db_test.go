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
		Want      store.Want       `json:"want"`
		Wants     []store.Want     `json:"wants"`
		First     uint64           `json:"first"`
		Last      uint64           `json:"last"`
		TagFacets []store.TagFacet `json:"tagFacets"`
	}
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

func TestWantLifecyclePrivacyQuotaDiscoveryAndPoolDerivationDB(t *testing.T) {
	a, priv := newOfferingAPI(t)
	t.Cleanup(func() { a.store.Pool().Close() })
	userID, token := seedCuratedAPIUser(t, a, priv)
	ctx := context.Background()
	var address string
	require.NoError(t, a.store.Pool().QueryRow(ctx, `UPDATE users SET email='amina@example.test',phone='+254700000000',avatar_url='https://storage.example/avatar.webp',max_wants=3 WHERE id=$1 RETURNING blockchain_address`, userID).Scan(&address))
	now := time.Now().UTC().Truncate(time.Second)
	active := wantResult(t, curatedRequest(a, http.MethodPost, "/api/v2/users/me/wants", token,
		wantBody(t, "Bicycle repair", now.Add(-time.Hour), nil, "Meet by the market", true, false)), http.StatusCreated).Want
	require.Equal(t, "active", active.State)
	require.Equal(t, []string{"Bicycle Help"}, active.Tags)
	require.Equal(t, 1, active.ContactPermission)

	public := wantResult(t, curatedRequest(a, http.MethodGet, fmt.Sprintf("/api/v2/wants/%d", active.ID), "", ""), http.StatusOK).Want
	require.Zero(t, public.ContactPermission)
	require.Equal(t, "amina@example.test", *public.Email)
	require.Nil(t, public.Phone)
	require.Equal(t, "Meet by the market", *public.ContactText)
	require.NotNil(t, public.EffectiveImageURL)

	tx, err := a.store.Pool().Begin(ctx)
	require.NoError(t, err)
	filter, err := store.NewCatalogFilter("wheel", []string{"Bicycle Help"}, store.TagMatchAny)
	require.NoError(t, err)
	_, err = a.store.ListWants(ctx, tx, filter, nil, store.CatalogFirstPage, 0, 20)
	require.NoError(t, err)
	_, err = a.store.ListWantTagFacets(ctx, tx, filter, nil)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback(ctx))

	list := wantResult(t, curatedRequest(a, http.MethodGet, "/api/v2/wants?q=wheel&tags=Bicycle%20Help&facets=true", "", ""), http.StatusOK)
	require.Len(t, list.Wants, 1)
	require.Zero(t, list.Wants[0].ContactPermission)
	require.Nil(t, list.Wants[0].ContactText)
	require.Nil(t, list.Wants[0].Email)
	require.Nil(t, list.Wants[0].Phone)
	require.NotEmpty(t, list.TagFacets)

	_, err = a.store.Pool().Exec(ctx, `UPDATE users SET email='new@example.test',avatar_url='https://storage.example/new.webp' WHERE id=$1`, userID)
	require.NoError(t, err)
	public = wantResult(t, curatedRequest(a, http.MethodGet, fmt.Sprintf("/api/v2/wants/%d", active.ID), "", ""), http.StatusOK).Want
	require.Equal(t, "new@example.test", *public.Email)
	require.Equal(t, "https://storage.example/new.webp", *public.EffectiveImageURL)

	scheduled := wantResult(t, curatedRequest(a, http.MethodPost, "/api/v2/users/me/wants", token,
		wantBody(t, "Scheduled request", now.Add(time.Hour), nil, "Text me later", false, true)), http.StatusCreated).Want
	require.Equal(t, "scheduled", scheduled.State)
	wantResult(t, curatedRequest(a, http.MethodGet, fmt.Sprintf("/api/v2/wants/%d", scheduled.ID), "", ""), http.StatusNotFound)
	owned := wantResult(t, curatedRequest(a, http.MethodGet, "/api/v2/users/me/wants?perPage=1", token, ""), http.StatusOK)
	require.Len(t, owned.Wants, 1)
	require.Equal(t, scheduled.ID, owned.Wants[0].ID)
	require.Equal(t, scheduled.ID, owned.First)
	require.NotZero(t, owned.Last)

	third := wantResult(t, curatedRequest(a, http.MethodPost, "/api/v2/users/me/wants", token,
		wantBody(t, "Third request", now.Add(-time.Hour), nil, "Call first", false, true)), http.StatusCreated).Want
	limit := curatedRequest(a, http.MethodPost, "/api/v2/users/me/wants", token,
		wantBody(t, "Over limit", now.Add(-time.Hour), nil, "Email first", true, false))
	require.Equal(t, http.StatusTooManyRequests, limit.Code, limit.Body.String())

	wantResult(t, curatedRequest(a, http.MethodPost, fmt.Sprintf("/api/v2/users/me/wants/%d/close", active.ID), token, ""), http.StatusOK)
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
	reopened := wantResult(t, curatedRequest(a, http.MethodPost, fmt.Sprintf("/api/v2/users/me/wants/%d/reopen", active.ID), token,
		wantBody(t, "Bicycle repair", now.Add(-time.Hour), nil, "Meet by the market", true, false)), http.StatusOK).Want
	require.Equal(t, store.WantStatusOpen, reopened.Status)

	var voucherID, poolID uint64
	var poolAddress string
	require.NoError(t, a.store.Pool().QueryRow(ctx, `INSERT INTO vouchers(chain_id,address,name,symbol,active,owner_address)
		VALUES(100,'0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),'Want owner voucher '||gen_random_uuid()::text,'WNT'||substr(md5(random()::text),1,5),true,$1) RETURNING id`, address).Scan(&voucherID))
	require.NoError(t, a.store.Pool().QueryRow(ctx, `INSERT INTO pools(chain_id,address,name)
		VALUES(100,'0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),'Want pool '||gen_random_uuid()::text) RETURNING id,address`).Scan(&poolID, &poolAddress))
	_, err = a.store.Pool().Exec(ctx, `INSERT INTO pool_tokens(pool_id,voucher_id,is_allowed) VALUES($1,$2,true)`, poolID, voucherID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := a.store.Pool().Exec(context.Background(), `DELETE FROM pools WHERE id=$1`, poolID)
		require.NoError(t, cleanupErr)
		_, cleanupErr = a.store.Pool().Exec(context.Background(), `DELETE FROM vouchers WHERE id=$1`, voucherID)
		require.NoError(t, cleanupErr)
	})

	tx, err = a.store.Pool().Begin(ctx)
	require.NoError(t, err)
	_, err = a.store.ListPoolWants(ctx, tx, poolAddress, store.CatalogFirstPage, 0, 20)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback(ctx))

	poolWants := wantResult(t, curatedRequest(a, http.MethodGet, fmt.Sprintf("/api/v2/pools/%s/wants", poolAddress), "", ""), http.StatusOK)
	require.NotEmpty(t, poolWants.Wants)
	creditURL := "/api/v2/wants?pools=" + url.QueryEscape("100:"+poolAddress)
	creditWants := wantResult(t, curatedRequest(a, http.MethodGet, creditURL, "", ""), http.StatusOK)
	require.NotEmpty(t, creditWants.Wants)
	_, err = a.store.Pool().Exec(ctx, `UPDATE pool_tokens SET is_allowed=false WHERE pool_id=$1 AND voucher_id=$2`, poolID, voucherID)
	require.NoError(t, err)
	poolWants = wantResult(t, curatedRequest(a, http.MethodGet, fmt.Sprintf("/api/v2/pools/%s/wants", poolAddress), "", ""), http.StatusOK)
	require.Empty(t, poolWants.Wants)
	_, err = a.store.Pool().Exec(ctx, `UPDATE pool_tokens SET is_allowed=true WHERE pool_id=$1 AND voucher_id=$2`, poolID, voucherID)
	require.NoError(t, err)
	_, err = a.store.Pool().Exec(ctx, `UPDATE vouchers SET owner_address='0x0000000000000000000000000000000000000001' WHERE id=$1`, voucherID)
	require.NoError(t, err)
	poolWants = wantResult(t, curatedRequest(a, http.MethodGet, fmt.Sprintf("/api/v2/pools/%s/wants", poolAddress), "", ""), http.StatusOK)
	require.Empty(t, poolWants.Wants)
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
}
