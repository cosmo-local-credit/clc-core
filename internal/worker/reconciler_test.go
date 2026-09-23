package worker

import (
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDecideReconcileActionReplaysTheIncident walks the exact shape of the
// 2026-09-04 stall: chain nonce 3, otx rows 3..15 all recorded IN_NETWORK,
// none of them ever in a pool. Nonce 3 is the head and the only one that may
// be republished; 4..15 are correctly waiting behind it.
func TestDecideReconcileActionReplaysTheIncident(t *testing.T) {
	const chainNonce = 3

	headTaken := false
	actions := map[uint64]reconcileAction{}

	for nonce := uint64(3); nonce <= 15; nonce++ {
		action := decideReconcileAction(nonce, chainNonce, false, headTaken)
		if action == actionRepublish {
			headTaken = true
		}
		actions[nonce] = action
	}

	assert.Equal(t, actionRepublish, actions[3], "nonce 3 is the head the chain is waiting for")
	for nonce := uint64(4); nonce <= 15; nonce++ {
		assert.Equal(t, actionWait, actions[nonce],
			"nonce %d must wait, not be pushed in behind an unqueued head", nonce)
	}
}

func TestDecideReconcileActionSettlesOnReceipt(t *testing.T) {
	// A receipt wins over everything, including a head that would otherwise be
	// republished. This is the case that left rows at IN_NETWORK for 34
	// minutes after their transaction had already succeeded.
	assert.Equal(t, actionSettle, decideReconcileAction(15, 16, true, false))
	assert.Equal(t, actionSettle, decideReconcileAction(3, 3, true, false))
	assert.Equal(t, actionSettle, decideReconcileAction(3, 3, true, true))
}

func TestDecideReconcileActionSupersedesConsumedNonces(t *testing.T) {
	// The chain moved past nonce 5 and nothing here has a receipt for it, so
	// some other transaction consumed it. Never republish: the bytes would be
	// rejected as "nonce too low" forever.
	assert.Equal(t, actionSupersede, decideReconcileAction(5, 9, false, false))
	assert.Equal(t, actionSupersede, decideReconcileAction(0, 16, false, true))
}

func TestDecideReconcileActionRepublishesExactlyOneHead(t *testing.T) {
	assert.Equal(t, actionRepublish, decideReconcileAction(7, 7, false, false))
	assert.Equal(t, actionWait, decideReconcileAction(7, 7, false, true))

	// A gap above the chain nonce is still only a candidate head once nothing
	// below it claimed the slot — the store never allocates gaps, but a
	// superseded row can leave one.
	assert.Equal(t, actionRepublish, decideReconcileAction(9, 7, false, false))
}

// TestReconcilerIsUniquePerSigner — the dispatcher, an exhausted receipt
// poller and the periodic sweep all enqueue this job. Without uniqueness they
// pile up and contend on the same advisory lock.
func TestReconcilerIsUniquePerSigner(t *testing.T) {
	opts := NonceReconcilerArgs{}.InsertOpts()

	assert.True(t, opts.UniqueOpts.ByArgs, "must dedupe on the signer argument")
	assert.Equal(t, 3, opts.MaxAttempts)

	// River rejects a custom ByState that omits any of its required states,
	// and the periodic enqueuer reports that as an internal error rather than
	// failing loudly — so the sweep silently never runs. Observed in
	// production: "UniqueOpts.ByState must contain all required states,
	// missing: pending".
	for _, required := range []rivertype.JobState{
		rivertype.JobStateAvailable,
		rivertype.JobStatePending,
		rivertype.JobStateRunning,
		rivertype.JobStateScheduled,
	} {
		assert.Contains(t, opts.UniqueOpts.ByState, required,
			"River requires %q in a custom UniqueOpts.ByState", required)
	}

	// Completed must stay out, or one reconcile per signer would suppress
	// every later sweep for the lifetime of the database.
	assert.NotContains(t, opts.UniqueOpts.ByState, rivertype.JobStateCompleted)
}

func TestNonceReconcilerKind(t *testing.T) {
	assert.Equal(t, "NONCE_RECONCILER", NonceReconcilerArgs{}.Kind())

	// Registered as a River worker, or the periodic sweep silently never runs.
	var _ river.Worker[NonceReconcilerArgs] = &NonceReconcilerWorker{}
	workers, err := setupWorkers(&WorkerContainer{})
	require.NoError(t, err)
	require.NotNil(t, workers)
}
