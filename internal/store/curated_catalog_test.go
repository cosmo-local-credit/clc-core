package store

import (
	"os"
	"strings"
	"testing"
)

func TestCuratedCatalogNamedSearchCoversNameLocationAndDescription(t *testing.T) {
	queries, err := loadQueries("../../core_queries.sql")
	if err != nil {
		t.Fatal(err)
	}
	query, args := curatedCatalogPageQuery(queries, 0, CatalogFilter{Term: "repair"}, nil, CuratedCatalogCursor{}, 20)
	if query != queries.ListCuratedCatalogsGlobal || len(args) != 8 {
		t.Fatal("global catalog query selection is invalid")
	}
	for _, fragment := range []string{"lower(c.name)", "lower(c.location_name)", "COALESCE(c.description,'')"} {
		if !strings.Contains(query, fragment) {
			t.Errorf("catalog search missing %q", fragment)
		}
	}
}

func TestCuratedCatalogNamedSearchKeepsDistanceCursor(t *testing.T) {
	queries, err := loadQueries("../../core_queries.sql")
	if err != nil {
		t.Fatal(err)
	}
	query, args := curatedCatalogPageQuery(queries, 0, CatalogFilter{Term: "repair"}, &CatalogLocation{Lat: 1, Lng: 2}, CuratedCatalogCursor{ID: 7, Distance: 10}, 20)
	if query != queries.ListCuratedCatalogsNearby || len(args) != 15 || !strings.Contains(query, "(distance_meters,id)>") {
		t.Fatal("nearby catalog query selection or distance keyset is invalid")
	}
}

func TestCuratedCatalogHasNoInlineDiscoverySQL(t *testing.T) {
	source, err := os.ReadFile("curated_catalog.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"SELECT ", "FROM catalogs", "composeCurated"} {
		if strings.Contains(string(source), forbidden) {
			t.Fatalf("catalog Go source contains inline SQL marker %q", forbidden)
		}
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
