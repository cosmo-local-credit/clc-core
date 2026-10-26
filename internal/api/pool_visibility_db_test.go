package api

import (
	"context"
	"math/big"
	"net/http"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

func TestRetiredPoolOwnerAndAdminRecoveryRoutes(t *testing.T) {
	a, signingKey := newOfferingAPI(t)
	fx, cleanup := seedPoolProfileFixtures(t, a)
	defer cleanup()
	ctx := context.Background()
	_, err := a.store.Pool().Exec(ctx, `UPDATE pools SET active=false WHERE id=$1`, fx.ownedID)
	require.NoError(t, err)
	owner := offeringToken(t, signingKey, offeringOwnerAddress, "USER")
	stranger := offeringToken(t, signingKey, offeringStrangerAddr, "USER")
	admin := offeringToken(t, signingKey, offeringOperatorAddr, "SUPERADMIN")
	for _, test := range []struct {
		path, token string
		status      int
	}{
		{"/api/v2/pools/" + fx.ownedAddress, "", http.StatusNotFound},
		{"/api/v2/pools/" + fx.ownedAddress + "/vouchers", "", http.StatusNotFound},
		{"/api/v2/users/me/owned-pools/" + fx.ownedAddress, owner, http.StatusOK},
		{"/api/v2/users/me/owned-pools/" + fx.ownedAddress + "/vouchers", owner, http.StatusOK},
		{"/api/v2/users/me/owned-pools/" + fx.ownedAddress, stranger, http.StatusNotFound},
		{"/api/v2/users/me/owned-pools/" + fx.ownedAddress + "/vouchers", stranger, http.StatusNotFound},
		{"/api/v2/users/me/owned-pools/" + fx.ownedAddress, "", http.StatusUnauthorized},
		{"/api/v2/admin/pools/" + fx.ownedAddress, admin, http.StatusOK},
		{"/api/v2/admin/pools/" + fx.ownedAddress, owner, http.StatusForbidden},
	} {
		res, _ := offeringRequest(t, a, http.MethodGet, test.path, test.token, nil)
		require.Equal(t, test.status, res.Code, test.path+": "+res.Body.String())
	}
	res, result := offeringRequest(t, a, http.MethodGet, "/api/v2/users/me/owned-pools?status=retired", owner, nil)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	rows, ok := result["pools"].([]any)
	require.True(t, ok)
	require.Len(t, rows, 1)
	poolRow, ok := rows[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, false, poolRow["active"])
	res, result = offeringRequest(t, a, http.MethodGet, "/api/v2/admin/pool-catalog?status=retired&q="+fx.ownedAddress, admin, nil)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	rows, ok = result["pools"].([]any)
	require.True(t, ok)
	require.Len(t, rows, 1)
	require.Equal(t, "no-store", res.Header().Get("Cache-Control"))
}

func TestRelayAdmissionRejectsRetiredPoolButPreservesWithdrawals(t *testing.T) {
	a := newRelayOperationAPI(t)
	ctx := context.Background()
	pool := common.HexToAddress("0x8888888888888888888888888888888888888888")
	activePool := common.HexToAddress("0x7777777777777777777777777777777777777777")
	token := common.HexToAddress("0x1111111111111111111111111111111111111111")
	_, err := a.store.Pool().Exec(ctx, `INSERT INTO pools(chain_id,address,active) VALUES(100,$1,false)`, pool.Hex())
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := a.store.Pool().Exec(ctx, `DELETE FROM pools WHERE chain_id=100 AND address=$1`, pool.Hex())
		require.NoError(t, err)
	})
	tracker := newRelayJobTracker(t, a)
	for _, test := range []struct {
		name, target, data string
		want               int
	}{
		{"direct deposit", token.Hex(), transferCalldata(pool, big.NewInt(5)), http.StatusConflict},
		{"swap", pool.Hex(), withdrawCalldata(token, token, big.NewInt(5), nil), http.StatusConflict},
		{"swap output to retired pool", activePool.Hex(), withdrawCalldata(token, token, big.NewInt(5), &pool), http.StatusConflict},
		{"liquidity output to retired pool", activePool.Hex(), calldata(functionSelector("withdrawLiquidity(address,address,uint256)"), addressWord(token), addressWord(pool), uintWord(big.NewInt(5))), http.StatusConflict},
		{"withdraw liquidity", pool.Hex(), calldata(functionSelector("withdrawLiquidity(address,address,uint256)"), addressWord(token), addressWord(token), uintWord(big.NewInt(5))), http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := relayOperationBody(tracker.operation(), "7")
			signed, ok := body["signedBatch"].(map[string]any)
			require.True(t, ok)
			batch, ok := signed["batchedCall"].(map[string]any)
			require.True(t, ok)
			batch["calls"] = []map[string]any{{"to": test.target, "value": "0", "data": test.data}}
			status, response := postRelay(t, a, body)
			require.Equal(t, test.want, status, response.Description)
			if status == http.StatusOK {
				trackingID, ok := response.Result["trackingId"].(string)
				require.True(t, ok)
				tracker.track(trackingID)
			}
		})
	}
}
