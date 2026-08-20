package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v5"
)

// The endpoint under test, mounted exactly as api.go mounts it.
func newStorageTokenRouter(t *testing.T) (*echo.Echo, *API) {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := &API{signingKey: priv, verifyingKey: pub}

	e := echo.New()
	group := e.Group("/auth", a.flexibleAuthMiddleware(), a.requireUserAuth())
	group.POST("/storage-token", a.storageTokenHandler)
	return e, a
}

func sessionCookie(t *testing.T, a *API) *http.Cookie {
	t.Helper()

	token, err := a.generateUserToken(store.User{
		ID:              42,
		EthereumAddress: "0x1111111111111111111111111111111111111111",
		Role:            "USER",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: userAuthCookieName, Value: token}
}

func postStorageToken(t *testing.T, e *echo.Echo, cookie *http.Cookie, bearer string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/auth/storage-token", nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if bearer != "" {
		req.Header.Set(echo.HeaderAuthorization, "Bearer "+bearer)
	}
	res := httptest.NewRecorder()
	e.ServeHTTP(res, req)
	return res
}

func TestStorageTokenReturnsTheSessionsOwnToken(t *testing.T) {
	e, a := newStorageTokenRouter(t)
	cookie := sessionCookie(t, a)

	res := postStorageToken(t, e, cookie, "")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", res.Code, res.Body.String())
	}

	var body struct {
		Ok     bool `json:"ok"`
		Result struct {
			Token     string `json:"token"`
			ExpiresAt string `json:"expiresAt"`
		} `json:"result"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Ok {
		t.Fatal("ok = false")
	}

	// Echoed, not minted. A fresh token here would roll the 30-day session
	// forward on every upload.
	if body.Result.Token != cookie.Value {
		t.Fatal("token is not the cookie's own token")
	}

	expiresAt, err := time.Parse(time.RFC3339, body.Result.ExpiresAt)
	if err != nil {
		t.Fatalf("expiresAt %q: %v", body.Result.ExpiresAt, err)
	}
	// The session's own lifetime, not a shortened one.
	if remaining := time.Until(expiresAt); remaining < userCookieDuration-time.Minute {
		t.Fatalf("expiresAt is %v away, want about %v", remaining, userCookieDuration)
	}

	// A bearer credential must not be stored by a proxy or the browser cache.
	if got := res.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestStorageTokenIsStableAcrossCalls(t *testing.T) {
	e, a := newStorageTokenRouter(t)
	cookie := sessionCookie(t, a)

	first := postStorageToken(t, e, cookie, "")
	second := postStorageToken(t, e, cookie, "")

	if first.Body.String() != second.Body.String() {
		t.Fatal("two calls returned different tokens; the session is being re-minted")
	}
}

// A caller already authenticating by header gets that same token back, so the
// endpoint means one thing however the request arrived.
func TestStorageTokenEchoesABearerCredential(t *testing.T) {
	e, a := newStorageTokenRouter(t)
	cookie := sessionCookie(t, a)

	res := postStorageToken(t, e, nil, cookie.Value)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), cookie.Value) {
		t.Fatal("response did not carry the presented token")
	}
}

func TestStorageTokenRejectsAServiceToken(t *testing.T) {
	e, a := newStorageTokenRouter(t)

	serviceToken, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, JWTCustomClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
		PublicKey: "0x2222222222222222222222222222222222222222",
		Service:   true,
	}).SignedString(a.signingKey)
	if err != nil {
		t.Fatal(err)
	}

	// There is no browser to hand a token to, and a service already holds its own.
	res := postStorageToken(t, e, nil, serviceToken)
	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 body=%s", res.Code, res.Body.String())
	}
}

func TestStorageTokenRejectsAnUnauthenticatedCaller(t *testing.T) {
	e, _ := newStorageTokenRouter(t)

	res := postStorageToken(t, e, nil, "")
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 body=%s", res.Code, res.Body.String())
	}
}
