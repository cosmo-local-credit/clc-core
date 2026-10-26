package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSafeSignerOffchainPermissionsThroughHTTP(t *testing.T) {
	a, key := newOfferingAPI(t)
	a.chainID = 42220
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()
	ctx := context.Background()
	const multisig = "0x9749234100000000000000000000000000000001"
	_, err := a.store.Pool().Exec(ctx, `UPDATE vouchers SET owner_address=$2 WHERE id=$1`, fx.ownedID, multisig)
	require.NoError(t, err)
	tx, err := a.store.Pool().Begin(ctx)
	require.NoError(t, err)
	_, err = a.store.ReplaceSafeSigners(ctx, tx, a.chainID, multisig,
		[]string{offeringOwnerAddress, offeringStrangerAddr}, 2, 200, 0)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
	t.Cleanup(func() {
		_, err := a.store.Pool().Exec(ctx, `DELETE FROM safe_state WHERE safe_address=$1`, strings.ToLower(multisig))
		require.NoError(t, err)
	})
	signer := offeringToken(t, key, offeringOwnerAddress, "USER")
	path := "/api/v2/vouchers/" + fx.ownedAddress
	t.Run("each signer edits metadata independently", func(t *testing.T) {
		for _, address := range []string{offeringOwnerAddress, offeringStrangerAddr} {
			token := offeringToken(t, key, address, "USER")
			response, _ := offeringRequest(t, a, http.MethodPut, path+"/display-name", token, map[string]any{"displayName": "Multisig voucher"})
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		}
	})
	t.Run("offering allowance and publication", func(t *testing.T) {
		response, _ := offeringRequest(t, a, http.MethodGet, "/api/v2/users/me/resource-usage?voucher="+fx.ownedAddress, signer, nil)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		response, _ = offeringRequest(t, a, http.MethodPost, path+"/offerings", signer, offeringBody(nil))
		require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	})

	t.Run("review owner controls", func(t *testing.T) {
		response, result := offeringRequest(t, a, http.MethodGet, path+"/reviews/me", signer, nil)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Equal(t, true, result["isOwner"])
	})
	t.Run("post linked to the multisig voucher", func(t *testing.T) {
		title := "Safe permission regression " + fx.ownedAddress
		response, _ := offeringRequest(t, a, http.MethodPost, "/api/v2/reports", signer,
			map[string]any{"title": title, "vouchers": []map[string]any{{"chainId": a.chainID, "address": fx.ownedAddress}}})
		t.Cleanup(func() { _, _ = a.store.Pool().Exec(ctx, `DELETE FROM reports WHERE title=$1`, title) })
		require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	})
	t.Run("retirement request on the Safe's behalf", func(t *testing.T) {
		a.retirementPreviewReader = retirementFixturePreview
		coSigner := offeringToken(t, key, offeringStrangerAddr, "USER")
		outsider := offeringToken(t, key, offeringOperatorAddr, "USER")
		requests := path + "/retirement-requests"

		response, _ := offeringRequest(t, a, http.MethodGet, path+"/retirement-preview", signer, nil)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		response, _ = offeringRequest(t, a, http.MethodPost, requests, outsider, map[string]any{"reason": "Not mine"})
		require.Equal(t, http.StatusForbidden, response.Code, response.Body.String())

		response, result := offeringRequest(t, a, http.MethodPost, requests, signer, map[string]any{"reason": "Safe closes voucher"})
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		id := retirementRequestNumber(t, result)
		request, ok := result["request"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, strings.ToLower(multisig), request["ownerAddress"], "the Safe owns the request")
		require.Equal(t, strings.ToLower(offeringOwnerAddress), request["requesterAddress"], "the signer made it")

		// The co-signer gets the Safe's pending request back, sees it, and
		// reads the history; only the requester may cancel it.
		response, again := offeringRequest(t, a, http.MethodPost, requests, coSigner, map[string]any{"reason": "Same intent"})
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Equal(t, id, retirementRequestNumber(t, again))
		response, own := offeringRequest(t, a, http.MethodGet, "/api/v2/users/me/retirement-requests", coSigner, nil)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Contains(t, fmt.Sprint(own["requests"]), fmt.Sprintf("id:%d ", id))
		response, _ = offeringRequest(t, a, http.MethodGet, requests, coSigner, nil)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		cancel := fmt.Sprintf("/api/v2/retirement-requests/%d/cancel", id)
		response, _ = offeringRequest(t, a, http.MethodPut, cancel, coSigner, nil)
		require.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
		response, _ = offeringRequest(t, a, http.MethodPut, cancel, signer, nil)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	})
	t.Run("retired voucher details stay with the signers", func(t *testing.T) {
		_, err := a.store.Pool().Exec(ctx, `UPDATE vouchers SET active=false WHERE id=$1`, fx.ownedID)
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = a.store.Pool().Exec(ctx, `UPDATE vouchers SET active=true WHERE id=$1`, fx.ownedID) })
		response, _ := offeringRequest(t, a, http.MethodGet, path+"/info", signer, nil)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		response, _ = offeringRequest(t, a, http.MethodGet, path+"/info", offeringToken(t, key, offeringOperatorAddr, "USER"), nil)
		require.Equal(t, http.StatusNotFound, response.Code, response.Body.String())
	})
	t.Run("owned pool routes", func(t *testing.T) {
		var poolID uint64
		var poolAddress string
		require.NoError(t, a.store.Pool().QueryRow(ctx, `INSERT INTO pools(chain_id,address,owner_address,name,symbol,active)
			VALUES($1,'0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),$2,'Safe pool','SAFEPOOL',false) RETURNING id,address`,
			a.chainID, strings.ToLower(multisig)).Scan(&poolID, &poolAddress))
		t.Cleanup(func() { _, _ = a.store.Pool().Exec(ctx, `DELETE FROM pools WHERE id=$1`, poolID) })
		for _, target := range []string{"/api/v2/users/me/owned-pools/" + poolAddress, "/api/v2/users/me/owned-pools/" + poolAddress + "/vouchers", "/api/v2/pools/" + poolAddress + "/info"} {
			response, _ := offeringRequest(t, a, http.MethodGet, target, signer, nil)
			require.Equal(t, http.StatusOK, response.Code, target+": "+response.Body.String())
			response, _ = offeringRequest(t, a, http.MethodGet, target, offeringToken(t, key, offeringOperatorAddr, "USER"), nil)
			require.Equal(t, http.StatusNotFound, response.Code, target+": "+response.Body.String())
		}
		response, result := offeringRequest(t, a, http.MethodGet, "/api/v2/users/me/owned-pools?status=retired", signer, nil)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Contains(t, strings.ToLower(fmt.Sprint(result["pools"])), strings.ToLower(poolAddress))
	})

	// A projected removal revokes the existing session, not merely its buttons.
	tx, err = a.store.Pool().Begin(ctx)
	require.NoError(t, err)
	_, err = a.store.ReplaceSafeSigners(ctx, tx, a.chainID, multisig,
		[]string{offeringStrangerAddr}, 1, 201, 0)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
	// Retirement must not mask the authorization check in this assertion.
	_, err = a.store.Pool().Exec(ctx, `UPDATE vouchers SET active=true WHERE id=$1`, fx.ownedID)
	require.NoError(t, err)
	response, _ := offeringRequest(t, a, http.MethodPut, path+"/display-name", signer, map[string]any{"displayName": "Removed signer"})
	require.Equal(t, http.StatusForbidden, response.Code, fmt.Sprint(response.Body))
}
