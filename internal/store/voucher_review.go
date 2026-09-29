package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

type (
	// VoucherReview is safe to return publicly. Identity and transaction data never
	// enter this DTO, even when the caller is an owner or administrator.
	VoucherReview struct {
		ID      uint64  `db:"id" json:"id"`
		Stars   int     `db:"stars" json:"stars"`
		Comment string  `db:"comment" json:"comment"`
		Reply   *string `db:"reply" json:"reply"`
	}

	ReviewVoucher struct {
		ID           uint64  `db:"id"`
		ChainID      int64   `db:"chain_id"`
		Address      string  `db:"address"`
		OwnerAddress *string `db:"owner_address"`
	}

	ReviewAccess struct {
		IsOwner          bool           `json:"isOwner"`
		EditableUntil    *time.Time     `json:"editableUntil"`
		Review           *VoucherReview `json:"review"`
		Hidden           bool           `json:"hidden"`
		EditableReplyIDs []uint64       `json:"editableReplyIds"`
		FlaggedReviewIDs []uint64       `json:"flaggedReviewIds"`
	}

	ReviewPage struct {
		Reviews []VoucherReview `json:"reviews"`
		Counts  map[int]int     `json:"counts"`
		Total   int             `json:"total"`
		Average float64         `json:"average"`
		Next    *uint64         `json:"next"`
	}

	ReviewFlag struct {
		ID              uint64    `db:"id" json:"id"`
		ReviewID        uint64    `db:"review_id" json:"reviewId"`
		VoucherAddress  string    `db:"voucher_address" json:"voucherAddress"`
		Reason          string    `db:"reason" json:"reason"`
		Stars           int       `db:"stars" json:"stars"`
		Comment         string    `db:"comment" json:"comment"`
		ReportedStars   int       `db:"reported_stars" json:"reportedStars"`
		ReportedComment string    `db:"reported_comment" json:"reportedComment"`
		CreatedAt       time.Time `db:"created_at" json:"createdAt"`
	}
)

// GetReviewVoucher reads or locks the voucher in the caller's transaction.
// Authorization and mutation share this lock.
func GetReviewVoucher(ctx context.Context, tx pgx.Tx, address string, lock bool) (ReviewVoucher, error) {
	query := `SELECT id, chain_id, address, owner_address FROM vouchers WHERE lower(address) = lower($1)`
	if lock {
		query += ` FOR UPDATE`
	}
	var voucher ReviewVoucher
	err := pgxscan.Get(ctx, tx, &voucher, query, address)
	return voucher, err
}

func ListVoucherReviews(ctx context.Context, tx pgx.Tx, voucherID uint64, stars int, before uint64) (ReviewPage, error) {
	page := ReviewPage{Reviews: []VoucherReview{}, Counts: map[int]int{1: 0, 2: 0, 3: 0, 4: 0, 5: 0}}
	var counts []struct {
		Stars int
		Count int
	}
	if err := pgxscan.Select(ctx, tx, &counts, `SELECT stars, count(*) AS count FROM voucher_reviews WHERE voucher_id=$1 AND NOT hidden GROUP BY stars`, voucherID); err != nil {
		return page, err
	}
	sum := 0
	for _, count := range counts {
		page.Counts[count.Stars] = count.Count
		page.Total += count.Count
		sum += count.Stars * count.Count
	}
	if page.Total > 0 {
		page.Average = float64(sum) / float64(page.Total)
	}
	err := pgxscan.Select(ctx, tx, &page.Reviews, `SELECT id, stars, comment, reply FROM voucher_reviews
        WHERE voucher_id=$1 AND NOT hidden AND ($2::int=0 OR stars=$2) AND ($3::bigint=0 OR id<$3)
        ORDER BY id DESC LIMIT 21`, voucherID, stars, before)
	if len(page.Reviews) > 20 {
		page.Reviews = page.Reviews[:20]
		id := page.Reviews[19].ID
		page.Next = &id
	}
	return page, err
}

func GetVoucherReviewAccess(ctx context.Context, tx pgx.Tx, voucher ReviewVoucher, address string) (ReviewAccess, error) {
	access := ReviewAccess{EditableReplyIDs: []uint64{}, FlaggedReviewIDs: []uint64{}}
	access.IsOwner = voucher.OwnerAddress != nil && strings.EqualFold(*voucher.OwnerAddress, address)
	var own struct {
		VoucherReview

		Hidden bool `db:"hidden"`
	}
	err := pgxscan.Get(ctx, tx, &own, `SELECT id, stars, comment, reply, hidden FROM voucher_reviews WHERE voucher_id=$1 AND reviewer_address=$2`, voucher.ID, strings.ToLower(address))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return access, err
	}
	if err == nil {
		access.Review = &own.VoucherReview
		access.Hidden = own.Hidden
	}
	if !access.IsOwner {
		return access, nil
	}
	err = pgxscan.Select(ctx, tx, &access.EditableReplyIDs, `SELECT id FROM voucher_reviews WHERE voucher_id=$1 AND NOT hidden AND reply_author_address=$2`, voucher.ID, strings.ToLower(address))
	if err != nil {
		return access, err
	}
	err = pgxscan.Select(ctx, tx, &access.FlaggedReviewIDs, `SELECT f.review_id FROM voucher_review_flags f JOIN voucher_reviews r ON r.id=f.review_id WHERE r.voucher_id=$1 AND f.reporter_address=$2`, voucher.ID, strings.ToLower(address))
	return access, err
}

func SaveVoucherReview(ctx context.Context, tx pgx.Tx, voucherID uint64, address string, stars int, comment string, until time.Time) error {
	var id uint64
	// Recheck the deadline in SQL, including after a conflicting insert waits.
	return tx.QueryRow(ctx, `INSERT INTO voucher_reviews (voucher_id, reviewer_address, stars, comment)
        SELECT $1, $2, $3, $4 WHERE clock_timestamp() < $5
        ON CONFLICT (voucher_id, reviewer_address) DO UPDATE SET stars=EXCLUDED.stars, comment=EXCLUDED.comment, updated_at=clock_timestamp()
        WHERE NOT voucher_reviews.hidden AND clock_timestamp() < $5 RETURNING id`, voucherID, strings.ToLower(address), stars, comment, until).Scan(&id)
}

func SaveVoucherReviewReply(ctx context.Context, tx pgx.Tx, voucherID, id uint64, author, reply string) error {
	var saved uint64
	return tx.QueryRow(ctx, `UPDATE voucher_reviews SET reply=$4, reply_author_address=$3, reply_updated_at=clock_timestamp()
        WHERE voucher_id=$1 AND id=$2 AND NOT hidden AND (reply_author_address IS NULL OR reply_author_address=$3) RETURNING id`, voucherID, id, strings.ToLower(author), reply).Scan(&saved)
}

func FlagVoucherReview(ctx context.Context, tx pgx.Tx, voucherID, id uint64, author, reason string) error {
	// Check the target even for retries; report ownership never grants edits.
	var exists uint64
	if err := tx.QueryRow(ctx, `SELECT id FROM voucher_reviews WHERE voucher_id=$1 AND id=$2 AND NOT hidden FOR UPDATE`, voucherID, id).Scan(&exists); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO voucher_review_flags (review_id, reporter_address, reason, reported_stars, reported_comment)
        SELECT id, $3, $4, stars, comment FROM voucher_reviews WHERE voucher_id=$1 AND id=$2
        ON CONFLICT (review_id, reporter_address) DO NOTHING`, voucherID, id, strings.ToLower(author), reason)
	return err
}

// LatestVoucherRedemption reads only successful, positive transfers from this
// reviewer to this voucher's issuer. The configured application chain is checked
// by the API before calling; the indexer tables themselves have no chain ID.
func LatestVoucherRedemption(ctx context.Context, txDB FederationStore, voucher, reviewer, issuer string) (*time.Time, error) {
	var latest *time.Time
	err := txDB.Pool().QueryRow(ctx, `SELECT max(tx.date_block AT TIME ZONE 'UTC')
        FROM token_transfer tt JOIN tx ON tx.id=tt.tx_id
        WHERE tt.contract_address=$1 AND tt.sender_address=$2 AND tt.recipient_address=$3
        AND tt.sender_address<>tt.recipient_address AND tt.transfer_value>0 AND tx.success
        AND tx.date_block <= (CURRENT_TIMESTAMP AT TIME ZONE 'UTC')`, strings.ToLower(voucher), strings.ToLower(reviewer), strings.ToLower(issuer)).Scan(&latest)
	return latest, err
}

func ListReviewFlags(ctx context.Context, tx pgx.Tx, before uint64) ([]ReviewFlag, error) {
	flags := []ReviewFlag{}
	err := pgxscan.Select(ctx, tx, &flags, `SELECT f.id, f.review_id, v.address AS voucher_address, f.reason,
        r.stars, r.comment, f.reported_stars, f.reported_comment, f.created_at FROM voucher_review_flags f
        JOIN voucher_reviews r ON r.id=f.review_id JOIN vouchers v ON v.id=r.voucher_id
        WHERE f.resolved_at IS NULL AND ($1::bigint=0 OR f.id<$1) ORDER BY f.id DESC LIMIT 21`, before)
	return flags, err
}

func ResolveReviewFlag(ctx context.Context, tx pgx.Tx, flagID uint64, hidden bool, admin string) error {
	var reviewID uint64
	// Same lock order as writers: voucher, then review, then flag.
	var address string
	if err := tx.QueryRow(ctx, `SELECT v.address FROM voucher_review_flags f JOIN voucher_reviews r ON r.id=f.review_id JOIN vouchers v ON v.id=r.voucher_id WHERE f.id=$1`, flagID).Scan(&address); err != nil {
		return err
	}
	if _, err := GetReviewVoucher(ctx, tx, address, true); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT review_id FROM voucher_review_flags WHERE id=$1 AND resolved_at IS NULL`, flagID).Scan(&reviewID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE voucher_reviews SET hidden=$2 WHERE id=$1`, reviewID, hidden); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE voucher_review_flags SET resolved_at=clock_timestamp(), resolved_by=$2 WHERE review_id=$1 AND resolved_at IS NULL`, reviewID, strings.ToLower(admin))
	return err
}
