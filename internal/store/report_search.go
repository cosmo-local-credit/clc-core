package store

import (
	"context"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

// Filtered report pages and the report tag facet (SEARCH_FILTER_PLAN.md Slice 4).
//
// Two things make this differ from the voucher and pool composers rather than reuse them:
//
//   - The feed's keyset is the (updated_at, id) pair, not a numeric id. Reports order by
//     update time, and on the imported catalog that order agrees with id order on only 4 of
//     3,730 rows — so an id cursor would silently skip and repeat rows. The cursor predicate
//     here is therefore a row comparison, not a single column.
//   - The search targets title and description, both ordinary columns on `reports`. Report
//     body search stays parked: the body is normalized into a separate structure and needs a
//     different index shape entirely.
//
// Report tags are **direct** `report_tags`, so the tag reducer and the facet are both the
// simple form — like pools, not like the offering-derived voucher tags.
//
// Visibility is unchanged: approved reports only. The feed has never shown anything else and
// search does not change that.

// reportProjection is the SELECT list, byte-identical in output to the static statements so a
// filtered page and an unfiltered page return the same fields.
const (
	reportProjection = `
SELECT r.id, r.title, r.description,
    CASE WHEN left(r.image_url, 8) ~* '^https?://' THEN r.image_url END AS image_url,
    r.geo_lat, r.geo_lng, r.period_from, r.period_to, r.author_address,
    r.created_at, r.updated_at,
    COALESCE((
        SELECT array_agg(rt.tag_value ORDER BY rt.tag_value)
        FROM report_tags rt
        WHERE rt.report_id = r.id
    ), ARRAY[]::TEXT[]) AS tags,
	COALESCE((
	    SELECT jsonb_agg(jsonb_build_object(
	        'id', v.id, 'chainId', v.chain_id, 'address', v.address,
	        'name', v.name, 'symbol', v.symbol
	    ) ORDER BY v.id)
        FROM report_vouchers rv
        INNER JOIN vouchers v ON v.id = rv.voucher_id
        WHERE rv.report_id = r.id
    ), '[]'::JSONB) AS vouchers
FROM reports r`
)

// reportSearchPredicate matches the term against title and description.
//
// Both fragments are constant text and the term is bound once and reused, exactly as the
// catalog's name/symbol predicate does.
func reportSearchPredicate(args *sqlArgs, filter CatalogFilter) string {
	if filter.Term == "" {
		return ""
	}

	placeholder := args.bind(filter.likeTerm())
	if filter.prefixOnly() {
		return "(lower(r.title) LIKE lower(" + placeholder + ") || '%' ESCAPE '\\'" +
			"\n            OR lower(r.description) LIKE lower(" + args.reuse() + ") || '%' ESCAPE '\\')"
	}
	return "(lower(r.title) LIKE '%' || lower(" + placeholder + ") || '%' ESCAPE '\\'" +
		"\n            OR lower(r.description) LIKE '%' || lower(" + args.reuse() + ") || '%' ESCAPE '\\')"
}

// reportTagPredicate returns the direct report_tags reducer.
//
// `report_tags`' primary key gives one row per (report, tag), so the all-tags form needs no
// DISTINCT — the join cannot fan out.
func reportTagPredicate(args *sqlArgs, filter CatalogFilter) string {
	if len(filter.Tags) == 0 {
		return ""
	}

	placeholder := args.bind(filter.Tags)
	if filter.TagMatch == TagMatchAll {
		return `(
            SELECT COUNT(*)
            FROM report_tags rt
            WHERE rt.report_id = r.id
                AND rt.tag_value = ANY(` + placeholder + `::TEXT[])
        ) = cardinality(` + args.reuse() + `::TEXT[])`
	}
	return `EXISTS (
            SELECT 1
            FROM report_tags rt
            WHERE rt.report_id = r.id
                AND rt.tag_value = ANY(` + placeholder + `::TEXT[])
        )`
}

// reportVoucherPredicate returns the report_vouchers relationship reducer.
// The primary key guarantees one row per report/voucher pair, so the all form
// can compare COUNT(*) directly with the deduplicated requested ID count.
func reportVoucherPredicate(args *sqlArgs, filter CatalogFilter) string {
	if len(filter.VoucherIDs) == 0 {
		return ""
	}

	placeholder := args.bind(voucherIDsParam(filter.VoucherIDs))
	if filter.VoucherMatch == TagMatchAll {
		return `(
            SELECT COUNT(*)
            FROM report_vouchers rv
            WHERE rv.report_id = r.id
                AND rv.voucher_id = ANY(` + placeholder + `::BIGINT[])
        ) = cardinality(` + args.reuse() + `::BIGINT[])`
	}
	return `EXISTS (
            SELECT 1
            FROM report_vouchers rv
            WHERE rv.report_id = r.id
                AND rv.voucher_id = ANY(` + placeholder + `::BIGINT[])
        )`
}

// reportCursorPredicate returns the feed's keyset comparison.
//
// A row comparison rather than a single column: `(updated_at, id) < ($1, $2)` is what makes
// the pair a total order. Comparing only `updated_at` would drop or repeat every row sharing
// a timestamp, and comparing only `id` would order the feed differently than it displays.
func reportCursorPredicate(args *sqlArgs, cursor ReportCursor, hasCursor bool) string {
	if !hasCursor {
		return ""
	}
	updatedAt := args.bind(cursor.UpdatedAt)
	id := args.bind(cursor.ID)
	return "(r.updated_at, r.id) < (" + updatedAt + ", " + id + ")"
}

// composeReportPage builds one filtered report page.
//
// The match set is materialized before ordering and limiting, for the reason migration 011
// measured on the voucher catalog: adding the keyset predicate to a query already ordered and
// limited makes the feed index look like it serves the filter, the ordering, and the early
// abort at once, so the planner takes it and demotes the search to a row filter over the whole
// table. Materializing keeps the trigram index driving.
//
// Without a term the direct shape is kept: with no search predicate the feed's existing
// (updated_at, id) index walk is the correct plan, and materializing would force every
// candidate to be collected before the limit could apply.
func composeReportPage(
	filter CatalogFilter,
	cursor ReportCursor,
	hasCursor bool,
	limit uint64,
) composed {
	args := &sqlArgs{}

	if filter.Term == "" {
		body := reportProjection +
			whereClause(
				"r.status = 'APPROVED'",
				reportTagPredicate(args, filter),
				reportVoucherPredicate(args, filter),
				reportCursorPredicate(args, cursor, hasCursor),
			) +
			"\nORDER BY r.updated_at DESC, r.id DESC" +
			"\nLIMIT " + args.bind(limit) + ";"
		return composed{sql: body, args: args.values}
	}

	// Search shape: let the trigram index build the match set, then page it.
	matches := "WITH matches AS MATERIALIZED (\n" +
		"    SELECT r.id, r.updated_at\n" +
		"    FROM reports r" +
		whereClause(
			"r.status = 'APPROVED'",
			reportSearchPredicate(args, filter),
			reportTagPredicate(args, filter),
			reportVoucherPredicate(args, filter),
		) +
		"\n),\npage AS MATERIALIZED (\n" +
		"    SELECT m.id\n" +
		"    FROM matches m" +
		whereClause(reportPageCursorPredicate(args, cursor, hasCursor)) +
		"\n    ORDER BY m.updated_at DESC, m.id DESC" +
		"\n    LIMIT " + args.bind(limit) +
		"\n)\n"

	// The projection joins back to `reports`, so it keeps its `r` alias and filters to the
	// paged ids.
	return composed{
		sql: matches + reportProjection +
			"\nINNER JOIN page ON page.id = r.id" +
			"\nORDER BY r.updated_at DESC, r.id DESC;",
		args: args.values,
	}
}

// reportPageCursorPredicate is the keyset comparison against the materialized match set, whose
// alias is `m` rather than `r`.
func reportPageCursorPredicate(args *sqlArgs, cursor ReportCursor, hasCursor bool) string {
	if !hasCursor {
		return ""
	}
	updatedAt := args.bind(cursor.UpdatedAt)
	id := args.bind(cursor.ID)
	return "(m.updated_at, m.id) < (" + updatedAt + ", " + id + ")"
}

// composeReportTagCounts counts, per tag, how many reports the current filter leaves under it.
//
// Disjunctive, like every other facet here: the candidate set carries the search term but
// deliberately omits the tag filter, so selecting one tag does not zero every other tag's
// count and the user can always broaden their selection.
func composeReportTagCounts(filter CatalogFilter) composed {
	args := &sqlArgs{}

	sql := `WITH candidates AS MATERIALIZED (
    SELECT r.id
    FROM reports r` +
		whereClause(
			"r.status = 'APPROVED'",
			reportSearchPredicate(args, filter),
			reportVoucherPredicate(args, filter),
		) + `
)
SELECT rt.tag_value AS tag_value,
    COUNT(*) AS match_count
FROM candidates c
INNER JOIN report_tags rt ON rt.report_id = c.id
GROUP BY rt.tag_value;`

	return composed{sql: sql, args: args.values}
}

// composeReportVoucherCounts returns the voucher options that occur under the
// current search and tag filters. It deliberately omits the selected voucher
// reducer, making the counts disjunctive within this facet.
func composeReportVoucherCounts(filter CatalogFilter) composed {
	args := &sqlArgs{}

	sql := `WITH candidates AS MATERIALIZED (
    SELECT r.id
    FROM reports r` +
		whereClause(
			"r.status = 'APPROVED'",
			reportSearchPredicate(args, filter),
			reportTagPredicate(args, filter),
		) + `
)
SELECT v.id, v.chain_id, v.address, v.name, v.symbol,
    COUNT(*) AS match_count
FROM candidates c
INNER JOIN report_vouchers rv ON rv.report_id = c.id
INNER JOIN vouchers v ON v.id = rv.voucher_id
GROUP BY v.id, v.chain_id, v.address, v.name, v.symbol
ORDER BY lower(v.name), v.id;`

	return composed{sql: sql, args: args.values}
}

// ListReportsFiltered returns one page of the approved report feed.
//
// With no active filter it delegates to the existing static statements, so the feed's measured
// plans are untouched.
func (pg *Pg) ListReportsFiltered(
	ctx context.Context,
	tx pgx.Tx,
	filter CatalogFilter,
	cursor ReportCursor,
	hasCursor bool,
	limit uint64,
) ([]Report, error) {
	if !filter.Active() {
		if hasCursor {
			return pg.ListReportsNext(ctx, tx, cursor, limit)
		}
		return pg.ListReports(ctx, tx, limit)
	}

	query := composeReportPage(filter, cursor, hasCursor, limit)

	var reports []Report
	if err := pgxscan.Select(ctx, tx, &reports, query.sql, query.args...); err != nil {
		return nil, err
	}
	return reports, nil
}

// ListReportTagFacets returns the direct report tag facet for the current search.
//
// Reports have no viewport, so the filter is the only narrowing dimension — the same shape as
// the pool facet.
func (pg *Pg) ListReportTagFacets(
	ctx context.Context,
	tx pgx.Tx,
	filter CatalogFilter,
) ([]TagFacet, error) {
	return pg.listTagFacets(ctx, tx,
		composeReportTagCounts(filter),
		pg.queries.ListReportTagVocabulary,
		filter.Active(),
		filter.Tags,
	)
}

// ListReportVoucherFacets returns relationship-scoped voucher choices for the
// current report search and tag filters.
func (pg *Pg) ListReportVoucherFacets(
	ctx context.Context,
	tx pgx.Tx,
	filter CatalogFilter,
) ([]VoucherFacet, error) {
	query := composeReportVoucherCounts(filter)
	var facets []VoucherFacet
	if err := pgxscan.Select(ctx, tx, &facets, query.sql, query.args...); err != nil {
		return nil, err
	}
	return facets, nil
}
