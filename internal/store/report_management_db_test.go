package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	managedReportAuthor = "0x00000000000000000000000000000000000000a1"
	managedReportOther  = "0x00000000000000000000000000000000000000b2"
)

// seedAuthoredReport inserts one report for a chosen author. Like seedReport,
// updated_at is set in the INSERT: the BEFORE UPDATE trigger would rewrite it
// and collapse every fixture onto one instant.
func seedAuthoredReport(t *testing.T, tx pgx.Tx, author, status string, updatedAt time.Time) uint64 {
	t.Helper()
	ctx := context.Background()

	var id uint64
	err := tx.QueryRow(ctx, `
		INSERT INTO reports (title, body, description, author_address, status, updated_at)
		VALUES ('Managed Fixture', '', 'Managed fixture description', $1, $2, $3)
		RETURNING id`, author, status, updatedAt).Scan(&id)
	if err != nil {
		t.Fatalf("seed authored report: %v", err)
	}
	return id
}

func managedStatusFilter(status string) ManagedReportFilter {
	return ManagedReportFilter{Status: status}
}

// TestManagedReportDetailProjectionMatchesPublicDetail is hermetic — it reads
// the two statements from core_queries.sql and needs no database.
//
// An editor built on get-managed-report sends every field back on an assign-all
// PUT, so a column the public detail returns and this one does not is a field
// the editor silently erases.
func TestManagedReportDetailProjectionMatchesPublicDetail(t *testing.T) {
	queries, err := loadQueries("../../core_queries.sql")
	if err != nil {
		t.Fatalf("load core queries: %v", err)
	}

	public, ok := reportDetailSelectList(queries.GetReport)
	if !ok {
		t.Fatalf("get-report has no FROM reports r: %q", queries.GetReport)
	}
	managed, ok := reportDetailSelectList(queries.GetManagedReport)
	if !ok {
		t.Fatalf("get-managed-report has no FROM reports r: %q", queries.GetManagedReport)
	}
	if public != managed {
		t.Fatalf("detail projections drifted.\npublic:\n%s\n\nmanaged:\n%s", public, managed)
	}
}

func reportDetailSelectList(statement string) (string, bool) {
	head, _, found := strings.Cut(statement, " FROM reports r")
	if !found {
		return "", false
	}
	return strings.TrimSpace(head), true
}

func TestManagedReportListReturnsEveryStatusForAnOperator(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	want := map[uint64]string{}
	for index, status := range ReportStatuses {
		id := seedReport(t, tx, status, base.Add(time.Duration(index)*time.Hour), "")
		want[id] = status
	}

	rows, err := pg.ListManagedReports(
		ctx, tx, managedStatusFilter(""), ReportCursor{}, CatalogFirstPage, 100)
	if err != nil {
		t.Fatalf("list managed reports: %v", err)
	}

	got := map[uint64]string{}
	for _, row := range rows {
		if _, seeded := want[row.ID]; seeded {
			got[row.ID] = row.Status
		}
	}
	for id, status := range want {
		if got[id] != status {
			t.Fatalf("report %d status = %q, want %q", id, got[id], status)
		}
	}
	for index := 1; index < len(rows); index++ {
		if rows[index].UpdatedAt.After(rows[index-1].UpdatedAt) {
			t.Fatalf("row %d updated after the row before it", index)
		}
	}
}

func TestManagedReportListFiltersByStatus(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	base := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	submitted := seedReport(t, tx, ReportStatusSubmitted, base, "")
	approved := seedReport(t, tx, ReportStatusApproved, base.Add(time.Hour), "")

	rows, err := pg.ListManagedReports(
		ctx, tx, managedStatusFilter(ReportStatusSubmitted), ReportCursor{}, CatalogFirstPage, 100)
	if err != nil {
		t.Fatalf("list submitted: %v", err)
	}
	for _, row := range rows {
		if row.Status != ReportStatusSubmitted {
			t.Fatalf("report %d status = %q under a SUBMITTED filter", row.ID, row.Status)
		}
	}
	if containsReport(rows, approved) {
		t.Fatalf("approved report %d appeared under a SUBMITTED filter", approved)
	}
	if !containsReport(rows, submitted) {
		t.Fatalf("submitted report %d missing from the SUBMITTED queue", submitted)
	}
}

func TestManagedReportListScopesToOneAuthor(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	base := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	mine := seedAuthoredReport(t, tx, managedReportAuthor, ReportStatusSubmitted, base)
	published := seedAuthoredReport(t, tx, managedReportAuthor, ReportStatusApproved, base.Add(time.Hour))
	theirs := seedAuthoredReport(t, tx, managedReportOther, ReportStatusSubmitted, base.Add(2*time.Hour))

	// Upper-cased in the filter: the predicate is case-folded and migration 025
	// indexes the same expression.
	filter := ManagedReportFilter{Filter: CatalogFilter{Owner: strings.ToUpper(managedReportAuthor)}}
	rows, err := pg.ListManagedReports(ctx, tx, filter, ReportCursor{}, CatalogFirstPage, 100)
	if err != nil {
		t.Fatalf("list author reports: %v", err)
	}

	if !containsReport(rows, mine) || !containsReport(rows, published) {
		t.Fatalf("author list missing own rows %d/%d", mine, published)
	}
	if containsReport(rows, theirs) {
		t.Fatalf("author list leaked another author's report %d", theirs)
	}
}

func TestManagedReportDetailScopesToOneAuthor(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	id := seedAuthoredReport(t, tx, managedReportAuthor, ReportStatusSubmitted,
		time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC))

	own, err := pg.GetManagedReport(ctx, tx, id, managedReportAuthor)
	if err != nil || own == nil || own.Status != ReportStatusSubmitted {
		t.Fatalf("own detail = %+v, err = %v", own, err)
	}
	// Nil, not an error: the handler answers 404 so the id cannot be probed.
	foreign, err := pg.GetManagedReport(ctx, tx, id, managedReportOther)
	if err != nil || foreign != nil {
		t.Fatalf("foreign detail = %+v, err = %v", foreign, err)
	}
	operator, err := pg.GetManagedReport(ctx, tx, id, "")
	if err != nil || operator == nil {
		t.Fatalf("operator detail = %+v, err = %v", operator, err)
	}
	if public, err := pg.GetReport(ctx, tx, id); err != nil || public != nil {
		t.Fatalf("public detail of a submitted report = %+v, err = %v", public, err)
	}
}

// TestManagedReportRelationFiltersMatchTheCatalogContract exercises the tag,
// voucher and search reducers against unapproved rows, which the feed's partial
// indexes and APPROVED predicate cannot reach.
func TestManagedReportRelationFiltersMatchTheCatalogContract(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	var tag string
	if err := tx.QueryRow(ctx, `SELECT value FROM token_tag ORDER BY value LIMIT 1`).Scan(&tag); err != nil {
		t.Fatalf("read tag vocabulary: %v", err)
	}
	voucherID := seedMarketVoucher(t, tx, -1.29, 36.82, true)

	base := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	related := seedAuthoredReport(t, tx, managedReportAuthor, ReportStatusSubmitted, base)
	unrelated := seedAuthoredReport(t, tx, managedReportAuthor, ReportStatusSubmitted, base.Add(time.Hour))
	if _, err := tx.Exec(ctx,
		`INSERT INTO report_tags (report_id, tag_value) VALUES ($1, $2)`, related, tag); err != nil {
		t.Fatalf("link tag: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO report_vouchers (report_id, voucher_id) VALUES ($1, $2)`, related, voucherID); err != nil {
		t.Fatalf("link voucher: %v", err)
	}

	owner := CatalogFilter{Owner: managedReportAuthor}
	for name, filter := range map[string]ManagedReportFilter{
		"tag": {Filter: CatalogFilter{
			Owner: owner.Owner, Tags: []string{tag}, TagMatch: TagMatchAny,
		}},
		"voucher": {Filter: CatalogFilter{
			Owner: owner.Owner, VoucherIDs: []uint64{voucherID}, VoucherMatch: TagMatchAny,
		}},
		"term": {Filter: CatalogFilter{Owner: owner.Owner, Term: "Managed Fixture"}},
	} {
		t.Run(name, func(t *testing.T) {
			rows, err := pg.ListManagedReports(ctx, tx, filter, ReportCursor{}, CatalogFirstPage, 100)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if !containsReport(rows, related) {
				t.Fatalf("%s filter missed report %d", name, related)
			}
			if name != "term" && containsReport(rows, unrelated) {
				t.Fatalf("%s filter matched unrelated report %d", name, unrelated)
			}
		})
	}
}

// TestManagedReportKeysetCoversEveryRowExactlyOnce walks forward to the end and
// back again. The fixtures share timestamps in pairs, which is what the imported
// catalog's batches look like and what a single-column cursor gets wrong.
func TestManagedReportKeysetCoversEveryRowExactlyOnce(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()

	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	seeded := map[uint64]struct{}{}
	for index := range 6 {
		stamp := base.Add(time.Duration(index/2) * time.Hour)
		seeded[seedAuthoredReport(t, tx, managedReportAuthor, ReportStatusSubmitted, stamp)] = struct{}{}
	}
	filter := ManagedReportFilter{
		Filter: CatalogFilter{Owner: managedReportAuthor},
		Status: ReportStatusSubmitted,
	}

	assertCoversExactly(t, "forward", walkManagedReports(t, pg, tx, filter, CatalogNextPage, 2), seeded)
	assertCoversExactly(t, "backward", walkManagedReports(t, pg, tx, filter, CatalogPrevPage, 2), seeded)
}

// walkManagedReports pages in one direction until the list is exhausted,
// returning every id it saw. A backward walk first advances to the last page so
// the reverse predicate is exercised from a real position.
func walkManagedReports(
	t *testing.T,
	pg *Pg,
	tx pgx.Tx,
	filter ManagedReportFilter,
	direction CatalogPage,
	perPage uint64,
) []uint64 {
	t.Helper()
	ctx := context.Background()

	page, err := pg.ListManagedReports(ctx, tx, filter, ReportCursor{}, CatalogFirstPage, perPage)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if direction == CatalogPrevPage {
		for len(page) > 0 {
			tail := page[len(page)-1]
			next, err := pg.ListManagedReports(ctx, tx, filter,
				ReportCursor{UpdatedAt: tail.UpdatedAt, ID: tail.ID}, CatalogNextPage, perPage)
			if err != nil {
				t.Fatalf("advance to the end: %v", err)
			}
			if len(next) == 0 {
				break
			}
			page = next
		}
	}

	seen := make([]uint64, 0, perPage*4)
	for len(page) > 0 {
		for _, row := range page {
			seen = append(seen, row.ID)
		}
		edge := page[len(page)-1]
		if direction == CatalogPrevPage {
			edge = page[0]
		}
		page, err = pg.ListManagedReports(ctx, tx, filter,
			ReportCursor{UpdatedAt: edge.UpdatedAt, ID: edge.ID}, direction, perPage)
		if err != nil {
			t.Fatalf("page: %v", err)
		}
	}
	return seen
}

func assertCoversExactly(t *testing.T, label string, seen []uint64, want map[uint64]struct{}) {
	t.Helper()

	counts := map[uint64]int{}
	for _, id := range seen {
		counts[id]++
	}
	for id := range want {
		if counts[id] != 1 {
			t.Fatalf("%s paging saw report %d %d times, want once", label, id, counts[id])
		}
	}
}

func containsReport(rows []Report, id uint64) bool {
	for _, row := range rows {
		if row.ID == id {
			return true
		}
	}
	return false
}
