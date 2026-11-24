package worker

import (
	"context"
	"crypto/rand"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/safe"
	"github.com/cosmo-local-credit/clc-core/pkg/event"
	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"
)

type (
	recordingApprovalSTS struct {
		t      *testing.T
		hashes []string
		err    error
	}
)

func TestSafeApprovalSyncUniquenessExpiresAfterCompletion(t *testing.T) {
	states := safeApprovalSyncInsertOpts().UniqueOpts.ByState
	require.Contains(t, states, rivertype.JobStateRunning)
	require.Contains(t, states, rivertype.JobStateRetryable)
	require.NotContains(t, states, rivertype.JobStateCompleted,
		"a completed sync must not suppress a later confirmation")
}

func TestSafeApprovalSyncQueuesEachSignerWhileAnEarlierSyncIsRunning(t *testing.T) {
	dsn := os.Getenv("CORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CORE_TEST_DSN not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	driver := riverpgxv5.New(pool)
	migrator, err := rivermigrate.New(driver, nil)
	require.NoError(t, err)
	_, err = migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	require.NoError(t, err)
	queue, err := river.NewClient(driver, &river.Config{})
	require.NoError(t, err)

	var hashBytes [32]byte
	_, err = rand.Read(hashBytes[:])
	require.NoError(t, err)
	hash := common.Hash(hashBytes).Hex()
	kind := SafeApprovalSyncArgs{}.Kind()
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `DELETE FROM river_job WHERE kind=$1 AND args->>'safeTxHash'=$2`, kind, hash)
		require.NoError(t, err)
	})

	firstSigner := common.HexToAddress("0xabcdef").Hex()
	secondSigner := common.HexToAddress("0x123abc").Hex()
	require.NoError(t, EnqueueSafeApprovalSync(ctx, queue, executeSafe, hash, firstSigner))
	tag, err := pool.Exec(ctx, `UPDATE river_job SET state='running' WHERE kind=$1 AND args->>'safeTxHash'=$2`, kind, hash)
	require.NoError(t, err)
	require.EqualValues(t, 1, tag.RowsAffected())

	require.NoError(t, EnqueueSafeApprovalSync(ctx, queue, executeSafe, hash, strings.ToLower(firstSigner)))
	require.NoError(t, EnqueueSafeApprovalSync(ctx, queue, executeSafe, hash, secondSigner))

	var signers []string
	require.NoError(t, pool.QueryRow(ctx, `SELECT array_agg(args->>'signer') FROM river_job WHERE kind=$1 AND args->>'safeTxHash'=$2`, kind, hash).Scan(&signers))
	require.ElementsMatch(t, []string{strings.ToLower(firstSigner), strings.ToLower(secondSigner)}, signers)
}

func (s *recordingApprovalSTS) Transaction(_ context.Context, hash string) (*safe.Transaction, error) {
	s.hashes = append(s.hashes, hash)
	return nil, s.err
}

func (s *recordingApprovalSTS) Queued(context.Context, string, string) ([]safe.Transaction, error) {
	s.t.Fatal("unexpected Safe queue scan")
	return nil, nil
}

func TestLegacySafeApprovalJobsDrainWithoutScanning(t *testing.T) {
	ctx := t.Context()
	require.NoError(t, (&SafeApprovalReconcileWorker{}).Work(ctx, &river.Job[SafeApprovalReconcileArgs]{}))

	sts := &recordingApprovalSTS{t: t}
	worker := &SafeApprovalSyncWorker{sts: sts}
	require.NoError(t, worker.Work(ctx, &river.Job[SafeApprovalSyncArgs]{
		Args: SafeApprovalSyncArgs{SafeAddress: executeSafe},
	}))
	require.Empty(t, sts.hashes)
}

func TestSafeApprovalSyncReadsOnlyTheTriggeredTransaction(t *testing.T) {
	sts := &recordingApprovalSTS{t: t}
	worker := &SafeApprovalSyncWorker{sts: sts}
	job := &river.Job[SafeApprovalSyncArgs]{Args: SafeApprovalSyncArgs{
		SafeAddress: executeSafe,
		SafeTxHash:  executeHash,
	}}
	require.NoError(t, worker.Work(t.Context(), job))
	require.Equal(t, []string{executeHash}, sts.hashes)

	sts.err = &safe.QuotaLimitError{Monthly: true, RetryAt: time.Now().Add(time.Hour)}
	var snooze *river.JobSnoozeError
	require.ErrorAs(t, worker.Work(t.Context(), job), &snooze)
	require.Greater(t, snooze.Duration, 59*time.Minute)
	require.LessOrEqual(t, snooze.Duration, time.Hour)
}

func TestSafeApprovalStage(t *testing.T) {
	tests := []struct {
		index, required int
		kind            string
		proposed        bool
	}{
		{0, 2, event.SafeProposed, true},
		{1, 3, event.SafeConfirmed, false},
		{2, 3, event.SafeApproved, false},
		{0, 1, event.SafeApproved, true},
		{3, 3, event.SafeConfirmed, false},
	}
	for _, test := range tests {
		kind, proposed := safeApprovalStage(test.index, test.required, false)
		require.Equal(t, test.kind, kind)
		require.Equal(t, test.proposed, proposed)
	}
}

func TestSafeRejectionStage(t *testing.T) {
	kind, proposed := safeApprovalStage(0, 2, true)
	require.Equal(t, event.SafeRejectionProposed, kind)
	require.True(t, proposed)
	kind, proposed = safeApprovalStage(1, 2, true)
	require.Equal(t, event.SafeRejectionApproved, kind)
	require.False(t, proposed)
}

func TestSafeApprovalTargetsDeduplicatesAndRecognizesSelfManagement(t *testing.T) {
	safeAddress := common.HexToAddress("0x1111111111111111111111111111111111111111")
	asset := common.HexToAddress("0x2222222222222222222222222222222222222222")
	policy := safe.NewPolicy(nil, nil, safe.DefaultAllowedSignatures)
	targets, allOwned := safeApprovalTargets(safeAddress, &safe.Exec{
		To: safeAddress, Value: big.NewInt(0), Data: []byte{1}, Operation: 0,
	}, policy)
	require.True(t, allOwned)
	require.Empty(t, targets)

	targets, allOwned = safeApprovalTargets(safeAddress, &safe.Exec{
		To: asset, Value: big.NewInt(0), Data: []byte{1}, Operation: 0,
	}, policy)
	require.False(t, allOwned)
	require.Equal(t, []string{strings.ToLower(asset.Hex())}, targets)
}
