package store

import (
	"context"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

// Filtered pool catalog pages and the pool tag facet (PG_SEARCH_PLAN.md
// Slice 2). Pools carry no visibility flag and no coordinate requirement, so a
// filtered pool page differs from the unfiltered one only by the predicates the
// caller asked for.
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
SELECT p.id, p.chain_id, p.address, p.name, p.symbol, p.owner_address, p.fee_ppm,
    p.fee_address, p.token_registry_address, p.limiter_address, p.quoter_address,
    p.fee_policy_address, p.protocol_fee_controller_address, p.fees_decoupled,
    p.decimals, p.description, p.unit_of_account, p.location_name, p.geo_lat, p.geo_lng,
    p.email, p.website, p.phone, p.image_url, p.banner_url, p.terms,
    p.default_voucher_id, p.platform_contract_id, p.created_at, p.updated_at,
    (SELECT COUNT(*) FROM pool_tokens pv WHERE pv.pool_id = p.id AND pv.is_allowed) AS voucher_count,
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

// composePoolPage builds one filtered pool page, with the same two driving
// shapes and for the same measured reason as the voucher page: a search term
// materializes its match set so the trigram index drives, and a reducer-only
// query pages the table directly.
func composePoolPage(filter CatalogFilter, page CatalogPage, cursor, limit uint64) composed {
	args := &sqlArgs{}

	if filter.Term == "" {
		body := "WITH page AS MATERIALIZED (\n" +
			"    SELECT p.id\n" +
			"    FROM pools p" +
			whereClause(
				poolTagPredicate(args, filter),
				cursorPredicate(args, "p.id", page, cursor),
			) +
			"\n    ORDER BY p.id " + pageOrder(page) +
			"\n    LIMIT " + args.bind(limit) +
			"\n)"
		return composed{sql: body + poolPageProjection, args: args.values}
	}

	body := "WITH matches AS MATERIALIZED (\n" +
		"    SELECT p.id\n" +
		"    FROM pools p" +
		whereClause(
			searchPredicate(args, filter, "p"),
			poolTagPredicate(args, filter),
		) +
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

	sql := `WITH candidates AS MATERIALIZED (
    SELECT p.id
    FROM pools p` +
		whereClause(searchPredicate(args, filter, "p")) + `
)
SELECT pt.tag_value AS tag_value,
    COUNT(*) AS match_count
FROM candidates c
INNER JOIN pool_tags pt ON pt.pool_id = c.id
GROUP BY pt.tag_value;`

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

// ListPoolTagFacets returns the direct pool tag facet for the current search.
//
// Pools have no viewport, so the filter is the only narrowing dimension.
func (pg *Pg) ListPoolTagFacets(ctx context.Context, tx pgx.Tx, filter CatalogFilter) ([]TagFacet, error) {
	return pg.listTagFacets(ctx, tx,
		composePoolTagCounts(filter),
		pg.queries.ListPoolTagVocabulary,
		filter.Active(),
		filter.Tags,
	)
}
