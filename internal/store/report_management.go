package store

import (
	"context"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

// Post management pages: one author's own posts in every status, and the
// console's global queue.
//
// A separate composer rather than a widening of composeReportPage. CatalogFilter
// means "the approved catalog" on every list that takes one, and every report
// index built so far is partial on status = 'APPROVED' because of it. Three
// things differ here:
//
//   - all four statuses are visible, narrowed by an optional exact status;
//   - paging is bidirectional, so it takes the typed CatalogPage the voucher and
//     pool catalogs use rather than the feed's forward-only hasCursor;
//   - ordering stays updated_at DESC, id DESC — an author's edit of an approved
//     post is a new moderation task, and id order would bury it at its original
//     creation position.

// managedReportPageProjection hydrates one already-selected page of report IDs.
// The column list is the feed's own plus status, which every management summary
// needs. The outer ORDER BY is unconditional: a backward page scans ascending
// and is restored to newest-first here.
const managedReportPageProjection = "\nSELECT " + reportSummaryColumns + ",\n    r.status" +
	"\nFROM page" +
	"\nINNER JOIN reports r ON r.id = page.id" +
	"\nORDER BY r.updated_at DESC, r.id DESC;"

// ManagedReportFilter is the validated narrowing for a management list. Status
// is separate from CatalogFilter because the catalog has one visible status by
// definition.
type ManagedReportFilter struct {
	Filter CatalogFilter
	// Status is one value from ReportStatuses, or "" for every status. It is
	// validated before it reaches here and crosses as a bound parameter.
	Status string
}

// reportStatusPredicate restricts a page to one workflow state. Exact equality:
// the only multi-state view is "all", which is the absent filter.
func reportStatusPredicate(args *sqlArgs, alias, status string) string {
	if status == "" {
		return ""
	}
	return alias + ".status = " + args.bind(status)
}

// managedReportCursorPredicate is the bidirectional keyset comparison. A row
// comparison because (updated_at, id) is a total order and updated_at alone is
// not; the operator comes from the typed direction.
func managedReportCursorPredicate(
	args *sqlArgs,
	alias string,
	page CatalogPage,
	cursor ReportCursor,
) string {
	if page == CatalogFirstPage {
		return ""
	}

	updatedAt := args.bind(cursor.UpdatedAt)
	id := args.bind(cursor.ID)
	operator := " < "
	if page == CatalogPrevPage {
		operator = " > "
	}
	return "(" + alias + ".updated_at, " + alias + ".id)" + operator +
		"(" + updatedAt + ", " + id + ")"
}

// managedReportOrder is the inner scan order over the keyset pair.
func managedReportOrder(alias string, page CatalogPage) string {
	direction := pageOrder(page)
	return "\n    ORDER BY " + alias + ".updated_at " + direction + ", " + alias + ".id " + direction
}

// composeManagedReportPage builds one management page, with the same two driving
// shapes and for the same measured reason as composeReportPage: a search term
// materializes its match set so the trigram index drives, and a reducer-only
// query pages the table directly.
//
// The all-status search relies on migration 025's non-partial trigram indexes.
// 012's are partial on APPROVED and cannot serve a review queue.
func composeManagedReportPage(
	filter ManagedReportFilter,
	cursor ReportCursor,
	page CatalogPage,
	limit uint64,
) composed {
	args := &sqlArgs{}
	inner := filter.Filter

	if inner.Term == "" {
		body := "WITH page AS MATERIALIZED (\n" +
			"    SELECT r.id\n" +
			"    FROM reports r" +
			whereClause(
				reportStatusPredicate(args, "r", filter.Status),
				ownerPredicate(args, "r.author_address", inner),
				reportTagPredicate(args, inner),
				reportVoucherPredicate(args, inner),
				managedReportCursorPredicate(args, "r", page, cursor),
			) +
			managedReportOrder("r", page) +
			"\n    LIMIT " + args.bind(limit) +
			"\n)"
		return composed{sql: body + managedReportPageProjection, args: args.values}
	}

	body := "WITH matches AS MATERIALIZED (\n" +
		"    SELECT r.id, r.updated_at\n" +
		"    FROM reports r" +
		whereClause(
			reportStatusPredicate(args, "r", filter.Status),
			ownerPredicate(args, "r.author_address", inner),
			reportSearchPredicate(args, inner),
			reportTagPredicate(args, inner),
			reportVoucherPredicate(args, inner),
		) +
		"\n),\npage AS MATERIALIZED (\n" +
		"    SELECT m.id\n" +
		"    FROM matches m" +
		whereClause(managedReportCursorPredicate(args, "m", page, cursor)) +
		managedReportOrder("m", page) +
		"\n    LIMIT " + args.bind(limit) +
		"\n)"
	return composed{sql: body + managedReportPageProjection, args: args.values}
}

// ListManagedReports returns one page of posts in every status.
//
// No unfiltered fast path, unlike ListReportsFiltered: there is no static
// all-status statement, and the empty filter is a real query here — the
// console's "All" view.
//
// The owner dimension is what makes one function serve both routes.
// /users/me/reports sets Filter.Owner from the verified JWT; /admin/reports
// leaves it empty or sets it from `author`.
func (pg *Pg) ListManagedReports(
	ctx context.Context,
	tx pgx.Tx,
	filter ManagedReportFilter,
	cursor ReportCursor,
	page CatalogPage,
	limit uint64,
) ([]Report, error) {
	query := composeManagedReportPage(filter, cursor, page, limit)

	var reports []Report
	if err := pgxscan.Select(ctx, tx, &reports, query.sql, query.args...); err != nil {
		return nil, err
	}
	return reports, nil
}
