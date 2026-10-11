package store

import (
	"context"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

// OfferingCursor is the stable keyset for global and distance-ordered offering
// discovery. Distance is used only when Nearby is active.
type OfferingCursor struct {
	ID       uint64  `json:"id"`
	Distance float64 `json:"distance"`
}

const offeringPageProjection = `
SELECT o.id, o.source_id, o.voucher_id, v.address AS voucher_address,
    COALESCE(v.display_name, v.name) AS voucher_name,
    v.symbol AS voucher_symbol, v.decimals AS voucher_decimals,
    v.icon_url AS voucher_image_url, v.location_name AS voucher_location_name,
    v.geo_lat AS voucher_geo_lat, v.geo_lng AS voucher_geo_lng,
    o.seller_address, o.commodity_name, o.commodity_type, o.description,
    o.quantity::TEXT AS quantity, o.unit, o.frequency, o.price::TEXT AS price,
    o.available, o.location_name, o.geo_lat, o.geo_lng, o.image_url,
    COALESCE(o.location_name, v.location_name) AS effective_location_name,
    COALESCE(o.geo_lat, v.geo_lat) AS effective_geo_lat,
    COALESCE(o.geo_lng, v.geo_lng) AS effective_geo_lng,
    page.distance_meters,
    COALESCE((
        SELECT array_agg(ot.tag_value ORDER BY ot.tag_value)
        FROM offering_tags ot
        WHERE ot.offering_id = o.id
    ), ARRAY[]::TEXT[]) AS tags,
    o.created_at, o.updated_at
FROM page
INNER JOIN offerings o ON o.id = page.id
INNER JOIN vouchers v ON v.id = o.voucher_id
ORDER BY `

func offeringSearchPredicate(args *sqlArgs, filter CatalogFilter) string {
	if filter.Term == "" {
		return ""
	}
	term := args.bind(filter.likeTerm())
	operatorPrefix, operatorSuffix := "'%' || lower(", ") || '%'"
	if filter.prefixOnly() {
		operatorPrefix, operatorSuffix = "lower(", ") || '%'"
	}
	match := func(column string) string {
		return "lower(" + column + ") LIKE " + operatorPrefix + term + operatorSuffix + " ESCAPE '\\'"
	}
	return "(" + match("o.commodity_name") +
		" OR " + match("o.description") +
		" OR " + match("COALESCE(v.display_name, v.name, '')") +
		" OR " + match("COALESCE(v.symbol, '')") +
		` OR EXISTS (
            SELECT 1 FROM offering_tags search_ot
            WHERE search_ot.offering_id = o.id
                AND ` + match("search_ot.tag_value") +
		"))"
}

func offeringTagPredicate(args *sqlArgs, filter CatalogFilter) string {
	if len(filter.Tags) == 0 {
		return ""
	}
	placeholder := args.bind(filter.Tags)
	if filter.TagMatch == TagMatchAll {
		return `(SELECT COUNT(*) FROM offering_tags selected_ot
            WHERE selected_ot.offering_id = o.id
                AND selected_ot.tag_value = ANY(` + placeholder + `::TEXT[])
        ) = cardinality(` + args.reuse() + `::TEXT[])`
	}
	return `EXISTS (SELECT 1 FROM offering_tags selected_ot
            WHERE selected_ot.offering_id = o.id
                AND selected_ot.tag_value = ANY(` + placeholder + `::TEXT[]))`
}

func composeOfferingPage(filter CatalogFilter, cursor OfferingCursor, limit uint64) composed {
	args := &sqlArgs{}
	effectiveLat := "COALESCE(o.geo_lat, v.geo_lat)"
	effectiveLng := "COALESCE(o.geo_lng, v.geo_lng)"
	distance := "NULL::float8"
	if filter.Nearby != nil {
		lat := args.bind(filter.Nearby.Lat) + "::float8"
		lng := args.bind(filter.Nearby.Lng) + "::float8"
		distance = haversineMetersSQL(effectiveLat, effectiveLng, lat, lng)
	}

	cursorPredicate := ""
	order := "id DESC"
	resultOrder := "page.id DESC"
	if cursor.ID != 0 {
		if filter.Nearby == nil {
			cursorPredicate = "id < " + args.bind(cursor.ID)
		} else {
			cursorPredicate = "(distance_meters, id) > (" +
				args.bind(cursor.Distance) + "::float8, " + args.bind(cursor.ID) + ")"
		}
	}
	if filter.Nearby != nil {
		order = "distance_meters ASC, id ASC"
		resultOrder = "page.distance_meters ASC, page.id ASC"
	}

	sql := `WITH candidates AS MATERIALIZED (
    SELECT o.id, ` + distance + ` AS distance_meters
    FROM offerings o
    INNER JOIN vouchers v ON v.id = o.voucher_id` +
		whereClause(
			"o.available",
			"v.active",
			effectiveLat+" IS NOT NULL",
			effectiveLng+" IS NOT NULL",
			nearbyPredicate(args, filter.Nearby, effectiveLat, effectiveLng),
			offeringSearchPredicate(args, filter),
			offeringTagPredicate(args, filter),
			voucherPoolPredicate(args, filter, "v.id"),
		) + `
), page AS MATERIALIZED (
    SELECT id, distance_meters FROM candidates` +
		whereClause(cursorPredicate) + `
    ORDER BY ` + order + `
    LIMIT ` + args.bind(limit) + `
)
` + offeringPageProjection + resultOrder
	return composed{sql: sql, args: args.values}
}

func composeOfferingTagCounts(filter CatalogFilter) composed {
	args := &sqlArgs{}
	effectiveLat := "COALESCE(o.geo_lat, v.geo_lat)"
	effectiveLng := "COALESCE(o.geo_lng, v.geo_lng)"
	sql := `WITH candidates AS MATERIALIZED (
    SELECT o.id
    FROM offerings o
    INNER JOIN vouchers v ON v.id = o.voucher_id` +
		whereClause(
			"o.available",
			"v.active",
			effectiveLat+" IS NOT NULL",
			effectiveLng+" IS NOT NULL",
			nearbyPredicate(args, filter.Nearby, effectiveLat, effectiveLng),
			offeringSearchPredicate(args, filter),
			voucherPoolPredicate(args, filter, "v.id"),
		) + `
)
SELECT ot.tag_value, COUNT(*) AS match_count
FROM candidates c
INNER JOIN offering_tags ot ON ot.offering_id = c.id
GROUP BY ot.tag_value`
	return composed{sql: sql, args: args.values}
}

// ListOfferingsFiltered returns only public, available offerings with an
// effective location, ordered newest-first globally or nearest-first nearby.
func (pg *Pg) ListOfferingsFiltered(
	ctx context.Context,
	tx pgx.Tx,
	filter CatalogFilter,
	cursor OfferingCursor,
	limit uint64,
) ([]Offering, error) {
	query := composeOfferingPage(filter, cursor, limit)
	rows := []Offering{}
	if err := pgxscan.Select(ctx, tx, &rows, query.sql, query.args...); err != nil {
		return nil, err
	}
	return rows, nil
}

// ListOfferingTagFacets returns offering-tag counts after every non-tag
// predicate, plus the stable public vocabulary.
func (pg *Pg) ListOfferingTagFacets(ctx context.Context, tx pgx.Tx, filter CatalogFilter) ([]TagFacet, error) {
	return pg.listTagFacets(ctx, tx, composeOfferingTagCounts(filter), pg.queries.ListOfferingTagVocabulary, true, filter.Tags)
}
