package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// Live-database coverage for relay operation claims. Skipped unless
// CORE_TEST_DSN points at a database. The race cases need two real
// transactions, so rows are committed and deleted again in cleanup rather than
// living inside one rolled-back harness transaction.

func newRelayOperationTestPool(t *testing.T) (*Pg, *pgxpool.Pool) {
	t.Helper()
	pg, pool := newCatalogTestPool(t)
	t.Cleanup(pool.Close)
	return pg, pool
}

func relayOperationFixture(t *testing.T, pool *pgxpool.Pool) RelayOperation {
	t.Helper()
	op := RelayOperation{
		OperationID:   uuid.NewString(),
		UserAddress:   "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266",
		RequestDigest: []byte{1, 2, 3, 4},
		TrackingID:    uuid.NewString(),
	}
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `DELETE FROM relay_operations WHERE operation_id = $1::uuid`, op.OperationID)
		require.NoError(t, err)
	})
	return op
}

func claimInOwnTx(t *testing.T, pg *Pg, pool *pgxpool.Pool, op RelayOperation) (RelayOperation, bool) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	got, claimed, err := pg.ClaimRelayOperation(ctx, tx, op)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
	return got, claimed
}

func TestClaimRelayOperationReturnsTheFirstClaim(t *testing.T) {
	pg, pool := newRelayOperationTestPool(t)
	op := relayOperationFixture(t, pool)

	got, claimed := claimInOwnTx(t, pg, pool, op)
	require.True(t, claimed)
	require.Equal(t, op.TrackingID, got.TrackingID)

	retry := op
	retry.TrackingID = uuid.NewString()
	got, claimed = claimInOwnTx(t, pg, pool, retry)
	require.False(t, claimed, "a second claim of the same id must not insert")
	require.Equal(t, op.TrackingID, got.TrackingID, "the original tracking id is returned")
	require.Equal(t, op.RequestDigest, got.RequestDigest)
	require.Equal(t, op.UserAddress, got.UserAddress)
}

// Two submissions racing on one id: the second blocks on the first's
// uncommitted row and, once it commits, reads it instead of failing.
func TestClaimRelayOperationConcurrentClaimWaitsForTheWinner(t *testing.T) {
	pg, pool := newRelayOperationTestPool(t)
	op := relayOperationFixture(t, pool)
	ctx := context.Background()

	first, err := pool.Begin(ctx)
	require.NoError(t, err)
	_, claimed, err := pg.ClaimRelayOperation(ctx, first, op)
	require.NoError(t, err)
	require.True(t, claimed)

	type outcome struct {
		op      RelayOperation
		claimed bool
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		second, err := pool.Begin(ctx)
		if err != nil {
			done <- outcome{err: err}
			return
		}
		defer second.Rollback(ctx)
		loser := op
		loser.TrackingID = uuid.NewString()
		got, claimed, err := pg.ClaimRelayOperation(ctx, second, loser)
		done <- outcome{op: got, claimed: claimed, err: err}
	}()

	select {
	case <-done:
		t.Fatal("the concurrent claim must wait for the first transaction")
	case <-time.After(200 * time.Millisecond):
	}

	require.NoError(t, first.Commit(ctx))
	result := <-done
	require.NoError(t, result.err)
	require.False(t, result.claimed)
	require.Equal(t, op.TrackingID, result.op.TrackingID)
}

// When the first submission rolls back (its job insert failed), the claim goes
// with it and the waiting submission takes the id.
func TestClaimRelayOperationTakesOverARolledBackClaim(t *testing.T) {
	pg, pool := newRelayOperationTestPool(t)
	op := relayOperationFixture(t, pool)
	ctx := context.Background()

	first, err := pool.Begin(ctx)
	require.NoError(t, err)
	_, claimed, err := pg.ClaimRelayOperation(ctx, first, op)
	require.NoError(t, err)
	require.True(t, claimed)

	takeover := op
	takeover.TrackingID = uuid.NewString()
	done := make(chan bool, 1)
	go func() {
		second, err := pool.Begin(ctx)
		if err != nil {
			done <- false
			return
		}
		_, claimed, err := pg.ClaimRelayOperation(ctx, second, takeover)
		if err != nil {
			_ = second.Rollback(ctx)
			done <- false
			return
		}
		done <- claimed && second.Commit(ctx) == nil
	}()

	time.Sleep(100 * time.Millisecond)
	require.NoError(t, first.Rollback(ctx))
	require.True(t, <-done, "the waiting claim inserts once the first rolls back")

	got, claimed := claimInOwnTx(t, pg, pool, op)
	require.False(t, claimed)
	require.Equal(t, takeover.TrackingID, got.TrackingID)
}
