package store

import (
	"context"
	"encoding/json"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

const (
	MarketMapMaxFeatures = 2000
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
		Features  []MarketMapFeature
		TagFacets []TagFacet
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
		filter.PoolIDs, kinds, zoom, includeFacets,
		hasBounds, minLng, minLat, maxLng, maxLat,
		hasSecond, secondMinLng, secondMinLat, secondMaxLng, secondMaxLat,
		MarketMapMaxFeatures,
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
