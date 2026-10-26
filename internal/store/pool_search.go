package store

import (
	"context"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

// Filtered public pool pages and facets exclude retired pools before pagination.
//
// The local catalog holds two pools, which is not representative of a
// production catalog of thousands. The plan shapes here are the ones the
// voucher slice measured at 5x scale; pool plans must be re-measured once
// representative pool data and ANALYZE have run, and this file's shape choices
// revisited if that measurement disagrees.

// poolPageProjection hydrates one already-selected page of pool IDs.
//
// The static statements aggregate tags with LEFT JOIN pool_tags + GROUP BY p.id
// across the whole scanned set. This uses a lateral over the paged IDs instead,
// so tag aggregation touches one page rather than every candidate, and the
// grouping cannot interact with the filter predicates. Output fields and order
// match the static statements exactly.
const (
	poolPageProjection = `
SELECT p.id, p.chain_id, p.address, COALESCE(p.display_name, p.name) AS name,
    p.name AS contract_name, p.symbol, p.active, p.owner_address, p.fee_ppm,
    p.fee_address, p.token_registry_address, p.limiter_address, p.quoter_address, p.quoter_type,
    p.fee_policy_address, p.protocol_fee_controller_address, p.fees_decoupled,
    p.decimals, p.description, p.unit_of_account, p.location_name, p.geo_lat, p.geo_lng,
    p.email, p.website, p.phone, p.image_url, p.banner_url, p.terms,
    p.default_voucher_id, p.platform_contract_id, p.created_at, p.updated_at,
    (SELECT COUNT(*) FROM pool_tokens pv JOIN vouchers member ON member.id = pv.voucher_id AND member.active WHERE pv.pool_id = p.id AND pv.is_allowed) AS voucher_count,
    COALESCE(pool_tag_list.tags, ARRAY[]::TEXT[]) AS tags
FROM page
INNER JOIN pools p ON p.id = page.id
LEFT JOIN LATERAL (
    SELECT array_agg(pt.tag_value ORDER BY pt.tag_value) AS tags
    FROM pool_tags pt
    WHERE pt.pool_id = p.id
) pool_tag_list ON TRUE
ORDER BY p.id DESC;`
)

// poolTextMatchesCTE finds pools by their own identity or by the identity of an
// active voucher they currently allow. Keeping the two searches as separate
// UNION arms lets PostgreSQL drive each one from its own trigram index instead
// of turning an OR across relationships into a catalog-wide scan.
func poolTextMatchesCTE(args *sqlArgs, filter CatalogFilter) string {
	placeholder := args.bind(filter.likeTerm())
	poolMatch := searchPredicateForPlaceholder(filter, "search_pool", placeholder)
	voucherMatch := searchPredicateForPlaceholder(filter, "search_voucher", placeholder)

	return `pool_text_matches AS MATERIALIZED (
    SELECT search_pool.id
    FROM pools search_pool
    WHERE search_pool.active AND ` + poolMatch + `
    UNION
    SELECT search_token.pool_id AS id
    FROM vouchers search_voucher
    INNER JOIN pool_tokens search_token
        ON search_token.voucher_id = search_voucher.id
        AND search_token.is_allowed
    WHERE search_voucher.active
        AND EXISTS (SELECT 1 FROM pools matched_pool WHERE matched_pool.id = search_token.pool_id AND matched_pool.active)
        AND ` + voucherMatch + `
)`
}

// searchPredicateForPlaceholder returns a displayed-name/symbol search while
// reusing an already-bound escaped search term.
func searchPredicateForPlaceholder(filter CatalogFilter, table, placeholder string) string {
	name := table + ".name"
	if table == "search_pool" || table == "search_voucher" {
		name = "COALESCE(" + table + ".display_name, " + table + ".name)"
	}
	if filter.prefixOnly() {
		return "(lower(" + name + ") LIKE lower(" + placeholder + ") || '%' ESCAPE '\\'" +
			"\n            OR lower(" + table + ".symbol) LIKE lower(" + placeholder + ") || '%' ESCAPE '\\')"
	}
	return "(lower(" + name + ") LIKE '%' || lower(" + placeholder + ") || '%' ESCAPE '\\'" +
		"\n            OR lower(" + table + ".symbol) LIKE '%' || lower(" + placeholder + ") || '%' ESCAPE '\\')"
}

// composePoolPage builds one filtered pool page. A search term materializes
// the union of pool-identity and approved-voucher matches before applying the
// remaining reducers; a reducer-only query pages the pool table directly.
func composePoolPage(filter CatalogFilter, page CatalogPage, cursor, limit uint64) composed {
	args := &sqlArgs{}

	if filter.Term == "" {
		body := "WITH page AS MATERIALIZED (\n" +
			"    SELECT p.id\n" +
			"    FROM pools p" +
			whereClause(
				"p.active",
				assetOwnerPredicate(args, "p", filter),
				catalogBoundsPredicate(args, filter.Bounds, "p.geo_lat", "p.geo_lng"),
				nearbyPredicate(args, filter.Nearby, "p.geo_lat", "p.geo_lng"),
				poolTagPredicate(args, filter),
				poolVoucherPredicate(args, filter),
				cursorPredicate(args, "p.id", page, cursor),
			) +
			"\n    ORDER BY p.id " + pageOrder(page) +
			"\n    LIMIT " + args.bind(limit) +
			"\n)"
		return composed{sql: body + poolPageProjection, args: args.values}
	}

	textMatches := poolTextMatchesCTE(args, filter)
	matchQuery := "    SELECT text_match.id\n" +
		"    FROM pool_text_matches text_match"
	if filter.Owner != "" || filter.Nearby != nil || len(filter.Bounds) > 0 || len(filter.Tags) > 0 || len(filter.VoucherIDs) > 0 {
		matchQuery = "    SELECT p.id\n" +
			"    FROM pool_text_matches text_match\n" +
			"    INNER JOIN pools p ON p.id = text_match.id" +
			whereClause(
				"p.active",
				assetOwnerPredicate(args, "p", filter),
				catalogBoundsPredicate(args, filter.Bounds, "p.geo_lat", "p.geo_lng"),
				nearbyPredicate(args, filter.Nearby, "p.geo_lat", "p.geo_lng"),
				poolTagPredicate(args, filter),
				poolVoucherPredicate(args, filter),
			)
	}

	body := "WITH " + textMatches + ",\nmatches AS MATERIALIZED (\n" +
		matchQuery +
		"\n),\npage AS MATERIALIZED (\n" +
		"    SELECT m.id\n" +
		"    FROM matches m" +
		whereClause(cursorPredicate(args, "m.id", page, cursor)) +
		"\n    ORDER BY m.id " + pageOrder(page) +
		"\n    LIMIT " + args.bind(limit) +
		"\n)"
	return composed{sql: body + poolPageProjection, args: args.values}
}

// composePoolTagCounts counts, per direct tag, how many pools the current filter
// leaves under it. Same disjunctive rule and same candidates-outwards shape as
// the voucher counts. No DISTINCT is needed here — pool_tags' primary key
// already guarantees one row per (pool, tag), so the join cannot fan out.
func composePoolTagCounts(filter CatalogFilter) composed {
	args := &sqlArgs{}

	prefix := "WITH "
	from := "    FROM pools p"
	if filter.Term != "" {
		prefix += poolTextMatchesCTE(args, filter) + ",\n"
		from = "    FROM pool_text_matches text_match\n    INNER JOIN pools p ON p.id = text_match.id"
	}

	sql := prefix + `candidates AS MATERIALIZED (
    SELECT p.id
` + from +
		whereClause(
			"p.active",
			assetOwnerPredicate(args, "p", filter),
			catalogBoundsPredicate(args, filter.Bounds, "p.geo_lat", "p.geo_lng"),
			nearbyPredicate(args, filter.Nearby, "p.geo_lat", "p.geo_lng"),
			poolVoucherPredicate(args, filter),
		) + `
)
SELECT pt.tag_value AS tag_value,
    COUNT(*) AS match_count
FROM candidates c
INNER JOIN pool_tags pt ON pt.pool_id = c.id
GROUP BY pt.tag_value;`

	return composed{sql: sql, args: args.values}
}

// poolVoucherPredicate filters on active, allowed composition rows, matching both the
// voucher_count projection and GET /pools/:address/vouchers. A disallowed
// or retired historical row therefore never makes a pool appear to contain a voucher.
func poolVoucherPredicate(args *sqlArgs, filter CatalogFilter) string {
	if len(filter.VoucherIDs) == 0 {
		return ""
	}

	placeholder := args.bind(voucherIDsParam(filter.VoucherIDs))
	if filter.VoucherMatch == TagMatchAll {
		return `(
            SELECT COUNT(*)
            FROM pool_tokens pv
            JOIN vouchers member ON member.id = pv.voucher_id AND member.active
            WHERE pv.pool_id = p.id
                AND pv.is_allowed
                AND pv.voucher_id = ANY(` + placeholder + `::BIGINT[])
        ) = cardinality(` + args.reuse() + `::BIGINT[])`
	}
	return `EXISTS (
            SELECT 1
            FROM pool_tokens pv
            JOIN vouchers member ON member.id = pv.voucher_id AND member.active
            WHERE pv.pool_id = p.id
                AND pv.is_allowed
                AND pv.voucher_id = ANY(` + placeholder + `::BIGINT[])
        )`
}

// composePoolVoucherCounts returns the allowed member vouchers under the
// current pool search and tag filters, while deliberately omitting the selected
// voucher reducer for disjunctive counts.
func composePoolVoucherCounts(filter CatalogFilter) composed {
	args := &sqlArgs{}

	prefix := "WITH "
	from := "    FROM pools p"
	if filter.Term != "" {
		prefix += poolTextMatchesCTE(args, filter) + ",\n"
		from = "    FROM pool_text_matches text_match\n    INNER JOIN pools p ON p.id = text_match.id"
	}

	sql := prefix + `candidates AS MATERIALIZED (
    SELECT p.id
` + from +
		whereClause(
			"p.active",
			assetOwnerPredicate(args, "p", filter),
			catalogBoundsPredicate(args, filter.Bounds, "p.geo_lat", "p.geo_lng"),
			nearbyPredicate(args, filter.Nearby, "p.geo_lat", "p.geo_lng"),
			poolTagPredicate(args, filter),
		) + `
)
SELECT v.id, v.chain_id, v.address, COALESCE(v.display_name, v.name) AS name, v.symbol,
    COUNT(*) AS match_count
FROM candidates c
INNER JOIN pool_tokens pv ON pv.pool_id = c.id AND pv.is_allowed
INNER JOIN vouchers v ON v.id = pv.voucher_id AND v.active
GROUP BY v.id, v.display_name, v.chain_id, v.address, v.name, v.symbol
ORDER BY lower(COALESCE(v.display_name, v.name)), v.id;`

	return composed{sql: sql, args: args.values}
}

// ListPoolsFiltered returns one page of the pool catalog, delegating to the
// existing static statements when nothing is filtered.
func (pg *Pg) ListPoolsFiltered(
	ctx context.Context,
	tx pgx.Tx,
	filter CatalogFilter,
	page CatalogPage,
	cursor, limit uint64,
) ([]Pool, error) {
	if !filter.Active() {
		switch page {
		case CatalogFirstPage:
			return pg.ListPools(ctx, tx, limit)
		case CatalogNextPage:
			return pg.ListPoolsNext(ctx, tx, cursor, limit)
		case CatalogPrevPage:
			return pg.ListPoolsPrevious(ctx, tx, cursor, limit)
		}
	}

	query := composePoolPage(filter, page, cursor, limit)

	var pools []Pool
	if err := pgxscan.Select(ctx, tx, &pools, query.sql, query.args...); err != nil {
		return nil, err
	}
	return pools, nil
}

// ListPoolTagFacets returns the direct pool tag facet for the current search
// and optional viewport.
func (pg *Pg) ListPoolTagFacets(ctx context.Context, tx pgx.Tx, filter CatalogFilter) ([]TagFacet, error) {
	return pg.listTagFacets(ctx, tx,
		composePoolTagCounts(filter),
		pg.queries.ListPoolTagVocabulary,
		filter.Active(),
		filter.Tags,
	)
}

// ListPoolVoucherFacets returns allowed member-voucher choices for the current
// pool search and tag filters.
func (pg *Pg) ListPoolVoucherFacets(
	ctx context.Context,
	tx pgx.Tx,
	filter CatalogFilter,
) ([]VoucherFacet, error) {
	query := composePoolVoucherCounts(filter)
	var facets []VoucherFacet
	if err := pgxscan.Select(ctx, tx, &facets, query.sql, query.args...); err != nil {
		return nil, err
	}
	return facets, nil
}
