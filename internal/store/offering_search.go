package store

import (
	"context"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

// OfferingCursor is the stable keyset for global and distance-ordered offering
// discovery. Distance is used only when Nearby is active.
type (
	OfferingCursor struct {
		ID       uint64  `json:"id"`
		Distance float64 `json:"distance"`
	}
)

func offeringSearchArgs(filter CatalogFilter) (string, []string, bool, []uint64) {
	tags := filter.Tags
	if tags == nil {
		tags = []string{}
	}
	pools := filter.PoolIDs
	if pools == nil {
		pools = []uint64{}
	}
	return filter.likeTerm(), tags, filter.TagMatch == TagMatchAll, pools
}

func offeringPageQuery(queries *Queries, filter CatalogFilter, cursor OfferingCursor, limit uint64) (string, []any) {
	term, tags, allTags, pools := offeringSearchArgs(filter)
	query := queries.ListOfferingsFilteredGlobal
	args := []any{term, filter.prefixOnly(), tags, allTags, pools, cursor.ID, limit}
	if filter.Nearby != nil {
		query = queries.ListOfferingsFilteredNearby
		bounds := filter.Nearby.bounds()
		args = []any{
			term, filter.prefixOnly(), tags, allTags, pools,
			filter.Nearby.Lat, filter.Nearby.Lng,
			bounds.MinLng, bounds.MinLat, bounds.MaxLng, bounds.MaxLat,
			cursor.Distance, cursor.ID, limit,
		}
	}
	return query, args
}

func offeringFacetQuery(queries *Queries, filter CatalogFilter) (string, []any) {
	term, _, _, pools := offeringSearchArgs(filter)
	query := queries.ListOfferingTagCountsGlobal
	args := []any{term, filter.prefixOnly(), pools}
	if filter.Nearby != nil {
		query = queries.ListOfferingTagCountsNearby
		bounds := filter.Nearby.bounds()
		args = []any{
			term, filter.prefixOnly(), pools, filter.Nearby.Lat, filter.Nearby.Lng,
			bounds.MinLng, bounds.MinLat, bounds.MaxLng, bounds.MaxLat,
		}
	}
	return query, args
}

// ListOfferingsFiltered returns only public, available offerings with an
// effective location, ordered newest-first globally or nearest-first nearby.
// SQL shape selection stays in Go; filtering, location fallback, distance,
// pagination, and price conversion are all performed by named statements.
func (pg *Pg) ListOfferingsFiltered(
	ctx context.Context,
	tx pgx.Tx,
	filter CatalogFilter,
	cursor OfferingCursor,
	limit uint64,
) ([]Offering, error) {
	query, args := offeringPageQuery(pg.queries, filter, cursor, limit)

	rows := []Offering{}
	if err := pgxscan.Select(ctx, tx, &rows, query, args...); err != nil {
		return nil, err
	}
	return rows, nil
}

// ListOfferingTagFacets returns offering-tag counts after every non-tag
// predicate, plus the stable public vocabulary.
func (pg *Pg) ListOfferingTagFacets(ctx context.Context, tx pgx.Tx, filter CatalogFilter) ([]TagFacet, error) {
	query, args := offeringFacetQuery(pg.queries, filter)

	var rows []TagFacet
	if err := pgxscan.Select(ctx, tx, &rows, query, args...); err != nil {
		return nil, err
	}
	counts := make(map[string]uint64, len(rows))
	for _, row := range rows {
		counts[row.Tag] = row.Count
	}

	var vocabulary []string
	if err := pgxscan.Select(ctx, tx, &vocabulary, pg.queries.ListOfferingTagVocabulary); err != nil {
		return nil, err
	}
	return mergeTagFacets(vocabulary, counts, filter.Tags), nil
}
