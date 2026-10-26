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
	t.Run("retire", func(t *testing.T) {
		response, _ := offeringRequest(t, a, http.MethodDelete, path, signer, nil)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
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
