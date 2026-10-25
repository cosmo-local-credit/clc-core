package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func retirementFixturePreview(_ context.Context, _ pgx.Tx, resource store.RetirementResource) (store.RetirementPreview, error) {
	amount := "123000000"
	count := 2
	result := store.RetirementPreview{ResourceType: resource.ResourceType, ResourceAddress: resource.Address,
		ChainID: resource.ChainID, OwnerAddress: resource.OwnerAddress, Active: resource.Active,
		Symbol: resource.Symbol, Decimals: 6, BlockNumber: "123", CheckedAt: time.Now().UTC(), HasPositiveBalances: true}
	if resource.ResourceType == "voucher" {
		result.AmountOutsideOwnerRaw = &amount
		result.Coverage = "total-supply"
	} else {
		result.PositiveTokenCount = &count
		result.Coverage = "known-tokens"
	}
	return result, nil
}

func retirementRequestNumber(t *testing.T, result map[string]any) uint64 {
	t.Helper()
	request, ok := result["request"].(map[string]any)
	require.True(t, ok)
	id, ok := request["id"].(float64)
	require.True(t, ok)
	return uint64(id)
}

func requestAndApproveVoucher(t *testing.T, a *API, address, owner, admin string) {
	t.Helper()
	a.retirementPreviewReader = retirementFixturePreview
	response, result := offeringRequest(t, a, http.MethodPost, "/api/v2/vouchers/"+address+"/retirement-requests", owner, map[string]any{"reason": "End of use"})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	id := retirementRequestNumber(t, result)
	response, _ = offeringRequest(t, a, http.MethodPut, fmt.Sprintf("/api/v2/admin/retirement-requests/%d/approve", id), admin, map[string]any{"acknowledgeBalances": true})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
}

func TestRetirementLifecycle(t *testing.T) {
	for _, kind := range []string{"voucher", "pool"} {
		t.Run(kind, func(t *testing.T) {
			a, priv := newOfferingAPI(t)
			fx, cleanup := seedOfferingFixtures(t, a)
			defer cleanup()
			a.retirementPreviewReader = retirementFixturePreview
			ctx := context.Background()
			owner := offeringToken(t, priv, offeringOwnerAddress, "USER")
			other := offeringToken(t, priv, offeringStrangerAddr, "USER")
			admin := offeringToken(t, priv, offeringOperatorAddr, "SUPERADMIN")
			address := fx.ownedAddress
			if kind == "pool" {
				var id uint64
				require.NoError(t, a.store.Pool().QueryRow(ctx, `INSERT INTO pools(chain_id,address,owner_address,name,symbol) VALUES(100,'0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),$1,'Retirement pool','RETPOOL') RETURNING id,address`, offeringOwnerAddress).Scan(&id, &address))
				defer func() {
					for _, q := range []string{"DELETE FROM retirement_events WHERE pool_id=$1", "DELETE FROM retirement_requests WHERE pool_id=$1", "DELETE FROM pools WHERE id=$1"} {
						_, err := a.store.Pool().Exec(ctx, q, id)
						require.NoError(t, err)
					}
				}()
			}
			path := "/api/v2/" + kind + "s/" + address + "/retirement-requests"
			for _, token := range []string{other, admin} {
				response, _ := offeringRequest(t, a, http.MethodPost, path, token, map[string]any{"reason": "Request"})
				require.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
			}
			response, _ := offeringRequest(t, a, http.MethodPost, path, owner, map[string]any{"reason": " \n "})
			require.Equal(t, http.StatusBadRequest, response.Code)
			response, result := offeringRequest(t, a, http.MethodPost, path, owner, map[string]any{"reason": " End of use "})
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			id := retirementRequestNumber(t, result)
			var active bool
			require.NoError(t, a.store.Pool().QueryRow(ctx, "SELECT active FROM "+kind+"s WHERE lower(address)=lower($1)", address).Scan(&active))
			require.True(t, active)
			response, again := offeringRequest(t, a, http.MethodPost, path, owner, map[string]any{"reason": "Repeated submission"})
			require.Equal(t, http.StatusOK, response.Code)
			require.Equal(t, id, retirementRequestNumber(t, again))
			response, result = offeringRequest(t, a, http.MethodGet, "/api/v2/admin/retirement-requests", admin, nil)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			pendingCount, ok := result["pendingCount"].(float64)
			require.True(t, ok)
			require.GreaterOrEqual(t, pendingCount, float64(1))
			decision := fmt.Sprintf("/api/v2/admin/retirement-requests/%d/approve", id)
			response, _ = offeringRequest(t, a, http.MethodPut, decision, owner, map[string]any{"acknowledgeBalances": true})
			require.Equal(t, http.StatusForbidden, response.Code)
			response, _ = offeringRequest(t, a, http.MethodPut, decision, admin, map[string]any{})
			require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
			response, _ = offeringRequest(t, a, http.MethodPut, decision, admin, map[string]any{"acknowledgeBalances": true})
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.NoError(t, a.store.Pool().QueryRow(ctx, "SELECT active FROM "+kind+"s WHERE lower(address)=lower($1)", address).Scan(&active))
			require.False(t, active)
			response, _ = offeringRequest(t, a, http.MethodPut, decision, admin, map[string]any{"acknowledgeBalances": true})
			require.Equal(t, http.StatusConflict, response.Code)
			response, result = offeringRequest(t, a, http.MethodGet, "/api/v2/users/me/retirement-requests", owner, nil)
			require.Equal(t, http.StatusOK, response.Code)
			require.NotEmpty(t, result["requests"])
			restore := "/api/v2/admin/" + kind + "s/" + address + "/restore"
			response, _ = offeringRequest(t, a, http.MethodPut, restore, admin, map[string]any{"reason": "Approved return"})
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			response, _ = offeringRequest(t, a, http.MethodPut, restore, admin, map[string]any{"reason": "Repeated restore"})
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.NoError(t, a.store.Pool().QueryRow(ctx, "SELECT active FROM "+kind+"s WHERE lower(address)=lower($1)", address).Scan(&active))
			require.True(t, active)
			response, result = offeringRequest(t, a, http.MethodGet, "/api/v2/admin/"+kind+"s/"+address+"/retirement-history", admin, nil)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			events, ok := result["events"].([]any)
			require.True(t, ok)
			require.Len(t, events, 3)
			event, ok := events[0].(map[string]any)
			require.True(t, ok)
			require.Equal(t, "restored", event["action"])
			response, _ = offeringRequest(t, a, http.MethodGet, path, other, nil)
			require.Equal(t, http.StatusForbidden, response.Code)
		})
	}
}

func TestRetirementCancellationRejectionAndOwnershipChange(t *testing.T) {
	a, priv := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()
	a.retirementPreviewReader = retirementFixturePreview
	owner := offeringToken(t, priv, offeringOwnerAddress, "USER")
	admin := offeringToken(t, priv, offeringOperatorAddr, "SUPERADMIN")
	path := "/api/v2/vouchers/" + fx.ownedAddress + "/retirement-requests"
	for _, action := range []string{"cancel", "reject", "ownership"} {
		response, result := offeringRequest(t, a, http.MethodPost, path, owner, map[string]any{"reason": "Close voucher"})
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		id := retirementRequestNumber(t, result)
		if action == "cancel" {
			response, _ = offeringRequest(t, a, http.MethodPut, fmt.Sprintf("/api/v2/retirement-requests/%d/cancel", id), owner, nil)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		}
		if action == "reject" {
			target := fmt.Sprintf("/api/v2/admin/retirement-requests/%d/reject", id)
			response, _ = offeringRequest(t, a, http.MethodPut, target, admin, nil)
			require.Equal(t, http.StatusBadRequest, response.Code)
			response, _ = offeringRequest(t, a, http.MethodPut, target, admin, map[string]any{"reason": "Keep operating"})
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		}
		if action == "ownership" {
			a.retirementPreviewReader = func(context.Context, pgx.Tx, store.RetirementResource) (store.RetirementPreview, error) {
				return store.RetirementPreview{}, errRetirementOwnerChanged
			}
			response, _ = offeringRequest(t, a, http.MethodPut, fmt.Sprintf("/api/v2/admin/retirement-requests/%d/approve", id), admin, map[string]any{"acknowledgeBalances": true})
			require.Equal(t, http.StatusConflict, response.Code)
			var status string
			require.NoError(t, a.store.Pool().QueryRow(context.Background(), "SELECT status FROM retirement_requests WHERE id=$1", id).Scan(&status))
			require.Equal(t, "invalidated", status)
		}
		var active bool
		require.NoError(t, a.store.Pool().QueryRow(context.Background(), "SELECT active FROM vouchers WHERE id=$1", fx.ownedID).Scan(&active))
		require.True(t, active)
	}
}

func TestRetirementConcurrentRequestsAndDecisions(t *testing.T) {
	a, priv := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()
	a.retirementPreviewReader = retirementFixturePreview
	owner := offeringToken(t, priv, offeringOwnerAddress, "USER")
	admin := offeringToken(t, priv, offeringOperatorAddr, "SUPERADMIN")
	path := "/api/v2/vouchers/" + fx.ownedAddress + "/retirement-requests"
	var wg sync.WaitGroup
	ids := make(chan uint64, 2)
	codes := make(chan int, 2)
	for range 2 {
		wg.Go(func() {
			response, result := offeringRequest(t, a, http.MethodPost, path, owner, map[string]any{"reason": "Close voucher"})
			codes <- response.Code
			if response.Code == http.StatusOK {
				ids <- retirementRequestNumber(t, result)
			}
		})
	}
	wg.Wait()
	close(ids)
	close(codes)
	for code := range codes {
		require.Equal(t, http.StatusOK, code)
	}
	first := <-ids
	second := <-ids
	require.Equal(t, first, second)
	target := "/api/v2/admin/retirement-requests/" + strconv.FormatUint(first, 10) + "/approve"
	codes = make(chan int, 2)
	for range 2 {
		wg.Go(func() {
			response, _ := offeringRequest(t, a, http.MethodPut, target, admin, map[string]any{"acknowledgeBalances": true})
			codes <- response.Code
		})
	}
	wg.Wait()
	close(codes)
	outcomes := map[int]int{}
	for code := range codes {
		outcomes[code]++
	}
	require.Equal(t, map[int]int{http.StatusOK: 1, http.StatusConflict: 1}, outcomes)
	var decisions int
	require.NoError(t, a.store.Pool().QueryRow(context.Background(), "SELECT count(*) FROM retirement_events WHERE request_id=$1 AND action='approved'", first).Scan(&decisions))
	require.Equal(t, 1, decisions)
}

func TestRetirementUnavailablePreviewNeverCreatesRequest(t *testing.T) {
	a, priv := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()
	a.retirementPreviewReader = func(context.Context, pgx.Tx, store.RetirementResource) (store.RetirementPreview, error) {
		return store.RetirementPreview{}, errors.New("RPC unavailable")
	}
	owner := offeringToken(t, priv, offeringOwnerAddress, "USER")
	response, _ := offeringRequest(t, a, http.MethodPost, "/api/v2/vouchers/"+fx.ownedAddress+"/retirement-requests", owner, map[string]any{"reason": "End use"})
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	var count int
	require.NoError(t, a.store.Pool().QueryRow(context.Background(), "SELECT count(*) FROM retirement_requests WHERE voucher_id=$1", fx.ownedID).Scan(&count))
	require.Zero(t, count)
	response, _ = offeringRequest(t, a, http.MethodDelete, "/api/v2/vouchers/"+fx.ownedAddress, owner, nil)
	require.Equal(t, http.StatusGone, response.Code)
	require.True(t, strings.Contains(response.Body.String(), "retirement request"))
}

func TestRetirementRejectsEmptyAccountClaims(t *testing.T) {
	a, priv := newOfferingAPI(t)
	token := offeringServiceToken(t, priv)
	for _, path := range []string{"/api/v2/users/me/retirement-requests", "/api/v2/users/me/owned-pools"} {
		response, _ := offeringRequest(t, a, http.MethodGet, path, token, nil)
		require.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
	}
}
