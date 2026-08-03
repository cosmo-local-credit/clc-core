package store

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

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

	// Report is one approved report. Since migration 010 nothing here is parsed
	// at read time: Body is markdown, and images, attachments and the
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
	var reports []Report
	if err := pgxscan.Select(ctx, tx, &reports, pg.queries.GetReport, id); err != nil {
		return nil, err
	}
	if len(reports) == 0 {
		return nil, nil
	}
	return &reports[0], nil
}
