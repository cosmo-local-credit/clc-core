package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// A tracking ID is handed out when a job is enqueued, before its OTX row
// exists, so "no OTX" has always meant "poll again". These cover the case that
// never resolves: a job that was discarded or cancelled writes no OTX ever, and
// the tracker has to say so instead of answering not-found for good.
//
//	CORE_TEST_DSN=postgres://postgres:postgres@127.0.0.1:5432/clc_core go test ./internal/api/ -run TrackingFailure

func getRelayerTrack(t *testing.T, a *API, trackingID string) (int, relayOperationResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v2/relayer/track/"+trackingID, nil)
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, req)
	var envelope relayOperationResponse
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &envelope), res.Body.String())
	return res.Code, envelope
}

// insertFinalizedJob writes a river_job row straight into a terminal state,
// which is what a worker that failed its last attempt leaves behind. An empty
// jobError leaves errors NULL, as a cancelled job that never ran does.
func insertFinalizedJob(t *testing.T, a *API, kind, state, trackingID, jobError string) {
	t.Helper()
	ctx := context.Background()
	args := fmt.Sprintf(`{"trackingId": %q}`, trackingID)
	var errs []string
	if jobError != "" {
		encoded, err := json.Marshal(jobError)
		require.NoError(t, err)
		errs = []string{fmt.Sprintf(`{"at": "2026-01-01T00:00:00Z", "error": %s, "attempt": 1}`, encoded)}
	}
	_, err := a.store.Pool().Exec(ctx, `
INSERT INTO river_job (kind, state, args, errors, attempt, max_attempts, finalized_at, queue, priority)
VALUES ($1, $2::river_job_state, $3::jsonb, $4::text[]::jsonb[], 1, 1, now(), 'default', 1)`,
		kind, state, args, errs)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := a.store.Pool().Exec(context.Background(),
			`DELETE FROM river_job WHERE kind = $1 AND args->>'trackingId' = $2`, kind, trackingID)
		require.NoError(t, err)
	})
}

func TestTrackingFailureReportsDiscardedJob(t *testing.T) {
	a := newRelayOperationAPI(t)
	trackingID := uuid.NewString()
	const reason = "delegate: authorization contract 0xaaa != configured calibur 0xbbb"
	insertFinalizedJob(t, a, "RELAY_DELEGATE", "discarded", trackingID, reason)

	status, body := getRelayerTrack(t, a, trackingID)

	// 410, not 404: the ID was real and its outcome is final.
	require.Equal(t, http.StatusGone, status)
	require.False(t, body.Ok)
	require.Equal(t, "E22", body.ErrCode)
	// The job's own error is the whole point — it names what to fix.
	require.Equal(t, reason, body.Description)
}

func TestTrackingFailureReportsCancelledJob(t *testing.T) {
	a := newRelayOperationAPI(t)
	trackingID := uuid.NewString()
	insertFinalizedJob(t, a, "RELAY_DELEGATE", "cancelled", trackingID, "")

	status, body := getRelayerTrack(t, a, trackingID)

	require.Equal(t, http.StatusGone, status)
	require.Equal(t, "E22", body.ErrCode)
	// With no recorded error, the state is still worth naming.
	require.Equal(t, "tracking id RELAY_DELEGATE was cancelled", body.Description)
}

func TestTrackingFailureLeavesAnUnknownIDNotFound(t *testing.T) {
	a := newRelayOperationAPI(t)

	status, body := getRelayerTrack(t, a, uuid.NewString())

	// Nothing was ever queued under it, so the old answer still stands.
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "E03", body.ErrCode)
}

func TestTrackingFailureKeepsPollingAQueuedJob(t *testing.T) {
	a := newRelayOperationAPI(t)
	trackingID := uuid.NewString()
	ctx := context.Background()
	// Enqueued and waiting: no OTX row yet, and nothing has failed.
	_, err := a.store.Pool().Exec(ctx, `
INSERT INTO river_job (kind, state, args, attempt, max_attempts, queue, priority)
VALUES ('RELAY_DELEGATE', 'available'::river_job_state, $1::jsonb, 0, 1, 'default', 1)`,
		fmt.Sprintf(`{"trackingId": %q}`, trackingID))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := a.store.Pool().Exec(context.Background(),
			`DELETE FROM river_job WHERE args->>'trackingId' = $1`, trackingID)
		require.NoError(t, err)
	})

	status, body := getRelayerTrack(t, a, trackingID)

	// Still pending, so the caller must keep polling rather than give up.
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "E03", body.ErrCode)
}
