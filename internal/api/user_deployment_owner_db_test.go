package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// GET /users/me/tokens/:address and /users/me/pools/:address return a
// deployment only to the user who started it, and to an administrator. Skipped
// unless CORE_TEST_DSN is set, like the other live endpoint tests.

const (
	ownerDeployAddress = "0x44a573149db22e759fB9A38Bcc461C12855c2644"
	otherDeployAddress = "0x55b573149db22e759fB9A38Bcc461C12855c2655"
	deployTokenAddress = "0x66c573149db22e759fB9A38Bcc461C12855c2666"
	deployPoolAddress  = "0x77d573149db22e759fB9A38Bcc461C12855c2677"
)

func mintUserToken(t *testing.T, priv any, userID uint64, address, role string) string {
	t.Helper()
	claims := UserJWTClaims{
		UserID:          userID,
		EthereumAddress: address,
		Role:            role,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(priv)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

func TestUserDeploymentByAddressIsOwnerOnlyDB(t *testing.T) {
	a, adminToken := newAdminAPI(t)
	ctx := context.Background()

	var keyID, ownerID, otherID, tokenOTX, poolOTX int64
	var tokenTracking, poolTracking string
	pool := a.store.Pool()

	cleanup := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM user_deployments WHERE tracking_id IN ($1, $2)`, tokenTracking, poolTracking)
		_, _ = pool.Exec(ctx, `DELETE FROM deployed_tokens WHERE otx_id = $1`, tokenOTX)
		_, _ = pool.Exec(ctx, `DELETE FROM deployed_pools WHERE otx_id = $1`, poolOTX)
		_, _ = pool.Exec(ctx, `DELETE FROM otx WHERE id IN ($1, $2)`, tokenOTX, poolOTX)
		_, _ = pool.Exec(ctx, `DELETE FROM keystore WHERE id = $1`, keyID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id IN ($1, $2)`, ownerID, otherID)
	}
	t.Cleanup(cleanup)

	must := func(err error, what string) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	must(pool.QueryRow(ctx, `
		INSERT INTO keystore (public_key, private_key)
		VALUES ('0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'), 'owner-test-key')
		RETURNING id`).Scan(&keyID), "insert key")
	insertUser := func(address, name string) int64 {
		var id int64
		must(pool.QueryRow(ctx, `
			INSERT INTO users (blockchain_address, name, email, username, status)
			VALUES ($1, $2, $3, $2, 'APPROVED') RETURNING id`,
			address, name, name+"@example.test").Scan(&id), "insert user")
		return id
	}
	ownerID = insertUser(ownerDeployAddress, "zqdeployowner")
	otherID = insertUser(otherDeployAddress, "zqdeployother")

	insertOTX := func(otxType string) (int64, string) {
		var id int64
		tracking := uuid.NewString()
		must(pool.QueryRow(ctx, `
			INSERT INTO otx (tracking_id, otx_type, signer_account, raw_tx, tx_hash, nonce)
			VALUES ($1, $2, $3, '0x00', $4, 0) RETURNING id`,
			tracking, otxType, keyID, "0x"+uuid.NewString()).Scan(&id), "insert otx")
		return id, tracking
	}
	platformID := func(contractType string) int64 {
		var id int64
		must(pool.QueryRow(ctx,
			`SELECT id FROM platform_contracts WHERE chain_id = 42220 AND contract_type = $1::contract_type`,
			contractType).Scan(&id), "select platform contract "+contractType)
		return id
	}

	tokenOTX, tokenTracking = insertOTX("TOKEN_PROXY_DEPLOY")
	_, err := pool.Exec(ctx, `
		INSERT INTO deployed_tokens (otx_id, platform_contract_id, status, address, symbol, name, decimals, admin)
		VALUES ($1, $2, 'CONFIRMED', $3, 'ZQDO', 'Zq deploy owner', 6, $4)`,
		tokenOTX, platformID("ERC20"), deployTokenAddress, ownerDeployAddress)
	must(err, "insert deployed token")
	_, err = pool.Exec(ctx, `INSERT INTO user_deployments (user_id, tracking_id, resource_type) VALUES ($1, $2, 'TOKEN')`, ownerID, tokenTracking)
	must(err, "link token deployment")

	poolOTX, poolTracking = insertOTX("POOL_TOKEN_INDEX_DEPLOY")
	_, err = pool.Exec(ctx, `
		INSERT INTO deployed_pools (otx_id, platform_contract_id, status, address, name, symbol, decimals,
		    token_registry_address, limiter_address, fee_policy_address, quoter_address,
		    protocol_fee_controller_address, fees_decoupled, admin)
		VALUES ($1, $2, 'CONFIRMED', $3, 'Zq deploy pool', 'ZQDP', 6, $4, $4, $4, $4, $4, false, $4)`,
		poolOTX, platformID("SWAPPOOL"), deployPoolAddress, ownerDeployAddress)
	must(err, "insert deployed pool")
	_, err = pool.Exec(ctx, `INSERT INTO user_deployments (user_id, tracking_id, resource_type) VALUES ($1, $2, 'POOL')`, ownerID, poolTracking)
	must(err, "link pool deployment")

	priv := a.signingKey
	callers := []struct {
		name  string
		token string
		want  int
	}{
		{"owner", mintUserToken(t, priv, uint64(ownerID), ownerDeployAddress, "USER"), http.StatusOK},
		{"another user", mintUserToken(t, priv, uint64(otherID), otherDeployAddress, "USER"), http.StatusNotFound},
		{"administrator", adminToken, http.StatusOK},
	}
	paths := []string{
		apiVersion + "/users/me/tokens/" + deployTokenAddress,
		apiVersion + "/users/me/pools/" + deployPoolAddress,
	}
	for _, path := range paths {
		for _, caller := range callers {
			t.Run(caller.name+" "+path, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.AddCookie(&http.Cookie{Name: userAuthCookieName, Value: caller.token})
				rec := httptest.NewRecorder()
				a.router.ServeHTTP(rec, req)
				if rec.Code != caller.want {
					t.Fatalf("status = %d, want %d, body %s", rec.Code, caller.want, rec.Body.String())
				}
			})
		}
	}
}
