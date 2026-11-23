package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/google/uuid"
)

func newOTXStatusAPI(t *testing.T) *API {
	t.Helper()

	dsn := os.Getenv("CORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CORE_TEST_DSN not set; skipping live OTX status tests")
	}

	logg := slog.New(slog.DiscardHandler)
	pgStore, err := store.NewPgStore(store.PgOpts{
		Logg:                 logg,
		DSN:                  dsn,
		MigrationsFolderPath: "../../migrations",
		QueriesFolderPath:    "../../core_queries.sql",
	})
	if err != nil {
		t.Fatalf("connect store: %v", err)
	}
	t.Cleanup(pgStore.Pool().Close)

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	return New(APIOpts{
		Logg:         logg,
		CORS:         []string{"https://app.example"},
		Store:        pgStore,
		SigningKey:   priv,
		VerifyingKey: pub,
	})
}

// seedOTXTracking writes the keystore → otx → dispatch chain the
// tracking-id read joins through, returning the tracking id.
func seedOTXTracking(t *testing.T, a *API, status, txHash string) string {
	t.Helper()
	ctx := context.Background()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}
	defer tx.Rollback(ctx)

	tracking := uuid.NewString()
	var keyID uint64
	if err := tx.QueryRow(ctx, `
		INSERT INTO keystore (public_key, private_key, active)
		VALUES ('0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'), 'otx-status-test', true)
		RETURNING id`).Scan(&keyID); err != nil {
		t.Fatalf("seed keystore: %v", err)
	}
	var otxID uint64
	if err := tx.QueryRow(ctx, `
		INSERT INTO otx (tracking_id, otx_type, signer_account, raw_tx, tx_hash, nonce)
		VALUES ($1::uuid, 'RELAY_EXEC', $2, '0xdeadbeef', $3, 0) RETURNING id`,
		tracking, keyID, txHash).Scan(&otxID); err != nil {
		t.Fatalf("seed otx: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO dispatch (otx_id, status) VALUES ($1, $2)`, otxID, status); err != nil {
		t.Fatalf("seed dispatch: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	return tracking
}

func serveOTXStatus(a *API, trackingID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v2/otx/status/"+trackingID, nil)
	req.RemoteAddr = "203.0.113.9:1234"
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, req)
	return res
}

func TestOTXStatusPublicOutcomes(t *testing.T) {
	a := newOTXStatusAPI(t)

	// Unknown tracking id → 404, never an auth challenge: the route is
	// deliberately public.
	res := serveOTXStatus(a, uuid.NewString())
	if res.Code != http.StatusNotFound {
		t.Fatalf("unknown tracking: got %d, want 404", res.Code)
	}

	pending := seedOTXTracking(t, a, "PENDING", "0x"+uuid.NewString()[:8]+"aa")
	res = serveOTXStatus(a, pending)
	if res.Code != http.StatusOK {
		t.Fatalf("pending tracking: got %d, want 200: %s", res.Code, res.Body)
	}
	var body apiresp.OKResponse
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	result := body.Result
	if result["status"] != "PENDING" {
		t.Fatalf("status = %v, want PENDING", result["status"])
	}
	if _, leaked := result["rawTx"]; leaked {
		t.Fatal("raw transaction leaked into the public status response")
	}
	if _, leaked := result["signerAccount"]; leaked {
		t.Fatal("signer account leaked into the public status response")
	}

	success := seedOTXTracking(t, a, "SUCCESS", "0x"+uuid.NewString()[:8]+"bb")
	res = serveOTXStatus(a, success)
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Result["status"] != "SUCCESS" {
		t.Fatalf("status = %v, want SUCCESS", body.Result["status"])
	}

	reverted := seedOTXTracking(t, a, "REVERTED", "")
	res = serveOTXStatus(a, reverted)
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Result["status"] != "FAILED" {
		t.Fatalf("status = %v, want FAILED", body.Result["status"])
	}
}
