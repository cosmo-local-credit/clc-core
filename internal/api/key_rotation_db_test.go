package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/relayer"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/internal/util"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Replacing a sign-in card through the real routes, nonce and challenge
// stores, river queue and database (GH clc-app#412). Skipped unless
// CORE_TEST_DSN is set. Every wallet is a fresh key and its rows are removed by
// user id afterwards.

const (
	rotationTestOrigin = "https://app.example"
)

func newRotationAPI(t *testing.T) *API {
	t.Helper()
	dsn := os.Getenv("CORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CORE_TEST_DSN not set; skipping live key rotation tests")
	}
	ctx := context.Background()
	logg := slog.New(slog.DiscardHandler)
	pgStore, err := store.NewPgStore(store.PgOpts{
		Logg:                 logg,
		DSN:                  dsn,
		MigrationsFolderPath: "../../migrations",
		QueriesFolderPath:    "../../core_queries.sql",
	})
	require.NoError(t, err)
	t.Cleanup(pgStore.Pool().Close)

	migrator, err := rivermigrate.New(riverpgxv5.New(pgStore.Pool()), nil)
	require.NoError(t, err)
	_, err = migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	require.NoError(t, err)
	queue, err := river.NewClient[pgx.Tx](riverpgxv5.New(pgStore.Pool()), &river.Config{})
	require.NoError(t, err)

	relayerInst, err := relayer.New(relayer.Opts{
		Logg:           logg,
		ChainID:        100,
		RPCEndpoint:    "http://127.0.0.1:1",
		CaliburAddress: webAuthnTestCalibur,
		AllowedOrigins: []string{rotationTestOrigin},
	})
	require.NoError(t, err)

	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	return New(APIOpts{
		Logg:         logg,
		CORS:         []string{rotationTestOrigin},
		Store:        pgStore,
		SigningKey:   priv,
		VerifyingKey: pub,
		QueueClient:  queue,
		ChainID:      100,
		Relayer:      relayerInst,
	})
}

// rotationCard is a fresh key; cleanup removes whatever account ends up on it
// and the rotations that name it.
func rotationCard(t *testing.T, a *API) (*ecdsa.PrivateKey, common.Address) {
	t.Helper()
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	address := crypto.PubkeyToAddress(key.PublicKey)
	t.Cleanup(func() {
		ctx := context.Background()
		pool := a.store.Pool()
		for _, statement := range []string{
			`DELETE FROM river_job WHERE kind = 'REGISTER_USER' AND args->>'userAddress' = lower($1)`,
			`DELETE FROM webauthn_credentials WHERE lower(address) = lower($1)`,
			`DELETE FROM user_deployments WHERE user_id IN (SELECT id FROM users WHERE lower(blockchain_address) = lower($1))`,
			`DELETE FROM address_rotations WHERE lower(old_address) = lower($1) OR lower(new_address) = lower($1)
				OR user_id IN (SELECT id FROM users WHERE lower(blockchain_address) = lower($1))
				OR admin_user_id IN (SELECT id FROM users WHERE lower(blockchain_address) = lower($1))`,
			`DELETE FROM users WHERE lower(blockchain_address) = lower($1)`,
		} {
			if _, err := pool.Exec(ctx, statement, address.Hex()); err != nil {
				t.Errorf("cleanup %s: %v", address.Hex(), err)
			}
		}
	})
	return key, address
}

func rotationNonce(t *testing.T, a *API) string {
	t.Helper()
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/auth/nonce", strings.NewReader("{}")))
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	var body struct {
		Result struct {
			Nonce string `json:"nonce"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &body))
	return body.Result.Nonce
}

// rotationSIWE signs the statement clc-app writes, for chainID, with key.
func rotationSIWE(t *testing.T, a *API, key *ecdsa.PrivateKey, statement string, chainID int) apiresp.KeyRotationSIWEProof {
	t.Helper()
	message := fmt.Sprintf(
		"app.example wants you to sign in with your Ethereum account:\n%s\n\n%s\n\nURI: https://app.example\nVersion: 1\nChain ID: %d\nNonce: %s\nIssued At: %s",
		crypto.PubkeyToAddress(key.PublicKey).Hex(), statement, chainID, rotationNonce(t, a), time.Now().UTC().Format(time.RFC3339),
	)
	signature, err := crypto.Sign(accounts.TextHash([]byte(message)), key)
	require.NoError(t, err)
	signature[64] += 27
	return apiresp.KeyRotationSIWEProof{Message: message, Signature: hexutil.Encode(signature)}
}

func postRotation(t *testing.T, a *API, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, req)
	return res
}

func getWithCookie(a *API, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, req)
	return res
}

func sessionFrom(t *testing.T, res *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range res.Result().Cookies() {
		if cookie.Name == userAuthCookieName && cookie.Value != "" {
			return cookie
		}
	}
	t.Fatalf("no session cookie in %d %s", res.Code, res.Body.String())
	return nil
}

type (
	rotationResponse struct {
		Ok     bool   `json:"ok"`
		Code   string `json:"code"`
		Result struct {
			UserID        uint64 `json:"userId"`
			Address       string `json:"address"`
			OldAddress    string `json:"oldAddress"`
			Replayed      bool   `json:"replayed"`
			MovedPasskeys []struct {
				CredentialID string `json:"credentialId"`
				PublicKeyX   string `json:"publicKeyX"`
			} `json:"movedPasskeys"`
			PasskeyRequired bool `json:"passkeyRequired"`
		} `json:"result"`
	}
)

func decodeRotation(t *testing.T, res *httptest.ResponseRecorder) rotationResponse {
	t.Helper()
	var body rotationResponse
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &body), res.Body.String())
	return body
}

// seedRotationPasskey stores an active passkey on address, as a confirmed
// Calibur registration would.
func seedRotationPasskey(t *testing.T, a *API, address common.Address) (*ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	// The standard library no longer exposes the coordinates; the uncompressed
	// point is 0x04 || X || Y.
	point, err := key.PublicKey.Bytes()
	require.NoError(t, err)
	x, y := new(big.Int).SetBytes(point[1:33]), new(big.Int).SetBytes(point[33:])
	credentialID := "rotation-" + strings.ToLower(address.Hex()[2:12])
	_, err = a.store.Pool().Exec(context.Background(), `
		INSERT INTO webauthn_credentials (credential_id, address, key_hash, public_key_x, public_key_y, state)
		VALUES ($1, $2, $3, $4::NUMERIC, $5::NUMERIC, 'active')`,
		credentialID, address.Hex(), relayer.ComputeP256KeyHash(x, y).Hex(), x.String(), y.String())
	require.NoError(t, err)
	return key, credentialID
}

// passkeyAssertion answers a fresh challenge the way a browser does.
func passkeyAssertion(t *testing.T, a *API, key *ecdsa.PrivateKey, credentialID string) *apiresp.WebAuthnRecoverRequest {
	t.Helper()
	challenge, err := a.relayer.IssueChallenge(context.Background())
	require.NoError(t, err)
	clientData, err := json.Marshal(map[string]string{
		"type": "webauthn.get", "challenge": challenge, "origin": rotationTestOrigin,
	})
	require.NoError(t, err)
	authData := make([]byte, 37)
	authData[32] = 0x05 // user present + user verified
	clientHash := sha256.Sum256(clientData)
	digest := sha256.Sum256(append(append([]byte{}, authData...), clientHash[:]...))
	signature, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	require.NoError(t, err)
	return &apiresp.WebAuthnRecoverRequest{
		CredentialID:      credentialID,
		AuthenticatorData: base64.RawURLEncoding.EncodeToString(authData),
		ClientDataJSON:    base64.RawURLEncoding.EncodeToString(clientData),
		Signature:         base64.RawURLEncoding.EncodeToString(signature),
	}
}

func cardRotation(t *testing.T, a *API, oldKey, newKey *ecdsa.PrivateKey) apiresp.KeyRotationRequest {
	t.Helper()
	statement := util.KeyRotationStatement(crypto.PubkeyToAddress(oldKey.PublicKey), crypto.PubkeyToAddress(newKey.PublicKey))
	siwe := rotationSIWE(t, a, oldKey, statement, 100)
	return apiresp.KeyRotationRequest{
		Old: apiresp.KeyRotationOldProof{SIWE: &siwe},
		New: rotationSIWE(t, a, newKey, statement, 100),
	}
}

func TestKeyRotationMovesTheAccountAndRetiresTheOldCard(t *testing.T) {
	a := newRotationAPI(t)
	ctx := context.Background()
	oldKey, oldAddress := rotationCard(t, a)
	newKey, newAddress := rotationCard(t, a)

	signin := postSignin(t, a, oldKey)
	require.Equal(t, http.StatusOK, signin.Code, signin.Body.String())
	userID := decodeSignin(t, signin).Result.UserID
	oldSession := sessionFrom(t, signin)
	require.Equal(t, http.StatusOK, getWithCookie(a, "/api/v2/users/me", oldSession).Code)

	res := postRotation(t, a, "/auth/key-rotation", cardRotation(t, a, oldKey, newKey), nil)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	body := decodeRotation(t, res)
	assert.Equal(t, userID, body.Result.UserID, "the account keeps its id")
	assert.Equal(t, newAddress.Hex(), body.Result.Address)
	assert.Equal(t, oldAddress.Hex(), body.Result.OldAddress)
	assert.False(t, body.Result.Replayed)
	newSession := sessionFrom(t, res)

	// The old card's session — anyone's copy of it — stops working at once.
	assert.Equal(t, http.StatusUnauthorized, getWithCookie(a, "/api/v2/users/me", oldSession).Code)
	assert.Equal(t, http.StatusNoContent, getWithCookie(a, "/auth/session", oldSession).Code)
	assert.Equal(t, http.StatusUnauthorized, getWithCookie(a, "/auth/webauthn/credentials", oldSession).Code)
	assert.Equal(t, http.StatusOK, getWithCookie(a, "/api/v2/users/me", newSession).Code)

	// The old card can never sign in or register again.
	retired := postSignin(t, a, oldKey)
	assert.Equal(t, http.StatusGone, retired.Code, retired.Body.String())
	assert.Equal(t, apiresp.ErrCodeAddressRetired, decodeSignin(t, retired).Code)

	// Sending to it is refused by name, without revealing the new card.
	reverse := getWithCookie(a, "/api/v2/recipients/reverse/"+oldAddress.Hex(), newSession)
	assert.Equal(t, http.StatusGone, reverse.Code, reverse.Body.String())
	assert.NotContains(t, reverse.Body.String(), newAddress.Hex())

	// The old card's pools can still be listed to move them, but only by the
	// account that replaced it.
	ownedByOld := getWithCookie(a, "/api/v2/users/me/owned-pools?owner="+oldAddress.Hex(), newSession)
	assert.Equal(t, http.StatusOK, ownedByOld.Code, ownedByOld.Body.String())
	_, strangerAddress := rotationCard(t, a)
	ownedByStranger := getWithCookie(a, "/api/v2/users/me/owned-pools?owner="+strangerAddress.Hex(), newSession)
	assert.Equal(t, http.StatusForbidden, ownedByStranger.Code, ownedByStranger.Body.String())

	history := getWithCookie(a, "/api/v2/users/me/key-rotations", newSession)
	require.Equal(t, http.StatusOK, history.Code, history.Body.String())
	assert.Contains(t, history.Body.String(), oldAddress.Hex())

	// The new wallet is queued for the AccountIndex so its stablecoin
	// transfers are tracked even if it never relays a delegation.
	var queued int
	require.NoError(t, a.store.Pool().QueryRow(ctx,
		`SELECT count(*) FROM river_job WHERE kind = 'REGISTER_USER' AND args->>'userAddress' = lower($1)`,
		newAddress.Hex()).Scan(&queued))
	assert.Equal(t, 1, queued)

	// A client that lost the response retries with fresh proofs and is told
	// the same thing, with a session for the new card.
	retry := postRotation(t, a, "/auth/key-rotation", cardRotation(t, a, oldKey, newKey), nil)
	require.Equal(t, http.StatusOK, retry.Code, retry.Body.String())
	assert.True(t, decodeRotation(t, retry).Result.Replayed)
	sessionFrom(t, retry)
}

func TestKeyRotationNeedsThePasskeyWhenTheAccountHasOne(t *testing.T) {
	a := newRotationAPI(t)
	oldKey, oldAddress := rotationCard(t, a)
	newKey, newAddress := rotationCard(t, a)
	require.Equal(t, http.StatusOK, postSignin(t, a, oldKey).Code)
	passkey, credentialID := seedRotationPasskey(t, a, oldAddress)

	// Whoever copied the card does not hold the passkey.
	refused := postRotation(t, a, "/auth/key-rotation", cardRotation(t, a, oldKey, newKey), nil)
	require.Equal(t, http.StatusForbidden, refused.Code, refused.Body.String())
	assert.True(t, decodeRotation(t, refused).Result.PasskeyRequired)

	statement := util.KeyRotationStatement(oldAddress, newAddress)
	res := postRotation(t, a, "/auth/key-rotation", apiresp.KeyRotationRequest{
		Old: apiresp.KeyRotationOldProof{WebAuthn: passkeyAssertion(t, a, passkey, credentialID)},
		New: rotationSIWE(t, a, newKey, statement, 100),
	}, nil)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	body := decodeRotation(t, res)
	require.Len(t, body.Result.MovedPasskeys, 1)
	assert.Equal(t, credentialID, body.Result.MovedPasskeys[0].CredentialID)
	assert.NotEmpty(t, body.Result.MovedPasskeys[0].PublicKeyX)

	// Until the new wallet registers it, the passkey says where to go next
	// instead of offering sign-up.
	verify := postRotation(t, a, "/auth/webauthn/verify", passkeyAssertion(t, a, passkey, credentialID), nil)
	assert.Equal(t, http.StatusConflict, verify.Code, verify.Body.String())
	assert.Contains(t, verify.Body.String(), apiresp.ErrCodePasskeyMoved)

	credentials := getWithCookie(a, "/auth/webauthn/credentials", sessionFrom(t, res))
	require.Equal(t, http.StatusOK, credentials.Code, credentials.Body.String())
	assert.Contains(t, credentials.Body.String(), `"state":"moved"`)
	assert.Contains(t, credentials.Body.String(), `"publicKeyX":"`)

	// A retry signed by the same passkey, now waiting on the new wallet, is
	// answered from the committed rotation.
	retry := postRotation(t, a, "/auth/key-rotation", apiresp.KeyRotationRequest{
		Old: apiresp.KeyRotationOldProof{WebAuthn: passkeyAssertion(t, a, passkey, credentialID)},
		New: rotationSIWE(t, a, newKey, statement, 100),
	}, nil)
	require.Equal(t, http.StatusOK, retry.Code, retry.Body.String())
	assert.True(t, decodeRotation(t, retry).Result.Replayed)
}

func TestKeyRotationRejectsProofsThatDoNotMatch(t *testing.T) {
	a := newRotationAPI(t)
	oldKey, oldAddress := rotationCard(t, a)
	newKey, newAddress := rotationCard(t, a)
	otherKey, otherAddress := rotationCard(t, a)
	require.Equal(t, http.StatusOK, postSignin(t, a, oldKey).Code)

	statement := util.KeyRotationStatement(oldAddress, newAddress)
	oldProof := func(key *ecdsa.PrivateKey, statement string, chainID int) apiresp.KeyRotationOldProof {
		proof := rotationSIWE(t, a, key, statement, chainID)
		return apiresp.KeyRotationOldProof{SIWE: &proof}
	}
	for name, req := range map[string]apiresp.KeyRotationRequest{
		"sign-in statement": {
			Old: oldProof(oldKey, "Sign in to CLC App", 100),
			New: rotationSIWE(t, a, newKey, "Sign in to CLC App", 100),
		},
		"old card signs for another replacement": {
			Old: oldProof(oldKey, util.KeyRotationStatement(oldAddress, otherAddress), 100),
			New: rotationSIWE(t, a, newKey, statement, 100),
		},
		"new proof signed by someone else": {
			Old: oldProof(oldKey, statement, 100),
			New: rotationSIWE(t, a, otherKey, statement, 100),
		},
		"old proof signed by the new card": {
			Old: oldProof(newKey, statement, 100),
			New: rotationSIWE(t, a, newKey, statement, 100),
		},
		"another chain": {
			Old: oldProof(oldKey, statement, 42220),
			New: rotationSIWE(t, a, newKey, statement, 42220),
		},
		"no old proof": {
			New: rotationSIWE(t, a, newKey, statement, 100),
		},
	} {
		t.Run(name, func(t *testing.T) {
			res := postRotation(t, a, "/auth/key-rotation", req, nil)
			assert.Equal(t, http.StatusBadRequest, res.Code, res.Body.String())
		})
	}

	// A card that already has its own account cannot absorb another.
	require.Equal(t, http.StatusOK, postSignin(t, a, otherKey).Code)
	taken := postRotation(t, a, "/auth/key-rotation", cardRotation(t, a, oldKey, otherKey), nil)
	assert.Equal(t, http.StatusConflict, taken.Code, taken.Body.String())

	var current string
	require.NoError(t, a.store.Pool().QueryRow(context.Background(),
		`SELECT blockchain_address FROM users WHERE lower(blockchain_address) = lower($1)`, oldAddress.Hex()).Scan(&current))
	assert.Equal(t, oldAddress.Hex(), current, "nothing moved")
}

func TestAdminKeyRotationIsSuperadminOnly(t *testing.T) {
	a := newRotationAPI(t)
	ctx := context.Background()
	_, targetAddress := rotationCard(t, a)
	newKey, newAddress := rotationCard(t, a)
	_, superAddress := rotationCard(t, a)
	_, adminAddress := rotationCard(t, a)
	_, otherSuperAddress := rotationCard(t, a)

	seedRoleTestUser(t, a, targetAddress.Hex(), "USER", "APPROVED")
	superUser := seedRoleTestUser(t, a, superAddress.Hex(), "SUPERADMIN", "APPROVED")
	adminUser := seedRoleTestUser(t, a, adminAddress.Hex(), "ADMIN", "APPROVED")
	seedRoleTestUser(t, a, otherSuperAddress.Hex(), "SUPERADMIN", "APPROVED")

	tokenFor := func(user store.User) *http.Cookie {
		token, err := a.generateUserToken(user)
		require.NoError(t, err)
		return &http.Cookie{Name: userAuthCookieName, Value: token}
	}
	rotate := func(cookie *http.Cookie, target common.Address) *httptest.ResponseRecorder {
		return postRotation(t, a, "/api/v2/admin/users/"+target.Hex()+"/key-rotation", apiresp.AdminKeyRotationRequest{
			New: rotationSIWE(t, a, newKey, util.KeyRotationStatement(target, newAddress), 100),
		}, cookie)
	}

	assert.Equal(t, http.StatusForbidden, rotate(tokenFor(adminUser), targetAddress).Code, "an ADMIN cannot re-key an account")
	assert.Equal(t, http.StatusForbidden, rotate(tokenFor(superUser), superAddress).Code, "nor a superadmin their own")
	assert.Equal(t, http.StatusForbidden, rotate(tokenFor(superUser), otherSuperAddress).Code, "nor another superadmin's")

	res := rotate(tokenFor(superUser), targetAddress)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	assert.Equal(t, newAddress.Hex(), decodeRotation(t, res).Result.Address)
	for _, cookie := range res.Result().Cookies() {
		assert.NotEqual(t, userAuthCookieName, cookie.Name, "the operator must not receive the user's session")
	}

	var initiatedBy string
	var adminID *uint64
	require.NoError(t, a.store.Pool().QueryRow(ctx,
		`SELECT initiated_by, admin_user_id FROM address_rotations WHERE lower(old_address) = lower($1)`,
		targetAddress.Hex()).Scan(&initiatedBy, &adminID))
	assert.Equal(t, store.RotationByAdmin, initiatedBy)
	require.NotNil(t, adminID)
	assert.Equal(t, superUser.ID, *adminID)
}
