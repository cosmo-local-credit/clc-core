package store

import (
	"strings"
	"testing"
)

func TestOfferingDiscoveryComposesAllPublicPredicates(t *testing.T) {
	filter, err := NewCatalogFilter("repair", []string{"Services"}, TagMatchAll)
	if err != nil {
		t.Fatal(err)
	}
	filter, err = filter.WithPoolIDs([]uint64{7, 8})
	if err != nil {
		t.Fatal(err)
	}
	filter.Nearby = &CatalogLocation{Lat: -1.2, Lng: 36.8}
	query := composeOfferingPage(filter, OfferingCursor{ID: 20, Distance: 15}, 21)
	for _, fragment := range []string{
		"o.available", "v.active", "COALESCE(o.geo_lat, v.geo_lat) IS NOT NULL",
		"o.commodity_name", "o.description", "search_ot.tag_value",
		"selected_ot.tag_value", "discovery_pt.is_allowed", "distance_meters ASC",
		"v.icon_url AS voucher_image_url", "effective_location_name",
	} {
		if !strings.Contains(query.sql, fragment) {
			t.Errorf("offering query missing %q:\n%s", fragment, query.sql)
		}
	}
	assertPlaceholdersMatchArgs(t, "offering discovery", query)
}

func TestOfferingDiscoveryGlobalAndNearbyCursorOrdering(t *testing.T) {
	global := composeOfferingPage(CatalogFilter{}, OfferingCursor{ID: 9}, 20)
	if !strings.Contains(global.sql, "id < $1") || !strings.Contains(global.sql, "page.id DESC") {
		t.Fatalf("global cursor is not newest-first keyset pagination:\n%s", global.sql)
	}
	nearby := composeOfferingPage(CatalogFilter{Nearby: &CatalogLocation{Lat: 0, Lng: 0}}, OfferingCursor{ID: 9, Distance: 12}, 20)
	if !strings.Contains(nearby.sql, "(distance_meters, id) >") || !strings.Contains(nearby.sql, "distance_meters ASC") {
		t.Fatalf("nearby cursor is not distance-safe keyset pagination:\n%s", nearby.sql)
	}
}

func TestOfferingFacetOmitsItsOwnTagSelection(t *testing.T) {
	query := composeOfferingTagCounts(CatalogFilter{Tags: []string{"Art"}, PoolIDs: []uint64{3}})
	if strings.Contains(query.sql, "selected_ot") {
		t.Fatalf("facet count must omit its own tag selection:\n%s", query.sql)
	}
	if !strings.Contains(query.sql, "discovery_pt.pool_id") {
		t.Fatalf("facet count must retain pool selection:\n%s", query.sql)
	}
}
