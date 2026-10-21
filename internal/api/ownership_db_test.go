package api

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTransferredAuthorityFollowsCatalogWithoutChangingSellerProvenance(t *testing.T) {
	a, key := newOfferingAPI(t)
	vouchers, cleanupV := seedOfferingFixtures(t, a)
	defer cleanupV()
	pools, cleanupP := seedPoolProfileFixtures(t, a)
	defer cleanupP()
	ctx := context.Background()
	var originalSeller string
	require.NoError(t, a.store.Pool().QueryRow(ctx, `SELECT seller_address FROM offerings WHERE id=$1`, vouchers.legacyOfferingID).Scan(&originalSeller))
	tx, err := a.store.Pool().Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	for _, address := range []string{vouchers.ownedAddress, pools.ownedAddress} {
		n, err := a.store.ProjectCatalogOwnership(ctx, tx, 42220, address, offeringOperatorAddr, 50, 1)
		require.NoError(t, err)
		require.EqualValues(t, 1, n)
	}
	require.NoError(t, tx.Commit(ctx))
	t.Cleanup(func() {
		_, err := a.store.Pool().Exec(ctx, `DELETE FROM catalog_ownership WHERE address=ANY($1::text[])`, []string{vouchers.ownedAddress, pools.ownedAddress})
		require.NoError(t, err)
	})
	for _, target := range []string{"/api/v2/vouchers/" + vouchers.ownedAddress + "/profile", "/api/v2/pools/" + pools.ownedAddress + "/profile"} {
		old := offeringToken(t, key, offeringOwnerAddress, "USER")
		res, _ := offeringRequest(t, a, http.MethodPut, target, old, map[string]any{"description": "old owner attempt"})
		require.Equal(t, http.StatusForbidden, res.Code, res.Body.String())
		next := offeringToken(t, key, offeringOperatorAddr, "USER")
		res, _ = offeringRequest(t, a, http.MethodPut, target, next, map[string]any{"description": "new owner update"})
		require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	}
	target := fmt.Sprintf("/api/v2/vouchers/%s/offerings/%d", vouchers.ownedAddress, vouchers.legacyOfferingID)
	old := offeringToken(t, key, offeringOwnerAddress, "USER")
	body := map[string]any{"commodityName": "Transferred offering", "description": "Existing offer", "unit": "bag", "price": "7.25"}
	res, _ := offeringRequest(t, a, http.MethodPut, target, old, body)
	require.Equal(t, http.StatusForbidden, res.Code, res.Body.String())
	next := offeringToken(t, key, offeringOperatorAddr, "USER")
	res, _ = offeringRequest(t, a, http.MethodPut, target, next, body)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	var seller string
	require.NoError(t, a.store.Pool().QueryRow(ctx, `SELECT seller_address FROM offerings WHERE id=$1`, vouchers.legacyOfferingID).Scan(&seller))
	require.Equal(t, originalSeller, seller)
}
