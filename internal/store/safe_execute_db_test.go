package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/stretchr/testify/require"
)

type (
	// safeExecuteJob restates the worker's job: store tests cannot import worker.
	safeExecuteJob struct {
		SafeTxHash string `json:"safeTxHash"`
		Safe       string `json:"safe"`
	}
)

func (safeExecuteJob) Kind() string { return "SAFE_EXECUTE" }

func TestSafeExecutionsQueuedFollowsTheJobStateDB(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()
	// The market fixture has no pool; River's migrator and client need one.
	pool, err := pgxpool.New(ctx, os.Getenv("CORE_TEST_DSN"))
	require.NoError(t, err)
	defer pool.Close()
	driver := riverpgxv5.New(pool)
	migrator, err := rivermigrate.New(driver, nil)
	require.NoError(t, err)
	_, err = migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	require.NoError(t, err)
	queue, err := river.NewClient(driver, &river.Config{})
	require.NoError(t, err)

	queued, cancelled, unknown := randomHash(t), randomHash(t), randomHash(t)
	for _, hash := range []string{queued, cancelled} {
		_, err := queue.InsertTx(ctx, tx, safeExecuteJob{SafeTxHash: hash, Safe: "0x1111111111111111111111111111111111111111"}, nil)
		require.NoError(t, err)
	}
	_, err = tx.Exec(ctx, `UPDATE river_job SET state='cancelled', finalized_at=now() WHERE kind='SAFE_EXECUTE' AND args->>'safeTxHash'=$1`, cancelled)
	require.NoError(t, err)

	got, err := pg.SafeExecutionsQueued(ctx, tx, []string{queued, cancelled, unknown})
	require.NoError(t, err)
	require.Equal(t, map[string]bool{queued: true}, got, "a cancelled execution leaves the Safe app as the way forward")
}

func randomHash(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return "0x" + hex.EncodeToString(b)
}
