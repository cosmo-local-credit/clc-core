package store

import (
	"context"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

// Filtered market voucher pages and the voucher tag facet.
//
// Visibility is unchanged from the unfiltered catalog: active vouchers with
// complete coordinates. The endpoint backs a map, so a voucher with no
// coordinates has never been listable through it and search does not change
// that — a name search will not surface an ungeolocated voucher. Lifting that
// restriction is an endpoint-contract change, not a search change.

// marketVoucherPageProjection hydrates one already-selected page of voucher
// IDs. Selecting the page first and joining afterwards keeps the per-row
// lateral kind lookup off every candidate row (PG_SEARCH_PLAN.md
// "Candidate-First Query Shape"). It is byte-identical in output to the static
// statements' projection, so a filtered page and an unfiltered page return the
// same fields.
const (
	marketVoucherPageProjection = `
SELECT v.id, v.chain_id, v.address, v.name, v.symbol, v.icon_url,
    v.location_name, v.geo_lat, v.geo_lng,
    CASE dominant.commodity_type
        WHEN 'GOOD' THEN 'goods'
        WHEN 'SERVICE' THEN 'services'
        -- commodity_type is FK-constrained to GOOD/SERVICE, so ELSE represents
        -- the LEFT JOIN miss (no available offerings), never an unknown type.
        -- A future third commodity type must extend this CASE, not fall here.
        ELSE 'voucher'
    END AS kind
FROM page
INNER JOIN vouchers v ON v.id = page.id
LEFT JOIN LATERAL (
    SELECT o.commodity_type
    FROM offerings o
    WHERE o.voucher_id = page.id AND o.available
    GROUP BY o.commodity_type
    ORDER BY COUNT(*) DESC, o.commodity_type ASC
    LIMIT 1
) dominant ON TRUE
ORDER BY v.id DESC;`
)

// boundsPredicate returns the viewport predicate. It is a driving predicate --
// the GiST point index is what makes a bounded page cheap — so it is emitted
// as literal SQL when a viewport is present and omitted entirely when it is
// not, never hidden behind a nullable `$1::box IS NULL OR ...` switch that
// would stop the planner from choosing the index.
func boundsPredicate(args *sqlArgs, bounds *MarketVoucherBounds) string {
	if bounds == nil {
		return ""
	}
	return "point(v.geo_lng, v.geo_lat) <@ box(point(" +
		args.bind(bounds.MinLng) + ", " + args.bind(bounds.MinLat) + "), point(" +
		args.bind(bounds.MaxLng) + ", " + args.bind(bounds.MaxLat) + "))"
}

// composeMarketVoucherPage builds one filtered voucher page.
//
// Two driving shapes, selected by whether a search term is present.
//
// With a term, the match set is materialized before ordering and limiting, and
// that is what keeps the trigram index driving. Adding the keyset predicate to a
// query already ordered by `v.id DESC LIMIT n` makes vouchers_pkey look like it
// serves the filter, the ordering, and the early abort at once, so the planner
// takes it and demotes the search to a row filter. Measured over 13,181 active
// vouchers on a cursor page, that plan cost 8.7 ms for a rare term and 7.7 ms for
// a no-match term — removing 13,710 and 13,711 rows respectively — against
// 0.085 ms and 0.075 ms with the match set. The same happens once a tag reducer
// joins the search. On a first page the two shapes are identical, so this exists
// for the cursored and tag-combined cases.
//
// Without a term the direct shape is correct and is kept: the backward
// primary-key walk stops as soon as it has a page, and materializing would force
// every candidate to be collected first. Measured for a tag matching ~5,500
// vouchers, direct was 2.8 ms against 7.9 ms materialized. Tags stay reducers in
// both shapes.
func composeMarketVoucherPage(
	bounds *MarketVoucherBounds,
	filter CatalogFilter,
	page CatalogPage,
	cursor, limit uint64,
) composed {
	args := &sqlArgs{}

	if filter.Term == "" {
		// Reducer-only shape: page the vouchers table directly.
		body := "WITH page AS MATERIALIZED (\n" +
			"    SELECT v.id\n" +
			"    FROM vouchers v" +
			whereClause(
				"v.active",
				"v.geo_lat IS NOT NULL",
				boundsPredicate(args, bounds),
				voucherTagPredicate(args, filter),
				cursorPredicate(args, "v.id", page, cursor),
			) +
			"\n    ORDER BY v.id " + pageOrder(page) +
			"\n    LIMIT " + args.bind(limit) +
			"\n)"
		return composed{sql: body + marketVoucherPageProjection, args: args.values}
	}

	// Search shape: let the trigram index build the match set, then page it.
	body := "WITH matches AS MATERIALIZED (\n" +
		"    SELECT v.id\n" +
		"    FROM vouchers v" +
		whereClause(
			"v.active",
			"v.geo_lat IS NOT NULL",
			boundsPredicate(args, bounds),
			searchPredicate(args, filter, "v"),
			voucherTagPredicate(args, filter),
		) +
		"\n),\npage AS MATERIALIZED (\n" +
		"    SELECT m.id\n" +
		"    FROM matches m" +
		whereClause(cursorPredicate(args, "m.id", page, cursor)) +
		"\n    ORDER BY m.id " + pageOrder(page) +
		"\n    LIMIT " + args.bind(limit) +
		"\n)"
	return composed{sql: body + marketVoucherPageProjection, args: args.values}
}

// composeMarketVoucherTagCounts counts, per tag, how many vouchers the current
// filter leaves under it.
//
// Counts are disjunctive: the candidate set carries the search term and every
// non-tag filter but deliberately omits the tag filter itself. Selecting one tag
// therefore does not zero every other tag's count, so the user can always
// broaden their selection.
//
// The query drives from the candidate set outwards — candidates, then their
// available offerings, then those offerings' tags — rather than from the tag
// junction inwards. That is what keeps the trigram index driving the search:
// starting from offering_tags made every plan a hash join over the whole
// junction and the search collapsed into a row filter. The facet vocabulary is a
// separate statement for the same reason (see mergeTagFacets).
//
// COUNT(DISTINCT c.id) because the derived join fans out: one voucher with three
// available offerings carrying the same tag is one match, not three.
func composeMarketVoucherTagCounts(bounds *MarketVoucherBounds, filter CatalogFilter) composed {
	args := &sqlArgs{}

	sql := `WITH candidates AS MATERIALIZED (
    SELECT v.id
    FROM vouchers v` +
		whereClause(
			"v.active",
			"v.geo_lat IS NOT NULL",
			boundsPredicate(args, bounds),
			searchPredicate(args, filter, "v"),
		) + `
)
SELECT ot.tag_value AS tag_value,
    COUNT(DISTINCT c.id) AS match_count
FROM candidates c
INNER JOIN offerings o ON o.voucher_id = c.id AND o.available
INNER JOIN offering_tags ot ON ot.offering_id = o.id
GROUP BY ot.tag_value;`

	return composed{sql: sql, args: args.values}
}

// ListMarketVouchersFiltered returns one page of the public voucher catalog.
//
// With no active filter it delegates to the existing static statements, so the
// map's bulk-import path keeps exactly the plans it was measured with. Every
// direction applies every active predicate: there is no path where a filtered
// request falls through to an unfiltered statement.
func (pg *Pg) ListMarketVouchersFiltered(
	ctx context.Context,
	tx pgx.Tx,
	bounds *MarketVoucherBounds,
	filter CatalogFilter,
	page CatalogPage,
	cursor, limit uint64,
) ([]MarketVoucher, error) {
	if !filter.Active() {
		switch page {
		case CatalogFirstPage:
			return pg.ListMarketVouchers(ctx, tx, bounds, limit)
		case CatalogNextPage:
			return pg.ListMarketVouchersNext(ctx, tx, bounds, cursor, limit)
		case CatalogPrevPage:
			return pg.ListMarketVouchersPrevious(ctx, tx, bounds, cursor, limit)
		}
	}

	query := composeMarketVoucherPage(bounds, filter, page, cursor, limit)

	var vouchers []MarketVoucher
	if err := pgxscan.Select(ctx, tx, &vouchers, query.sql, query.args...); err != nil {
		return nil, err
	}
	return vouchers, nil
}

// ListMarketVoucherTagFacets returns the tag facet for the current search and
// viewport. Callers request it for the first page only: a facet describes the
// whole filtered set, not the page, so recalculating it per cursor page would
// return the same answer at the same cost.
//
// A voucher facet narrows on the viewport as well as on the filter, so a bounded
// request is narrowed even with no term and no tags: its count keys describe the
// tags inside the camera, which is not the vocabulary.
func (pg *Pg) ListMarketVoucherTagFacets(
	ctx context.Context,
	tx pgx.Tx,
	bounds *MarketVoucherBounds,
	filter CatalogFilter,
) ([]TagFacet, error) {
	return pg.listTagFacets(ctx, tx,
		composeMarketVoucherTagCounts(bounds, filter),
		pg.queries.ListVoucherTagVocabulary,
		bounds != nil || filter.Active(),
		filter.Tags,
	)
}
