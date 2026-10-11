package store

import (
	"strings"
	"testing"
)

func TestCuratedCatalogSearchCoversNameLocationAndDescription(t *testing.T) {
	query := composeCuratedCatalogPage(0, "repair", nil, CuratedCatalogCursor{}, 20)
	for _, fragment := range []string{"lower(c.name)", "lower(c.location_name)", "COALESCE(c.description, '')"} {
		if !strings.Contains(query.sql, fragment) {
			t.Errorf("catalog search missing %q:\n%s", fragment, query.sql)
		}
	}
	assertPlaceholdersMatchArgs(t, "curated catalog search", query)
}

func TestCuratedCatalogSearchKeepsDistanceCursor(t *testing.T) {
	query := composeCuratedCatalogPage(0, "repair", &CatalogLocation{Lat: 1, Lng: 2}, CuratedCatalogCursor{ID: 7, Distance: 10}, 20)
	if !strings.Contains(query.sql, "(distance_meters, id) >") || !strings.Contains(query.sql, "distance_meters ASC") {
		t.Fatalf("nearby catalog search lost distance-safe pagination:\n%s", query.sql)
	}
}

func TestNullableDescription(t *testing.T) {
	if nullableDescription("") != nil {
		t.Fatal("blank description must persist as NULL")
	}
	value := nullableDescription("Community repair")
	if value == nil || *value != "Community repair" {
		t.Fatalf("description = %v", value)
	}
}
