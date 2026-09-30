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
