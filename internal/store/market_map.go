package store

import (
	"context"
	"encoding/json"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

const (
	MarketMapMaxFeatures = 2000
	// MarketMapPointsCeiling bounds one points response. A catalog that reaches
	// it sets Truncated rather than dropping points silently; well before then
	// the map belongs on vector tiles.
	MarketMapPointsCeiling = 50_000
)

type (
	MarketMapFeature struct {
		Type          string         `db:"feature_type" json:"type"`
		ID            string         `db:"feature_id" json:"id"`
		Kind          *string        `db:"kind" json:"kind,omitempty"`
		ResourceID    *string        `db:"resource_id" json:"resourceId,omitempty"`
		Name          *string        `db:"name" json:"name,omitempty"`
		GeoLat        float64        `db:"geo_lat" json:"lat"`
		GeoLng        float64        `db:"geo_lng" json:"lng"`
		MarkerIcon    *string        `db:"marker_icon" json:"markerIcon,omitempty"`
		MarkerColor   *string        `db:"marker_color" json:"markerColor,omitempty"`
		Count         uint64         `db:"feature_count" json:"count,omitempty"`
		Kinds         map[string]int `db:"-" json:"kinds,omitempty"`
		ExpansionZoom float64        `db:"expansion_zoom" json:"expansionZoom,omitempty"`
		KindsJSON     []byte         `db:"kinds" json:"-"`
		FacetsJSON    []byte         `db:"tag_facets" json:"-"`
	}

	MarketMapResult struct {
		Features          []MarketMapFeature
		TagFacets         []TagFacet
		PostVoucherFacets []VoucherFacet
		PostPoolFacets    []PoolFacet
	}

	MarketMapPoint struct {
		Kind        string  `json:"kind"`
		ResourceID  string  `json:"resourceId"`
		Name        string  `json:"name"`
		GeoLat      float64 `json:"lat"`
		GeoLng      float64 `json:"lng"`
		MarkerIcon  *string `json:"markerIcon"`
		MarkerColor *string `json:"markerColor"`
	}

	MarketMapPoints struct {
		Points    []MarketMapPoint
		Truncated bool
	}

	MarketMapFacets struct {
		TagFacets         []TagFacet
		PostVoucherFacets []VoucherFacet
		PostPoolFacets    []PoolFacet
	}

	marketMapDiscoveryRow struct {
		Kind        *string  `db:"kind"`
		SortID      *int64   `db:"sort_id"`
		ResourceID  *string  `db:"resource_id"`
		Name        *string  `db:"name"`
		GeoLat      *float64 `db:"geo_lat"`
		GeoLng      *float64 `db:"geo_lng"`
		MarkerIcon  *string  `db:"marker_icon"`
		MarkerColor *string  `db:"marker_color"`
		Tag         *string  `db:"tag"`
		TagCount    *int64   `db:"tag_count"`
	}
)

func (pg *Pg) ListMarketMap(
	ctx context.Context,
	tx pgx.Tx,
	filter CatalogFilter,
	kinds []string,
	zoom float64,
	includeFacets bool,
) (MarketMapResult, error) {
	hasBounds, minLng, minLat, maxLng, maxLat, hasSecond, secondMinLng, secondMinLat, secondMaxLng, secondMaxLat := catalogBoundsQueryArgs(filter.Bounds)
	rows := []MarketMapFeature{}
	err := pgxscan.Select(ctx, tx, &rows, pg.queries.ListMarketMap,
		filter.likeTerm(), filter.prefixOnly(), filter.Tags, filter.TagMatch == TagMatchAll,
		filter.DiscoveryPoolIDs, kinds, zoom, includeFacets,
		hasBounds, minLng, minLat, maxLng, maxLat,
		hasSecond, secondMinLng, secondMinLat, secondMaxLng, secondMaxLat,
		MarketMapMaxFeatures,
		filter.PostedAfter,
		filter.VoucherIDs, filter.VoucherMatch == TagMatchAll,
		filter.PoolIDs, filter.PoolMatch == TagMatchAll,
		filter.ReportTiming,
	)
	if err != nil {
		return MarketMapResult{}, err
	}

	result := MarketMapResult{Features: rows, TagFacets: []TagFacet{}}
	for index := range result.Features {
		feature := &result.Features[index]
		if feature.Type == "cluster" {
			feature.Kinds = map[string]int{}
		}
		if feature.Type == "cluster" && len(feature.KindsJSON) > 0 {
			if err := json.Unmarshal(feature.KindsJSON, &feature.Kinds); err != nil {
				return MarketMapResult{}, err
			}
		} else if feature.Type == "marker" {
			feature.Count = 0
		}
	}
	if len(rows) > 0 && len(rows[0].FacetsJSON) > 0 {
		if err := json.Unmarshal(rows[0].FacetsJSON, &result.TagFacets); err != nil {
			return MarketMapResult{}, err
		}
	}
	return result, nil
}

// ListMarketMapPoints returns every matching point, whatever the viewport. The
// client clusters them, so filter.Bounds is ignored.
func (pg *Pg) ListMarketMapPoints(
	ctx context.Context,
	tx pgx.Tx,
	filter CatalogFilter,
	kinds []string,
) (MarketMapPoints, error) {
	filter.Bounds = nil
	rows, err := pg.listMarketMapDiscovery(ctx, tx, filter, kinds, false)
	if err != nil {
		return MarketMapPoints{}, err
	}
	result := MarketMapPoints{Points: make([]MarketMapPoint, 0, len(rows))}
	for _, row := range rows {
		if row.Kind == nil || row.ResourceID == nil || row.GeoLat == nil || row.GeoLng == nil {
			continue
		}
		if len(result.Points) == MarketMapPointsCeiling {
			result.Truncated = true
			break
		}
		point := MarketMapPoint{
			Kind: *row.Kind, ResourceID: *row.ResourceID, GeoLat: *row.GeoLat, GeoLng: *row.GeoLng,
			MarkerIcon: row.MarkerIcon, MarkerColor: row.MarkerColor,
		}
		if row.Name != nil {
			point.Name = *row.Name
		}
		result.Points = append(result.Points, point)
	}
	return result, nil
}

// ListMarketMapTagFacets counts the tags of the points inside filter.Bounds.
func (pg *Pg) ListMarketMapTagFacets(
	ctx context.Context,
	tx pgx.Tx,
	filter CatalogFilter,
	kinds []string,
) ([]TagFacet, error) {
	rows, err := pg.listMarketMapDiscovery(ctx, tx, filter, kinds, true)
	if err != nil {
		return nil, err
	}
	facets := make([]TagFacet, 0, len(rows))
	for _, row := range rows {
		if row.Tag == nil || row.TagCount == nil {
			continue
		}
		facets = append(facets, TagFacet{Tag: *row.Tag, Count: uint64(*row.TagCount)})
	}
	return facets, nil
}

func (pg *Pg) listMarketMapDiscovery(
	ctx context.Context,
	tx pgx.Tx,
	filter CatalogFilter,
	kinds []string,
	facets bool,
) ([]marketMapDiscoveryRow, error) {
	hasBounds, minLng, minLat, maxLng, maxLat, hasSecond, secondMinLng, secondMinLat, secondMaxLng, secondMaxLat := catalogBoundsQueryArgs(filter.Bounds)
	rows := []marketMapDiscoveryRow{}
	err := pgxscan.Select(ctx, tx, &rows, pg.queries.ListMarketMapDiscovery,
		filter.likeTerm(), filter.prefixOnly(), filter.Tags, filter.TagMatch == TagMatchAll,
		filter.DiscoveryPoolIDs, kinds, facets,
		hasBounds, minLng, minLat, maxLng, maxLat,
		hasSecond, secondMinLng, secondMinLat, secondMaxLng, secondMaxLat,
		MarketMapPointsCeiling+1,
		filter.PostedAfter,
		filter.VoucherIDs, filter.VoucherMatch == TagMatchAll,
		filter.PoolIDs, filter.PoolMatch == TagMatchAll,
		filter.ReportTiming,
	)
	return rows, err
}
