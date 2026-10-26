package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

func resourceGet(t *testing.T, a *API, target, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = "198.51.100.210:5100"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, req)
	return res
}

func resourceResultObject(t *testing.T, res *httptest.ResponseRecorder, key string) map[string]json.RawMessage {
	t.Helper()
	var envelope struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &envelope))
	var object map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(envelope.Result[key], &object))
	return object
}

func resourceResultObjects(t *testing.T, res *httptest.ResponseRecorder, key string) []map[string]json.RawMessage {
	t.Helper()
	var envelope struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &envelope))
	var objects []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(envelope.Result[key], &objects))
	return objects
}

func requireFieldsAbsent(t *testing.T, object map[string]json.RawMessage, fields ...string) {
	t.Helper()
	for _, field := range fields {
		require.NotContains(t, object, field)
	}
}

func TestPublicResourcesRedactContactsAndProtectedReadsRestoreThemDB(t *testing.T) {
	a, signingKey := newOfferingAPI(t)
	voucherAddress, _, cleanupVoucher := seedEditableVoucherFixture(t, a)
	defer cleanupVoucher()
	pools, cleanupPools := seedPoolProfileFixtures(t, a)
	defer cleanupPools()

	var voucherID, poolID uint64
	require.NoError(t, a.store.Pool().QueryRow(t.Context(),
		`SELECT id FROM vouchers WHERE lower(address)=lower($1)`, voucherAddress).Scan(&voucherID))
	require.NoError(t, a.store.Pool().QueryRow(t.Context(),
		`SELECT id FROM pools WHERE lower(address)=lower($1)`, pools.ownedAddress).Scan(&poolID))
	_, err := a.store.Pool().Exec(t.Context(), `
		INSERT INTO pool_tokens(pool_id,voucher_id,is_allowed)
		VALUES($1,$2,TRUE)
		ON CONFLICT(pool_id,voucher_id) DO UPDATE SET is_allowed=TRUE`, poolID, voucherID)
	require.NoError(t, err)
	defer func() {
		_, cleanupErr := a.store.Pool().Exec(t.Context(),
			`DELETE FROM pool_tokens WHERE pool_id=$1 AND voucher_id=$2`, poolID, voucherID)
		require.NoError(t, cleanupErr)
	}()

	userToken := offeringToken(t, signingKey, offeringOwnerAddress, "USER")

	publicVoucher := resourceGet(t, a, "/api/v2/vouchers/"+voucherAddress, "")
	require.Equal(t, http.StatusOK, publicVoucher.Code, publicVoucher.Body.String())
	requireFieldsAbsent(t, resourceResultObject(t, publicVoucher, "voucher"), "email", "phone", "website")
	publicVoucherWithSession := resourceGet(t, a, "/api/v2/vouchers/"+voucherAddress, userToken)
	require.JSONEq(t, publicVoucher.Body.String(), publicVoucherWithSession.Body.String())

	privateVoucher := resourceGet(t, a, "/api/v2/vouchers/"+voucherAddress+"/info", userToken)
	require.Equal(t, http.StatusOK, privateVoucher.Code, privateVoucher.Body.String())
	require.Equal(t, "private, no-store", privateVoucher.Header().Get("Cache-Control"))
	voucherInfo := resourceResultObject(t, privateVoucher, "voucher")
	require.Contains(t, voucherInfo, "email")
	require.Contains(t, voucherInfo, "phone")
	require.Contains(t, voucherInfo, "website")

	publicPool := resourceGet(t, a, "/api/v2/pools/"+pools.ownedAddress, "")
	require.Equal(t, http.StatusOK, publicPool.Code, publicPool.Body.String())
	requireFieldsAbsent(t, resourceResultObject(t, publicPool, "pool"), "email", "phone", "website")
	publicPoolWithSession := resourceGet(t, a, "/api/v2/pools/"+pools.ownedAddress, userToken)
	require.JSONEq(t, publicPool.Body.String(), publicPoolWithSession.Body.String())

	privatePool := resourceGet(t, a, "/api/v2/pools/"+pools.ownedAddress+"/info", userToken)
	require.Equal(t, http.StatusOK, privatePool.Code, privatePool.Body.String())
	require.Equal(t, "private, no-store", privatePool.Header().Get("Cache-Control"))
	poolInfo := resourceResultObject(t, privatePool, "pool")
	require.Contains(t, poolInfo, "email")
	require.Contains(t, poolInfo, "phone")
	require.Contains(t, poolInfo, "website")

	publicPoolList := resourceGet(t, a, "/api/v2/pools?owner="+offeringOwnerAddress, "")
	require.Equal(t, http.StatusOK, publicPoolList.Code, publicPoolList.Body.String())
	for _, row := range resourceResultObjects(t, publicPoolList, "pools") {
		requireFieldsAbsent(t, row, "email", "phone", "website")
	}

	publicMembers := resourceGet(t, a, "/api/v2/pools/"+pools.ownedAddress+"/vouchers", "")
	require.Equal(t, http.StatusOK, publicMembers.Code, publicMembers.Body.String())
	members := resourceResultObjects(t, publicMembers, "vouchers")
	require.NotEmpty(t, members)
	for _, row := range members {
		requireFieldsAbsent(t, row, "email", "phone", "website")
	}
}

func TestProtectedResourceReadsRequireAUserCredential(t *testing.T) {
	a, signingKey := newOfferingAPI(t)
	serviceToken, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, JWTCustomClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt: jwt.NewNumericDate(time.Now()), ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
		PublicKey: "0x2222222222222222222222222222222222222222", Service: true,
	}).SignedString(signingKey)
	require.NoError(t, err)
	expiredClaims := UserJWTClaims{
		UserID: 1, EthereumAddress: offeringOwnerAddress, Role: "USER",
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour)),
		},
	}
	expired, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, expiredClaims).SignedString(signingKey)
	require.NoError(t, err)

	paths := []string{
		"/api/v2/vouchers/0x0000000000000000000000000000000000000001/info",
		"/api/v2/pools/0x0000000000000000000000000000000000000001/info",
		"/api/v2/wants/1/contact",
	}
	for _, path := range paths {
		t.Run(strings.ReplaceAll(path, "/", "_"), func(t *testing.T) {
			require.Equal(t, http.StatusUnauthorized, resourceGet(t, a, path, "").Code)
			require.Equal(t, http.StatusUnauthorized, resourceGet(t, a, path, "forged").Code)
			require.Equal(t, http.StatusUnauthorized, resourceGet(t, a, path, expired).Code)
			require.Equal(t, http.StatusForbidden, resourceGet(t, a, path, serviceToken).Code)
		})
	}
}

func TestProtectedInfoPreservesRetiredOwnerAndAdminAccessDB(t *testing.T) {
	a, signingKey := newOfferingAPI(t)
	voucherAddress, _, cleanupVoucher := seedEditableVoucherFixture(t, a)
	defer cleanupVoucher()
	pools, cleanupPools := seedPoolProfileFixtures(t, a)
	defer cleanupPools()

	_, err := a.store.Pool().Exec(t.Context(),
		`UPDATE vouchers SET active=FALSE WHERE lower(address)=lower($1)`, voucherAddress)
	require.NoError(t, err)
	_, err = a.store.Pool().Exec(t.Context(),
		`UPDATE pools SET active=FALSE WHERE lower(address)=lower($1)`, pools.ownedAddress)
	require.NoError(t, err)

	voucherPath := "/api/v2/vouchers/" + voucherAddress
	poolPath := "/api/v2/pools/" + pools.ownedAddress
	require.Equal(t, http.StatusNotFound, resourceGet(t, a, voucherPath, "").Code)
	require.Equal(t, http.StatusNotFound, resourceGet(t, a, poolPath, "").Code)

	voucherOwner := offeringToken(t, signingKey, accountAddress, "USER")
	poolOwner := offeringToken(t, signingKey, offeringOwnerAddress, "USER")
	stranger := offeringToken(t, signingKey, offeringStrangerAddr, "USER")
	admin := offeringToken(t, signingKey, offeringOperatorAddr, "SUPERADMIN")

	require.Equal(t, http.StatusOK, resourceGet(t, a, voucherPath+"/info", voucherOwner).Code)
	require.Equal(t, http.StatusNotFound, resourceGet(t, a, voucherPath+"/info", stranger).Code)
	require.Equal(t, http.StatusOK, resourceGet(t, a, voucherPath+"/info", admin).Code)
	require.Equal(t, http.StatusOK, resourceGet(t, a, poolPath+"/info", poolOwner).Code)
	require.Equal(t, http.StatusNotFound, resourceGet(t, a, poolPath+"/info", stranger).Code)
	require.Equal(t, http.StatusOK, resourceGet(t, a, poolPath+"/info", admin).Code)
}

func TestProfileContactsRequireExplicitClearIntentDB(t *testing.T) {
	a, signingKey := newOfferingAPI(t)
	voucherAddress, _, cleanupVoucher := seedEditableVoucherFixture(t, a)
	defer cleanupVoucher()
	pools, cleanupPools := seedPoolProfileFixtures(t, a)
	defer cleanupPools()
	owner := offeringToken(t, signingKey, offeringOwnerAddress, "USER")
	admin := offeringToken(t, signingKey, offeringOperatorAddr, "SUPERADMIN")

	voucherPath := "/api/v2/admin/vouchers/" + voucherAddress + "/profile"
	res, _ := offeringRequest(t, a, http.MethodPut, voucherPath, admin,
		map[string]any{"email": "", "tags": []string{}})
	require.Equal(t, http.StatusConflict, res.Code, res.Body.String())
	private := resourceGet(t, a, "/api/v2/vouchers/"+voucherAddress+"/info", admin)
	require.Contains(t, resourceResultObject(t, private, "voucher"), "email")

	res, result := offeringRequest(t, a, http.MethodPut, voucherPath, admin,
		map[string]any{"email": nil, "clearContactFields": []string{"email"}, "tags": []string{}})
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	voucher, ok := result["voucher"].(map[string]any)
	require.True(t, ok)
	require.Nil(t, voucher["email"])

	poolPath := "/api/v2/pools/" + pools.ownedAddress + "/profile"
	res, _ = offeringRequest(t, a, http.MethodPut, poolPath, owner,
		map[string]any{"email": "", "tags": []string{}})
	require.Equal(t, http.StatusConflict, res.Code, res.Body.String())
	res, result = offeringRequest(t, a, http.MethodPut, poolPath, owner,
		map[string]any{"email": nil, "clearContactFields": []string{"email"}, "tags": []string{}})
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	pool, ok := result["pool"].(map[string]any)
	require.True(t, ok)
	require.Nil(t, pool["email"])
}
