package store

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestSafeSignersOrderingAndAssetOwnership(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()
	poolID, voucherID, registry, _, _, pool, token := seedComposablePool(t, tx)
	safeAddr, a, b, c, stranger := randomAddress(t, tx), randomAddress(t, tx), randomAddress(t, tx), randomAddress(t, tx), randomAddress(t, tx)

	_, err := pg.GetSafeState(ctx, tx, 42220, safeAddr)
	require.ErrorIs(t, err, pgx.ErrNoRows, "an unseen address is not a Safe")

	ok, err := pg.ReplaceSafeSigners(ctx, tx, 42220, safeAddr, []string{a, b}, 2, 10, 1)
	require.NoError(t, err)
	require.True(t, ok)

	// Stale and replayed positions never roll the set back.
	for _, pos := range [][2]uint64{{10, 1}, {10, 0}, {9, 99}} {
		ok, err = pg.ReplaceSafeSigners(ctx, tx, 42220, safeAddr, []string{c}, 1, pos[0], pos[1])
		require.NoError(t, err)
		require.False(t, ok)
	}
	ok, err = pg.ReplaceSafeSigners(ctx, tx, 42220, safeAddr, []string{b, c}, 1, 11, 0)
	require.NoError(t, err)
	require.True(t, ok)

	state, err := pg.GetSafeState(ctx, tx, 42220, safeAddr)
	require.NoError(t, err)
	require.Equal(t, 1, state.Threshold)
	require.ElementsMatch(t, []string{lower(b), lower(c)}, state.Signers, "a removed signer is gone")

	// Reconciliation fences a block but yields to a later event.
	ok, err = pg.ReplaceSafeSigners(ctx, tx, 42220, safeAddr, []string{a}, 1, 12, math.MaxInt64)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = pg.ReplaceSafeSigners(ctx, tx, 42220, safeAddr, []string{b}, 1, 12, 5)
	require.NoError(t, err)
	require.False(t, ok)

	// Other chains are separate.
	_, err = pg.GetSafeState(ctx, tx, 100, safeAddr)
	require.ErrorIs(t, err, pgx.ErrNoRows)

	// Invalid sets are refused outright.
	for _, bad := range []struct {
		signers   []string
		threshold int
	}{{[]string{a}, 2}, {[]string{a, a}, 1}, {[]string{"nope"}, 1}, {[]string{a}, -1}} {
		_, err = pg.ReplaceSafeSigners(ctx, tx, 42220, safeAddr, bad.signers, bad.threshold, 99, 0)
		require.Error(t, err)
	}
	_, err = pg.ReplaceSafeSigners(ctx, tx, 100, safeAddr, []string{a}, 0, 10, 0)
	require.NoError(t, err, "a compatible contract can have an unknown threshold")

	// Asset management covers the voucher, the pool and its wiring, only for
	// assets the Safe owns.
	_, err = tx.Exec(ctx, `UPDATE pools SET owner_address=$2 WHERE id=$1`, poolID, safeAddr)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `UPDATE vouchers SET owner_address=$2 WHERE id=$1`, voucherID, safeAddr)
	require.NoError(t, err)
	for _, target := range []string{pool, token, registry} {
		managed, err := pg.SafeManagesAsset(ctx, tx, 42220, safeAddr, target)
		require.NoError(t, err)
		require.True(t, managed, target)
	}
	managed, err := pg.SafeManagesAsset(ctx, tx, 42220, stranger, pool)
	require.NoError(t, err)
	require.False(t, managed, "a Safe does not manage assets it does not own")
	managed, err = pg.SafeManagesAsset(ctx, tx, 42220, safeAddr, stranger)
	require.NoError(t, err)
	require.False(t, managed)
}

func lower(s string) string { return strings.ToLower(s) }
