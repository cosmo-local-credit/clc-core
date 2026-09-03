package store

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

// The closed set of workflow states a post can hold. DRAFT and REJECTED are
// legacy — nothing in the current contract creates or rejects — but imported
// rows hold them, so a management read has to be able to name them.
const (
	ReportStatusDraft     = "DRAFT"
	ReportStatusSubmitted = "SUBMITTED"
	ReportStatusApproved  = "APPROVED"
	ReportStatusRejected  = "REJECTED"
)

// ReportStatuses is the vocabulary in workflow order.
var ReportStatuses = []string{
	ReportStatusDraft,
	ReportStatusSubmitted,
	ReportStatusApproved,
	ReportStatusRejected,
}

// NormalizeReportStatus upper-cases and validates one requested status.
// Case-insensitive in, canonical out, like tagMatch. An empty string means no
// status filter and is not an error.
func NormalizeReportStatus(raw string) (string, bool) {
	trimmed := strings.ToUpper(strings.TrimSpace(raw))
	if trimmed == "" {
		return "", true
	}
	return trimmed, slices.Contains(ReportStatuses, trimmed)
}

type (
	// ReportVoucher names a voucher a report is linked to. Only what a list row
	// or a detail card renders — the full voucher is a separate lookup.
	ReportVoucher struct {
		ID      uint64  `json:"id"`
		ChainID int64   `json:"chainId"`
		Address string  `json:"address"`
		Name    *string `json:"name"`
		Symbol  *string `json:"symbol"`
	}

	// ReportImage is one http(s) image belonging to a report, in document
	// order. Width and Height are the editor's display box and are advisory:
	// they let a client reserve space before the bytes arrive. MaxWidth and
	// MaxHeight are the intrinsic size the editor recorded, and are not a
	// scaled version of the other pair.
	ReportImage struct {
		URL       string  `json:"url"`
		Width     *int32  `json:"width"`
		Height    *int32  `json:"height"`
		MaxWidth  *int32  `json:"maxWidth"`
		MaxHeight *int32  `json:"maxHeight"`
		ByteSize  *int64  `json:"byteSize"`
		Caption   *string `json:"caption"`
	}

	// ReportAttachment is one non-image medium on a report: Kind is "FILE" for
	// an uploaded document or video, "EMBED" for an external player URL.
	// Filename is set only for uploads.
	ReportAttachment struct {
		Kind     string  `json:"kind"`
		URL      string  `json:"url"`
		Filename *string `json:"filename"`
		ByteSize *int64  `json:"byteSize"`
	}

	// Report is one report representation. Public reads populate it only for an
	// approved row; authorized mutation responses can also carry workflow state
	// for a submitted or other legacy row. Since migration 010 nothing here is
	// parsed at read time: Body is markdown, and images, attachments and the
	// field-report form were extracted from the editor document at write time.
	Report struct {
		ID          uint64          `db:"id"             json:"id"`
		Title       string          `db:"title"          json:"title"`
		Description string          `db:"description"    json:"description"`
		ImageURL    *string         `db:"image_url"      json:"imageUrl"`
		GeoLat      *float64        `db:"geo_lat"        json:"geoLat"`
		GeoLng      *float64        `db:"geo_lng"        json:"geoLng"`
		PeriodFrom  *time.Time      `db:"period_from"    json:"periodFrom"`
		PeriodTo    *time.Time      `db:"period_to"      json:"periodTo"`
		AuthorAddr  string          `db:"author_address" json:"authorAddress"`
		Status      string          `db:"status"         json:"status,omitempty"`
		CreatedAt   time.Time       `db:"created_at"     json:"createdAt"`
		UpdatedAt   time.Time       `db:"updated_at"     json:"updatedAt"`
		Tags        []string        `db:"tags"           json:"tags"`
		Vouchers    []ReportVoucher `db:"vouchers"       json:"vouchers"`

		// Detail-only. Body is the prose as markdown and is empty on all but
		// 310 of the imported reports, whose content lives in Description. The
		// three form fields are issuer free text and are never normalized
		// ("2", "1 hour", "2hrs" all occur for TimeTaken).
		Body         *string            `db:"body"         json:"body,omitempty"`
		HostName     *string            `db:"host_name"    json:"hostName,omitempty"`
		Participants *string            `db:"participants" json:"participants,omitempty"`
		TimeTaken    *string            `db:"time_taken"   json:"timeTaken,omitempty"`
		Images       []ReportImage      `db:"images"       json:"images,omitempty"`
		Attachments  []ReportAttachment `db:"attachments"  json:"attachments,omitempty"`
	}

	// ReportCursor is the feed's keyset position. The feed orders by updated_at,
	// which on the imported catalog is unrelated to id order, so a single id is
	// not a usable cursor — the pair is.
	ReportCursor struct {
		UpdatedAt time.Time
		ID        uint64
	}

	// ReportWrite is the complete editable half of a report. The handler builds
	// it only after validating and canonicalising public input. Author, status,
	// source id and timestamps are intentionally not writable fields.
	ReportWrite struct {
		Title        string
		Body         string
		Description  string
		ImageURL     *string
		GeoLat       *float64
		GeoLng       *float64
		PeriodFrom   *time.Time
		PeriodTo     *time.Time
		HostName     *string
		Participants *string
		TimeTaken    *string
		Tags         []string
		VoucherIDs   []uint64
		Images       []ReportImage
		Attachments  []ReportAttachment
	}
)

func (pg *Pg) ListReports(ctx context.Context, tx pgx.Tx, limit uint64) ([]Report, error) {
	var reports []Report
	if err := pgxscan.Select(ctx, tx, &reports, pg.queries.ListReports, limit); err != nil {
		return nil, err
	}
	return reports, nil
}

func (pg *Pg) ListReportsNext(ctx context.Context, tx pgx.Tx, cursor ReportCursor, limit uint64) ([]Report, error) {
	var reports []Report
	err := pgxscan.Select(ctx, tx, &reports, pg.queries.ListReportsNext, cursor.UpdatedAt, cursor.ID, limit)
	if err != nil {
		return nil, err
	}
	return reports, nil
}

// GetReport returns nil when no approved report has that id, so an unapproved
// or missing report is indistinguishable from the outside.
func (pg *Pg) GetReport(ctx context.Context, tx pgx.Tx, id uint64) (*Report, error) {
	return pg.getReport(ctx, tx, id, false)
}

// GetReportForMutation reads the just-written representation regardless of a
// legacy row's workflow status. Mutation handlers call it only after creating
// the row or locking it and authorising its author or a superadmin; public
// detail keeps using GetReport and therefore still hides non-approved rows.
func (pg *Pg) GetReportForMutation(ctx context.Context, tx pgx.Tx, id uint64) (*Report, error) {
	return pg.getReport(ctx, tx, id, true)
}

func (pg *Pg) getReport(ctx context.Context, tx pgx.Tx, id uint64, includeUnapproved bool) (*Report, error) {
	var reports []Report
	if err := pgxscan.Select(ctx, tx, &reports, pg.queries.GetReport, id, includeUnapproved); err != nil {
		return nil, err
	}
	if len(reports) == 0 {
		return nil, nil
	}
	return &reports[0], nil
}

// GetManagedReport reads one complete post in any status, optionally
// constrained to one author. authorAddress is "" for a superadmin read and the
// caller's own wallet for GET /users/me/reports/:id; another author's row comes
// back nil, so the handler's 404 cannot be used to probe for it.
func (pg *Pg) GetManagedReport(
	ctx context.Context,
	tx pgx.Tx,
	id uint64,
	authorAddress string,
) (*Report, error) {
	var owner *string
	if authorAddress != "" {
		owner = &authorAddress
	}

	var reports []Report
	if err := pgxscan.Select(ctx, tx, &reports, pg.queries.GetManagedReport, id, owner); err != nil {
		return nil, err
	}
	if len(reports) == 0 {
		return nil, nil
	}
	return &reports[0], nil
}

// CreateReport inserts a post in the workflow state chosen from the caller's
// verified role. authorAddress comes from the user JWT, never from the request
// body, so a caller cannot create a post attributed to another wallet.
func (pg *Pg) CreateReport(
	ctx context.Context,
	tx pgx.Tx,
	report ReportWrite,
	authorAddress string,
	status string,
) (uint64, error) {
	var id uint64
	err := tx.QueryRow(
		ctx,
		pg.queries.CreateReport,
		report.Title,
		report.Body,
		report.Description,
		report.ImageURL,
		report.GeoLat,
		report.GeoLng,
		report.PeriodFrom,
		report.PeriodTo,
		authorAddress,
		status,
		report.HostName,
		report.Participants,
		report.TimeTaken,
	).Scan(&id)
	return id, err
}

// LockReportAuthor returns the immutable original author while holding the row
// lock through the caller's transaction. That makes the authorization decision
// and its update one atomic operation.
func (pg *Pg) LockReportAuthor(ctx context.Context, tx pgx.Tx, id uint64) (string, error) {
	var authorAddress string
	err := tx.QueryRow(ctx, pg.queries.LockReportAuthor, id).Scan(&authorAddress)
	return authorAddress, err
}

// UpdateReport assigns all editable scalar fields. Relations are replaced by
// ReplaceReportRelations in the same transaction, and modifierAddress records
// who made the latest edit without changing the original author.
//
// status is nil to preserve the row's current workflow state (a superadmin
// edit) or a value from ReportStatuses to assign it (an author's edit
// resubmits). Derived by the handler, which knows the verified caller.
func (pg *Pg) UpdateReport(
	ctx context.Context,
	tx pgx.Tx,
	id uint64,
	report ReportWrite,
	modifierAddress string,
	status *string,
) error {
	_, err := tx.Exec(
		ctx,
		pg.queries.UpdateReport,
		id,
		report.Title,
		report.Body,
		report.Description,
		report.ImageURL,
		report.GeoLat,
		report.GeoLng,
		report.PeriodFrom,
		report.PeriodTo,
		modifierAddress,
		report.HostName,
		report.Participants,
		report.TimeTaken,
		status,
	)
	return err
}

// ApproveReport changes workflow state without rewriting content. Keeping it
// separate from UpdateReport is what lets authors edit approved posts without
// putting them back into a review queue.
func (pg *Pg) ApproveReport(ctx context.Context, tx pgx.Tx, id uint64, modifierAddress string) error {
	var approvedID uint64
	return tx.QueryRow(ctx, pg.queries.ApproveReport, id, modifierAddress).Scan(&approvedID)
}

// ReplaceReportRelations swaps all four ordered/set-valued parts of a report.
// The sets are request-bounded (12 tags/vouchers, 50 images, 20 attachments),
// so the simple explicit inserts keep the SQL and failure point obvious.
func (pg *Pg) ReplaceReportRelations(
	ctx context.Context,
	tx pgx.Tx,
	reportID uint64,
	report ReportWrite,
) error {
	for _, query := range []string{
		pg.queries.DeleteReportTags,
		pg.queries.DeleteReportVouchers,
		pg.queries.DeleteReportImages,
		pg.queries.DeleteReportAttachments,
	} {
		if _, err := tx.Exec(ctx, query, reportID); err != nil {
			return err
		}
	}

	for _, tag := range report.Tags {
		if _, err := tx.Exec(ctx, pg.queries.InsertReportTag, reportID, tag); err != nil {
			return err
		}
	}
	for _, voucherID := range report.VoucherIDs {
		if _, err := tx.Exec(ctx, pg.queries.InsertReportVoucher, reportID, voucherID); err != nil {
			return err
		}
	}
	for index, reportImage := range report.Images {
		if _, err := tx.Exec(
			ctx,
			pg.queries.InsertReportImage,
			reportID,
			index+1,
			reportImage.URL,
			reportImage.Width,
			reportImage.Height,
			reportImage.MaxWidth,
			reportImage.MaxHeight,
			reportImage.ByteSize,
			reportImage.Caption,
		); err != nil {
			return err
		}
	}
	for index, attachment := range report.Attachments {
		if _, err := tx.Exec(
			ctx,
			pg.queries.InsertReportAttachment,
			reportID,
			index+1,
			attachment.Kind,
			attachment.URL,
			attachment.Filename,
			attachment.ByteSize,
		); err != nil {
			return err
		}
	}
	return nil
}

func (pg *Pg) DeleteReport(ctx context.Context, tx pgx.Tx, id uint64) error {
	var deletedID uint64
	return tx.QueryRow(ctx, pg.queries.DeleteReport, id).Scan(&deletedID)
}
