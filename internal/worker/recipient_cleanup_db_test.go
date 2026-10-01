package worker

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/keypair"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"
)

type cleanupFailureStore struct {
	store.Store

	pool         *pgxpool.Pool
	cleanupCalls int
	nonceCalls   int
}

func (s *cleanupFailureStore) Pool() *pgxpool.Pool { return s.pool }

func (s *cleanupFailureStore) ReconcileDeploymentIdentities(ctx context.Context, tx pgx.Tx) error {
	s.cleanupCalls++
	// A SQL failure aborts this transaction, so recovery must use a fresh one.
	_, err := tx.Exec(ctx, "SELECT 1/0")
	return err
}

func (s *cleanupFailureStore) LoadMasterSignerKey(context.Context, pgx.Tx) (keypair.Key, error) {
	return keypair.Key{Public: "0x1111111111111111111111111111111111111111"}, nil
}

func (s *cleanupFailureStore) GetUnsettledOTXBySigner(ctx context.Context, tx pgx.Tx, _ string) ([]*store.OTX, error) {
	s.nonceCalls++
	var value int
	err := tx.QueryRow(ctx, "SELECT 1").Scan(&value)
	return nil, err
}

func TestNonceReconcilerContinuesAfterIdentityCleanupFails(t *testing.T) {
	dsn := os.Getenv("CORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CORE_TEST_DSN not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()
	s := &cleanupFailureStore{pool: pool}
	w := &NonceReconcilerWorker{wc: &WorkerContainer{store: s, logg: slog.New(slog.DiscardHandler)}}
	require.NoError(t, w.Work(ctx, &river.Job[NonceReconcilerArgs]{}))
	require.Equal(t, 1, s.cleanupCalls)
	require.Equal(t, 1, s.nonceCalls, "cleanup failure must not prevent loading nonce work")
	require.NoError(t, w.Work(ctx, &river.Job[NonceReconcilerArgs]{
		Args: NonceReconcilerArgs{Signer: "0x2222222222222222222222222222222222222222"},
	}))
	require.Equal(t, 1, s.cleanupCalls, "per-signer jobs must skip global identity cleanup")
	require.Equal(t, 2, s.nonceCalls)
}
