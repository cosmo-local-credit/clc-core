package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestVoucherReviewPaginationAndExpiry(t *testing.T) {
	_, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()
	voucherID, _ := seedEditableVoucher(t, tx)
	for i := range 25 {
		author := fmt.Sprintf("0x%040x", i+1)
		require.NoError(t, SaveVoucherReview(ctx, tx, voucherID, author, i%5+1, "Experience", time.Now().Add(time.Hour)))
	}
	page, err := ListVoucherReviews(ctx, tx, voucherID, 0, 0)
	require.NoError(t, err)
	require.Equal(t, 25, page.Total)
	require.InDelta(t, 3, page.Average, 0.001)
	require.Len(t, page.Reviews, 20)
	require.NotNil(t, page.Next)
	next, err := ListVoucherReviews(ctx, tx, voucherID, 0, *page.Next)
	require.NoError(t, err)
	require.Len(t, next.Reviews, 5)
	require.Nil(t, next.Next)
	require.Greater(t, *page.Next, next.Reviews[0].ID)
	filtered, err := ListVoucherReviews(ctx, tx, voucherID, 1, 0)
	require.NoError(t, err)
	require.Len(t, filtered.Reviews, 5)
	require.Equal(t, page.Counts, filtered.Counts, "filter never changes the histogram")
	// SQL must refuse both inserts and updates after the deadline.
	expired := time.Now().Add(-time.Second)
	require.ErrorIs(t, SaveVoucherReview(ctx, tx, voucherID, fmt.Sprintf("0x%040x", 1), 5, "Expired edit", expired), pgx.ErrNoRows)
	require.ErrorIs(t, SaveVoucherReview(ctx, tx, voucherID, fmt.Sprintf("0x%040x", 99), 5, "Expired insert", expired), pgx.ErrNoRows)
	originalID := next.Reviews[len(next.Reviews)-1].ID
	require.NoError(t, SaveVoucherReview(ctx, tx, voucherID, fmt.Sprintf("0x%040x", 1), 5, "New experience", time.Now().Add(time.Hour)))
	updated, err := ListVoucherReviews(ctx, tx, voucherID, 0, *page.Next)
	require.NoError(t, err)
	require.Equal(t, originalID, updated.Reviews[len(updated.Reviews)-1].ID, "edits preserve original date order")
	require.Equal(t, 25, updated.Total, "case-normalized upsert keeps one review")
}

func TestPoolReviewPaginationAndExpiry(t *testing.T) {
	_, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()
	_, poolAddress := seedEditablePool(t, tx)
	pool, err := GetReviewPool(ctx, tx, poolAddress, false)
	require.NoError(t, err)

	for i := range 25 {
		author := fmt.Sprintf("0x%040x", i+1)
		require.NoError(t, SavePoolReview(ctx, tx, pool.ID, author, i%5+1, "Swap experience", time.Now().Add(time.Hour)))
	}
	page, err := ListPoolReviews(ctx, tx, pool.ID, 0, 0)
	require.NoError(t, err)
	require.Equal(t, 25, page.Total)
	require.InDelta(t, 3, page.Average, 0.001)
	require.Len(t, page.Reviews, 20)
	require.NotNil(t, page.Next)
	next, err := ListPoolReviews(ctx, tx, pool.ID, 0, *page.Next)
	require.NoError(t, err)
	require.Len(t, next.Reviews, 5)
	require.Nil(t, next.Next)
	filtered, err := ListPoolReviews(ctx, tx, pool.ID, 1, 0)
	require.NoError(t, err)
	require.Len(t, filtered.Reviews, 5)
	require.Equal(t, page.Counts, filtered.Counts, "filter never changes the histogram")

	expired := time.Now().Add(-time.Second)
	require.ErrorIs(t, SavePoolReview(ctx, tx, pool.ID, fmt.Sprintf("0x%040x", 1), 5, "Expired edit", expired), pgx.ErrNoRows)
	require.ErrorIs(t, SavePoolReview(ctx, tx, pool.ID, fmt.Sprintf("0x%040x", 99), 5, "Expired insert", expired), pgx.ErrNoRows)
	require.NoError(t, SavePoolReview(ctx, tx, pool.ID, fmt.Sprintf("0x%040x", 1), 5, "New swap", time.Now().Add(time.Hour)))
	updated, err := ListPoolReviews(ctx, tx, pool.ID, 0, 0)
	require.NoError(t, err)
	require.Equal(t, 25, updated.Total, "case-normalized upsert keeps one review")
}

func TestReviewFlagsIncludeVoucherAndPoolTargets(t *testing.T) {
	_, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()
	voucherID, voucherAddress := seedEditableVoucher(t, tx)
	poolAddress := randomAddress(t, tx)
	var poolID uint64
	require.NoError(t, tx.QueryRow(ctx, `INSERT INTO pools (chain_id, address, name, symbol, decimals)
		VALUES (42220, $1, 'Review Pool', 'RVP', 6) RETURNING id`, poolAddress).Scan(&poolID))
	reviewer := fmt.Sprintf("0x%040x", 7)
	reporter := fmt.Sprintf("0x%040x", 8)
	deadline := time.Now().Add(time.Hour)

	require.NoError(t, SaveVoucherReview(ctx, tx, voucherID, reviewer, 4, "Voucher review", deadline))
	require.NoError(t, SavePoolReview(ctx, tx, poolID, reviewer, 5, "Pool review", deadline))
	var voucherReviewID, poolReviewID uint64
	require.NoError(t, tx.QueryRow(ctx, `SELECT id FROM voucher_reviews WHERE voucher_id=$1`, voucherID).Scan(&voucherReviewID))
	require.NoError(t, tx.QueryRow(ctx, `SELECT id FROM pool_reviews WHERE pool_id=$1`, poolID).Scan(&poolReviewID))
	require.NoError(t, FlagVoucherReview(ctx, tx, voucherID, voucherReviewID, reporter, "Voucher reason"))
	require.NoError(t, FlagPoolReview(ctx, tx, poolID, poolReviewID, reporter, "Pool reason"))

	flags, err := ListReviewFlags(ctx, tx, 0)
	require.NoError(t, err)
	require.Len(t, flags, 2)
	require.Greater(t, flags[0].ID, flags[1].ID)
	byKind := map[string]ReviewFlag{}
	for _, flag := range flags {
		byKind[flag.ResourceKind] = flag
	}
	require.Equal(t, voucherAddress, byKind["voucher"].ResourceAddress)
	require.NotNil(t, byKind["voucher"].VoucherAddress)
	require.Nil(t, byKind["voucher"].PoolAddress)
	require.Equal(t, poolAddress, byKind["pool"].ResourceAddress)
	require.Nil(t, byKind["pool"].VoucherAddress)
	require.NotNil(t, byKind["pool"].PoolAddress)

	require.NoError(t, ResolveReviewFlag(ctx, tx, byKind["pool"].ID, true, reporter))
	flags, err = ListReviewFlags(ctx, tx, 0)
	require.NoError(t, err)
	require.Len(t, flags, 1)
	require.Equal(t, "voucher", flags[0].ResourceKind)
}
