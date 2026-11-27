package api

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVoucherDeploySeparatesDisplayAndNativeSymbols(t *testing.T) {
	a := newRelayOperationAPI(t) // An insert-only River client, with no chain worker.
	key, ok := a.signingKey.(ed25519.PrivateKey)
	require.True(t, ok)
	ctx := context.Background()
	pool := a.store.Pool()

	const caller = "0x2400000000000000000000000000000000000077"
	const namePrefix = "ZXD Publish Fixture "
	var userID uint64
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO users(blockchain_address,name,email,status,role,max_tokens)
		VALUES($1,'Voucher publisher',gen_random_uuid()::TEXT || '@voucher-publish.test','APPROVED','USER',10)
		RETURNING id`, caller).Scan(&userID))
	t.Cleanup(func() {
		_, err := pool.Exec(ctx, `
			DELETE FROM identity_claims WHERE owner_key IN (
				SELECT 'deploy:' || (args->>'trackingId') FROM river_job
				WHERE kind='TOKEN_DEPLOY' AND starts_with(args->>'name',$1)
			)`, namePrefix)
		require.NoError(t, err)
		_, err = pool.Exec(ctx, `DELETE FROM river_job WHERE kind='TOKEN_DEPLOY' AND starts_with(args->>'name',$1)`, namePrefix)
		require.NoError(t, err)
		_, err = pool.Exec(ctx, "DELETE FROM user_deployments WHERE user_id=$1", userID)
		require.NoError(t, err)
		_, err = pool.Exec(ctx, "DELETE FROM users WHERE id=$1", userID)
		require.NoError(t, err)
	})
	session := mintTokenForUser(t, key, userID, caller, "USER")

	// Retirement leaves the immutable ticker occupied, but the old CLC ticker
	// can be published by a new Voucher without duplicating its contract ticker.
	var retiredID uint64
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO vouchers(chain_id,address,name,symbol,display_symbol,active)
		VALUES(100,'0x'||lpad(to_hex((random()*1e18)::BIGINT),40,'0'),'Retired publish fixture','ZXDNEW','ZXDNEW',false)
		RETURNING id`).Scan(&retiredID))
	t.Cleanup(func() {
		_, err := pool.Exec(ctx, "DELETE FROM vouchers WHERE id=$1", retiredID)
		require.NoError(t, err)
	})

	publish := func(name, native, label string) (*httptest.ResponseRecorder, map[string]any) {
		t.Helper()
		body := map[string]any{"name": namePrefix + name, "symbol": native, "decimals": "6", "owner": caller}
		if label != "" {
			body["displaySymbol"] = label
		}
		response, result := offeringRequest(t, a, http.MethodPost, "/api/v2/contracts/token", session, body)
		return response, result
	}
	assertQueued := func(result map[string]any, expectedNative, expectedLabel string) {
		t.Helper()
		trackingID, ok := result["trackingId"].(string)
		require.True(t, ok)
		require.NotEmpty(t, trackingID)
		var native, label string
		require.NoError(t, pool.QueryRow(ctx, `
			SELECT args->>'symbol', args->>'displaySymbol' FROM river_job
			WHERE kind='TOKEN_DEPLOY' AND args->>'trackingId'=$1`, trackingID).Scan(&native, &label))
		require.Equal(t, expectedNative, native)
		require.Equal(t, expectedLabel, label)
		require.Equal(t, 1, countRows(t, a, `
			SELECT count(*) FROM user_deployments WHERE user_id=$1 AND tracking_id=$2 AND resource_type='TOKEN'`, userID, trackingID))
	}

	response, result := publish("current", "ZXDNEW", " zxdnew ")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assertQueued(result, "ZXDN2", "ZXDNEW")

	response, _ = publish("collision", "ZXDNEW", "zxdnew")
	require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
	var conflict struct {
		Code   string            `json:"code"`
		Result map[string]string `json:"result"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &conflict))
	require.Equal(t, "E16", conflict.Code)
	require.Equal(t, "displaySymbol", conflict.Result["field"])
	require.Zero(t, countRows(t, a, `
		SELECT count(*) FROM river_job WHERE kind='TOKEN_DEPLOY' AND args->>'name'=$1`, namePrefix+"collision"))
	require.Zero(t, countRows(t, a, `
		SELECT count(*) FROM identity_claims WHERE namespace='symbol' AND name='zxdn3'`), "the rejected label must roll back its native candidate")

	response, result = publish("legacy", " ZxdLg ", "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assertQueued(result, " ZxdLg ", "ZXDLG")
}
