package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// POST /auth/signin through the real route table, nonce store and database. A
// wallet with no account is registered under the paper-wallet placeholders
// rather than refused, so a key whose signup failed still signs in. Skipped
// unless CORE_TEST_DSN is set. Every wallet is a fresh key, and its rows are
// deleted by address, so no other suite's accounts are touched.

func newSigninAPI(t *testing.T) *API {
	t.Helper()

	dsn := os.Getenv("CORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CORE_TEST_DSN not set; skipping live sign-in tests")
	}

	logg := slog.New(slog.DiscardHandler)
	pgStore, err := store.NewPgStore(store.PgOpts{
		Logg:                 logg,
		DSN:                  dsn,
		MigrationsFolderPath: "../../migrations",
		QueriesFolderPath:    "../../core_queries.sql",
	})
	require.NoError(t, err)
	t.Cleanup(pgStore.Pool().Close)

	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	return New(APIOpts{
		Logg:         logg,
		CORS:         []string{"https://app.example"},
		Store:        pgStore,
		SigningKey:   priv,
		VerifyingKey: pub,
	})
}

// newSigninWallet returns a fresh key and deletes its account after the test.
func newSigninWallet(t *testing.T, a *API) (*ecdsa.PrivateKey, string) {
	t.Helper()

	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	address := crypto.PubkeyToAddress(key.PublicKey).Hex()
	t.Cleanup(func() {
		if _, err := a.store.Pool().Exec(context.Background(),
			`DELETE FROM users WHERE lower(blockchain_address) = lower($1)`, address); err != nil {
			t.Errorf("cleanup %s: %v", address, err)
		}
	})
	return key, address
}

// postSignin signs a fresh nonce the way apps/web does and posts it.
func postSignin(t *testing.T, a *API, key *ecdsa.PrivateKey) *httptest.ResponseRecorder {
	t.Helper()

	nonceRes := httptest.NewRecorder()
	a.router.ServeHTTP(nonceRes, httptest.NewRequest(http.MethodPost, "/auth/nonce", strings.NewReader("{}")))
	require.Equal(t, http.StatusOK, nonceRes.Code, nonceRes.Body.String())
	var nonceBody struct {
		Result struct {
			Nonce string `json:"nonce"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(nonceRes.Body.Bytes(), &nonceBody))

	message := fmt.Sprintf(
		"app.example wants you to sign in with your Ethereum account:\n%s\n\nSign in to CLC App\n\nURI: https://app.example\nVersion: 1\nChain ID: 100\nNonce: %s\nIssued At: %s",
		crypto.PubkeyToAddress(key.PublicKey).Hex(),
		nonceBody.Result.Nonce,
		time.Now().UTC().Format(time.RFC3339),
	)
	signature, err := crypto.Sign(accounts.TextHash([]byte(message)), key)
	require.NoError(t, err)
	signature[64] += 27

	body, err := json.Marshal(map[string]string{"message": message, "signature": hexutil.Encode(signature)})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/auth/signin", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, req)
	return res
}

type (
	signinResponse struct {
		Ok     bool   `json:"ok"`
		Code   string `json:"code"`
		Result struct {
			UserID uint64 `json:"userId"`
			Role   string `json:"role"`
		} `json:"result"`
	}
)

func decodeSignin(t *testing.T, res *httptest.ResponseRecorder) signinResponse {
	t.Helper()
	var body signinResponse
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &body), res.Body.String())
	return body
}

func TestSigninRegistersUnknownWallet(t *testing.T) {
	a := newSigninAPI(t)
	key, address := newSigninWallet(t, a)
	ctx := context.Background()

	res := postSignin(t, a, key)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	body := decodeSignin(t, res)
	assert.True(t, body.Ok)
	assert.Equal(t, "USER", body.Result.Role)
	require.NotZero(t, body.Result.UserID)

	var cookie *http.Cookie
	for _, c := range res.Result().Cookies() {
		if c.Name == userAuthCookieName {
			cookie = c
		}
	}
	require.NotNil(t, cookie, "a registered wallet must leave signed in")

	tx, err := a.store.Pool().Begin(ctx)
	require.NoError(t, err)
	user, err := a.store.GetUserByAddress(ctx, tx, address)
	require.NoError(t, tx.Rollback(ctx))
	require.NoError(t, err)
	assert.Equal(t, body.Result.UserID, user.ID)
	assert.Equal(t, address, user.EthereumAddress, "stored in the EIP-55 form the SIWE parser returns")
	assert.Equal(t, "Sign-in card", user.Name)
	assert.Equal(t, "pending+"+strings.ToLower(address[2:])+"@clc.local", user.Email)
	assert.Equal(t, "", user.Phone)
	assert.Nil(t, user.Username)
	assert.Equal(t, "APPROVED", user.Status)
	assert.Equal(t, "USER", user.Role)

	session := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	session.AddCookie(cookie)
	sessionRes := httptest.NewRecorder()
	a.router.ServeHTTP(sessionRes, session)
	require.Equal(t, http.StatusOK, sessionRes.Code, sessionRes.Body.String())
	assert.Contains(t, sessionRes.Body.String(), address)

	again := postSignin(t, a, key)
	require.Equal(t, http.StatusOK, again.Code, again.Body.String())
	assert.Equal(t, body.Result.UserID, decodeSignin(t, again).Result.UserID, "a second sign-in reuses the account")

	var count int
	require.NoError(t, a.store.Pool().QueryRow(ctx,
		`SELECT count(*) FROM users WHERE lower(blockchain_address) = lower($1)`, address).Scan(&count))
	assert.Equal(t, 1, count)
}

func TestSigninKeepsRegisteredProfile(t *testing.T) {
	a := newSigninAPI(t)
	key, address := newSigninWallet(t, a)
	ctx := context.Background()

	var id uint64
	require.NoError(t, a.store.Pool().QueryRow(ctx, `
		INSERT INTO users (blockchain_address, name, email, role, status)
		VALUES ($1, 'Amina Wanjiru', $2, 'USER', 'APPROVED')
		RETURNING id`, address, strings.ToLower(address)+"@example.test").Scan(&id))

	res := postSignin(t, a, key)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	assert.Equal(t, id, decodeSignin(t, res).Result.UserID)

	var name, email string
	require.NoError(t, a.store.Pool().QueryRow(ctx,
		`SELECT name, email FROM users WHERE id = $1`, id).Scan(&name, &email))
	assert.Equal(t, "Amina Wanjiru", name)
	assert.Equal(t, strings.ToLower(address)+"@example.test", email)
}

func TestSigninStillRefusesPendingAccount(t *testing.T) {
	a := newSigninAPI(t)
	key, address := newSigninWallet(t, a)

	_, err := a.store.Pool().Exec(context.Background(), `
		INSERT INTO users (blockchain_address, name, email, role, status)
		VALUES ($1, 'Awaiting approval', $2, 'USER', 'PENDING')`, address, strings.ToLower(address)+"@example.test")
	require.NoError(t, err)

	res := postSignin(t, a, key)
	require.Equal(t, http.StatusForbidden, res.Code, res.Body.String())
	assert.Empty(t, res.Result().Cookies())
}

// A second sign-in for the same new wallet — a double tap, or the link import
// racing the scanner — blocks on the first one's uncommitted insert. It must
// sign in to the account the first one created, not fail on the unique index.
func TestSigninJoinsConcurrentRegistration(t *testing.T) {
	a := newSigninAPI(t)
	key, address := newSigninWallet(t, a)
	ctx := context.Background()

	first, err := a.store.Pool().Begin(ctx)
	require.NoError(t, err)
	defer first.Rollback(ctx)
	var id uint64
	require.NoError(t, first.QueryRow(ctx, `
		INSERT INTO users (blockchain_address, name, email, role, status)
		VALUES ($1, 'Sign-in card', $2, 'USER', 'APPROVED')
		RETURNING id`, address, pendingSIWEEmail(address)).Scan(&id))

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- postSignin(t, a, key) }()

	// Commit only once the sign-in is waiting on this row, so the test exercises
	// the unique violation rather than a lookup that finds the account.
	require.Eventually(t, func() bool {
		var waiting int
		err := a.store.Pool().QueryRow(ctx, `
			SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database()
			  AND wait_event_type = 'Lock'
			  AND query ILIKE 'INSERT INTO users%'`).Scan(&waiting)
		return err == nil && waiting > 0
	}, 10*time.Second, 20*time.Millisecond, "the sign-in never blocked on the concurrent insert")
	require.NoError(t, first.Commit(ctx))

	var res *httptest.ResponseRecorder
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("sign-in did not finish after the concurrent insert committed")
	}
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	assert.Equal(t, id, decodeSignin(t, res).Result.UserID)
}

// The placeholder email is derived from the address, so only an account that
// copied it into its own profile can hold it. That is a conflict to report,
// and the wallet is not registered.
func TestSigninReportsHeldPlaceholderEmail(t *testing.T) {
	a := newSigninAPI(t)
	key, address := newSigninWallet(t, a)
	_, squatter := newSigninWallet(t, a)
	ctx := context.Background()

	_, err := a.store.Pool().Exec(ctx, `
		INSERT INTO users (blockchain_address, name, email, role, status)
		VALUES ($1, 'Squatter', $2, 'USER', 'APPROVED')`, squatter, pendingSIWEEmail(address))
	require.NoError(t, err)

	res := postSignin(t, a, key)
	require.Equal(t, http.StatusConflict, res.Code, res.Body.String())
	assert.Equal(t, "E17", decodeSignin(t, res).Code)

	var count int
	require.NoError(t, a.store.Pool().QueryRow(ctx,
		`SELECT count(*) FROM users WHERE lower(blockchain_address) = lower($1)`, address).Scan(&count))
	assert.Zero(t, count)
}
