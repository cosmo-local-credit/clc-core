package api

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/relayer"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/golang-jwt/jwt/v5"
	gocalibur "github.com/grassrootseconomics/go-calibur"
)

const webAuthnTestCalibur = "0x000000009B1D0aF20D8C6d0A44e162d11F9b8f00"

func TestWebAuthnCredentialsRequiresAUserSession(t *testing.T) {
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, otherPrivate, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	a := New(APIOpts{
		Logg:         slog.New(slog.DiscardHandler),
		CORS:         []string{"https://app.example"},
		SigningKey:   private,
		VerifyingKey: public,
	})

	forged, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, UserJWTClaims{
		EthereumAddress: "0x" + "1234567890123456789012345678901234567890",
		Role:            "USER",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}).SignedString(otherPrivate)
	if err != nil {
		t.Fatal(err)
	}
	serviceToken, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, JWTCustomClaims{
		PublicKey: "0x1234567890123456789012345678901234567890",
		Service:   true,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}).SignedString(private)
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]func(*http.Request){
		"guest":                     func(*http.Request) {},
		"cookie signed by another":  func(r *http.Request) { r.AddCookie(&http.Cookie{Name: userAuthCookieName, Value: forged}) },
		"service bearer, no cookie": func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+serviceToken) },
	}
	for name, prepare := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/auth/webauthn/credentials", nil)
			prepare(req)
			res := httptest.NewRecorder()
			a.router.ServeHTTP(res, req)
			if res.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", res.Code)
			}
		})
	}
}

func TestWebAuthnCredentialSummariesExposeTrackingOnlyWhilePending(t *testing.T) {
	pendingID := "11111111-1111-1111-1111-111111111111"
	activeID := "22222222-2222-2222-2222-222222222222"
	got := webAuthnCredentialSummaries([]store.WebAuthnCredential{
		{CredentialID: "a", State: store.WebAuthnPending, TrackingID: &pendingID},
		{CredentialID: "b", State: store.WebAuthnActive, TrackingID: &activeID},
	})
	if got[0].TrackingID == nil || *got[0].TrackingID != pendingID {
		t.Fatalf("pending summary trackingId = %v, want %s", got[0].TrackingID, pendingID)
	}
	if got[1].TrackingID != nil {
		t.Fatalf("active summary trackingId = %v, want nil", *got[1].TrackingID)
	}
}

// TestRelayRejectsForgedRegistrationBeforeQueueing drives the public relay
// route with registrations that must never reach the job queue. The API has no
// store or queue client: reaching either would panic, so a 400 proves the
// request was refused before anything was persisted.
func TestRelayRejectsForgedRegistrationBeforeQueueing(t *testing.T) {
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	relayerInst, err := relayer.New(relayer.Opts{
		Logg:           slog.New(slog.DiscardHandler),
		ChainID:        100,
		RPCEndpoint:    "http://127.0.0.1:1",
		CaliburAddress: webAuthnTestCalibur,
	})
	if err != nil {
		t.Fatal(err)
	}
	a := New(APIOpts{
		Logg:         slog.New(slog.DiscardHandler),
		CORS:         []string{"https://app.example"},
		SigningKey:   private,
		VerifyingKey: public,
		Relayer:      relayerInst,
	})

	victimKey, _ := crypto.GenerateKey()
	victim := crypto.PubkeyToAddress(victimKey.PublicKey)
	attackerKey, _ := crypto.GenerateKey()
	passkey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	registerBatch := func(user common.Address, x, y string) apiresp.CaliburSignedBatch {
		px, _ := new(big.Int).SetString(x, 10)
		py, _ := new(big.Int).SetString(y, 10)
		data, err := relayer.EncodeWebAuthnRegisterCalldata(px, py)
		if err != nil {
			t.Fatal(err)
		}
		return apiresp.CaliburSignedBatch{
			BatchedCall: apiresp.CaliburBatchedCall{
				Calls:           []apiresp.CaliburCall{{To: user.Hex(), Value: "0", Data: hexutil.Encode(data)}},
				RevertOnFailure: true,
			},
			Nonce:    "0",
			KeyHash:  common.Hash{}.Hex(),
			Executor: common.Address{}.Hex(),
			Deadline: "0",
		}
	}
	sign := func(key *ecdsa.PrivateKey, user common.Address, batch apiresp.CaliburSignedBatch) string {
		digest, err := relayerDigest(user, batch)
		if err != nil {
			t.Fatal(err)
		}
		sig, err := crypto.Sign(digest, key)
		if err != nil {
			t.Fatal(err)
		}
		bytesT, _ := abi.NewType("bytes", "", nil)
		wrapped, err := abi.Arguments{{Type: bytesT}, {Type: bytesT}}.Pack(sig, []byte{})
		if err != nil {
			t.Fatal(err)
		}
		return hexutil.Encode(wrapped)
	}

	x, y := passkey.X.String(), passkey.Y.String()
	victimBatch := registerBatch(victim, x, y)

	cases := map[string]apiresp.RelayRequest{
		"attacker passkey on victim address": {
			UserAddress:   victim.Hex(),
			SignedBatch:   victimBatch,
			Signature:     sign(attackerKey, victim, victimBatch),
			SignatureType: "secp256k1",
			CredentialID:  "attacker-credential",
			PublicKeyX:    x,
			PublicKeyY:    y,
		},
		"partial registration metadata": {
			UserAddress:   victim.Hex(),
			SignedBatch:   victimBatch,
			Signature:     sign(victimKey, victim, victimBatch),
			SignatureType: "secp256k1",
			CredentialID:  "no-coordinates",
		},
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(body)
			req := httptest.NewRequest(http.MethodPost, "/api/v2/relayer/relay", bytes.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			res := httptest.NewRecorder()
			a.router.ServeHTTP(res, req)
			if res.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", res.Code, res.Body.String())
			}
		})
	}
}

func relayerDigest(user common.Address, batch apiresp.CaliburSignedBatch) ([]byte, error) {
	nonce, _ := new(big.Int).SetString(batch.Nonce, 10)
	deadline, _ := new(big.Int).SetString(batch.Deadline, 10)
	calls := make([]gocalibur.Call, 0, len(batch.BatchedCall.Calls))
	for _, c := range batch.BatchedCall.Calls {
		value, _ := new(big.Int).SetString(c.Value, 10)
		calls = append(calls, gocalibur.Call{To: common.HexToAddress(c.To), Value: value, Data: common.FromHex(c.Data)})
	}
	signed := gocalibur.SignedBatchedCall{
		BatchedCall: gocalibur.BatchedCall{Calls: calls, RevertOnFailure: batch.BatchedCall.RevertOnFailure},
		Nonce:       nonce,
		KeyHash:     common.HexToHash(batch.KeyHash),
		Executor:    common.HexToAddress(batch.Executor),
		Deadline:    deadline,
	}
	d := gocalibur.ComputeDigest(gocalibur.CaliburDomain(big.NewInt(100), user, common.HexToAddress(webAuthnTestCalibur)), signed)
	return d[:], nil
}
