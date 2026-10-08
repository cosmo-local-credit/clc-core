package api

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/stretchr/testify/require"
)

// End-to-end coverage of idempotent POST /relayer/relay: the real router,
// store and an insert-only river client against CORE_TEST_DSN. Skipped when it
// is unset:
//
//	CORE_TEST_DSN=postgres://postgres:postgres@127.0.0.1:5432/clc_core go test ./internal/api/ -run RelayOperation
//
// Every job and operation row a test creates is deleted again in cleanup.

type (
	relayOperationResponse struct {
		Ok          bool           `json:"ok"`
		ErrCode     string         `json:"code"`
		Description string         `json:"description"`
		Result      map[string]any `json:"result"`
	}

	// relayJobTracker deletes every RELAY job and operation row for the tracking
	// ids a test collects, and counts the jobs queued under one of them.
	relayJobTracker struct {
		t           *testing.T
		a           *API
		trackingIDs []string
		operations  []string
	}
)

func newRelayOperationAPI(t *testing.T) *API {
	t.Helper()

	dsn := os.Getenv("CORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CORE_TEST_DSN not set; skipping live relay operation tests")
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

	ctx := context.Background()
	migrator, err := rivermigrate.New(riverpgxv5.New(pgStore.Pool()), nil)
	require.NoError(t, err)
	_, err = migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	require.NoError(t, err)

	// No workers: insert-only, so queued jobs stay queued for the assertions.
	queue, err := river.NewClient[pgx.Tx](riverpgxv5.New(pgStore.Pool()), &river.Config{})
	require.NoError(t, err)

	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	return New(APIOpts{
		Logg:         logg,
		CORS:         []string{"https://app.example"},
		Store:        pgStore,
		SigningKey:   priv,
		VerifyingKey: pub,
		QueueClient:  queue,
		ChainID:      100,
	})
}

func relayOperationBody(operationID, nonce string) map[string]any {
	body := map[string]any{
		"userAddress": "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266",
		"signedBatch": map[string]any{
			"batchedCall": map[string]any{
				"calls": []map[string]any{{
					"to":    "0x70997970C51812dc3A010C7d01b50e0d17dc79C8",
					"value": "0",
					"data":  "0xa9059cbb",
				}},
				"revertOnFailure": true,
			},
			"nonce":    nonce,
			"keyHash":  "0x0000000000000000000000000000000000000000000000000000000000000000",
			"executor": "0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC",
			"deadline": "1700000300",
		},
		"signature":     "0xabcdef",
		"signatureType": "secp256k1",
	}
	if operationID != "" {
		body["operationId"] = operationID
	}
	return body
}

func postRelay(t *testing.T, a *API, body map[string]any) (int, relayOperationResponse) {
	t.Helper()
	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/v2/relayer/relay", strings.NewReader(string(encoded)))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, req)
	var envelope relayOperationResponse
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &envelope), res.Body.String())
	return res.Code, envelope
}

func newRelayJobTracker(t *testing.T, a *API) *relayJobTracker {
	tracker := &relayJobTracker{t: t, a: a}
	t.Cleanup(func() {
		ctx := context.Background()
		for _, id := range tracker.trackingIDs {
			_, err := a.store.Pool().Exec(ctx, `DELETE FROM river_job WHERE kind = 'RELAY' AND args->>'trackingId' = $1`, id)
			require.NoError(t, err)
		}
		for _, id := range tracker.operations {
			_, err := a.store.Pool().Exec(ctx, `DELETE FROM relay_operations WHERE operation_id = $1::uuid`, id)
			require.NoError(t, err)
		}
	})
	return tracker
}

func (r *relayJobTracker) operation() string {
	id := uuid.NewString()
	r.operations = append(r.operations, id)
	return id
}

func (r *relayJobTracker) track(id string) { r.trackingIDs = append(r.trackingIDs, id) }

func (r *relayJobTracker) jobs(trackingID string) int {
	r.t.Helper()
	var count int
	require.NoError(r.t, r.a.store.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE kind = 'RELAY' AND args->>'trackingId' = $1`, trackingID,
	).Scan(&count))
	return count
}

func TestRelayOperationReplayReturnsTheOriginalTrackingID(t *testing.T) {
	a := newRelayOperationAPI(t)
	jobs := newRelayJobTracker(t, a)
	operationID := jobs.operation()

	status, first := postRelay(t, a, relayOperationBody(operationID, "7"))
	require.Equal(t, http.StatusOK, status, first.Description)
	trackingID, _ := first.Result["trackingId"].(string)
	require.NotEmpty(t, trackingID)
	jobs.track(trackingID)
	require.Equal(t, false, first.Result["replayed"])
	require.Equal(t, operationID, first.Result["operationId"])

	status, replay := postRelay(t, a, relayOperationBody(operationID, "7"))
	require.Equal(t, http.StatusOK, status, replay.Description)
	require.Equal(t, trackingID, replay.Result["trackingId"])
	require.Equal(t, true, replay.Result["replayed"])
	require.Equal(t, "Relay request already accepted", replay.Description)

	require.Equal(t, 1, jobs.jobs(trackingID), "a replay must not queue a second job")
}

func TestRelayOperationRefusesReuseForADifferentRequest(t *testing.T) {
	a := newRelayOperationAPI(t)
	jobs := newRelayJobTracker(t, a)
	operationID := jobs.operation()

	status, first := postRelay(t, a, relayOperationBody(operationID, "7"))
	require.Equal(t, http.StatusOK, status, first.Description)
	trackingID, _ := first.Result["trackingId"].(string)
	jobs.track(trackingID)

	status, reused := postRelay(t, a, relayOperationBody(operationID, "8"))
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "E16", reused.ErrCode)
	require.Nil(t, reused.Result, "a refused reuse must reveal nothing about the original")
	require.NotContains(t, reused.Description, trackingID)

	var queued int
	require.NoError(t, a.store.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE kind = 'RELAY' AND args->'signedBatch'->>'nonce' = '8' AND args->>'userAddress' = $1 AND created_at > now() - interval '1 minute'`,
		"0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266",
	).Scan(&queued))
	require.Zero(t, queued, "a refused reuse must not queue a job")
}

func TestRelayOperationConcurrentDuplicatesQueueOneJob(t *testing.T) {
	a := newRelayOperationAPI(t)
	jobs := newRelayJobTracker(t, a)
	operationID := jobs.operation()

	const submissions = 8
	trackingIDs := make([]string, submissions)
	statuses := make([]int, submissions)
	var wg sync.WaitGroup
	for i := range submissions {
		wg.Go(func() {
			status, envelope := postRelay(t, a, relayOperationBody(operationID, "7"))
			statuses[i] = status
			trackingIDs[i], _ = envelope.Result["trackingId"].(string)
		})
	}
	wg.Wait()

	for i := range submissions {
		require.Equal(t, http.StatusOK, statuses[i])
		require.Equal(t, trackingIDs[0], trackingIDs[i], "every duplicate answers with the winner's tracking id")
	}
	jobs.track(trackingIDs[0])
	require.Equal(t, 1, jobs.jobs(trackingIDs[0]))
}

func TestRelayWithoutOperationIDIsUnchanged(t *testing.T) {
	a := newRelayOperationAPI(t)
	jobs := newRelayJobTracker(t, a)

	status, first := postRelay(t, a, relayOperationBody("", "7"))
	require.Equal(t, http.StatusOK, status, first.Description)
	status, second := postRelay(t, a, relayOperationBody("", "7"))
	require.Equal(t, http.StatusOK, status, second.Description)

	firstID, _ := first.Result["trackingId"].(string)
	secondID, _ := second.Result["trackingId"].(string)
	jobs.track(firstID)
	jobs.track(secondID)
	require.NotEqual(t, firstID, secondID)
	require.NotContains(t, first.Result, "replayed")
	require.NotContains(t, first.Result, "operationId")
}

func TestRelayOperationIDMustBeAUUID(t *testing.T) {
	a := newRelayOperationAPI(t)

	status, envelope := postRelay(t, a, relayOperationBody("not-a-uuid", "7"))
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "E04", envelope.ErrCode)
}
