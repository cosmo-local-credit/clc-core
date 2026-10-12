package store

import (
	"os"
	"strings"
	"testing"
)

func TestMarketplaceDiscoveryUsesOneStaticNamedQuery(t *testing.T) {
	queries, err := loadQueries("../../core_queries.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		"'offering'::TEXT AS kind",
		"'voucher'",
		"'pool'",
		"'catalog'",
		"o.available AND v.active",
		"pool_tokens",
		"catalog_tags",
		"distance_meters",
		"facet_counts",
		"price_in_unit_of_account",
	} {
		if !strings.Contains(queries.ListMarketplace, fragment) {
			t.Errorf("marketplace query missing %q", fragment)
		}
	}

	source, err := os.ReadFile("marketplace.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"SELECT ", "FROM offerings", "FROM catalogs"} {
		if strings.Contains(string(source), forbidden) {
			t.Fatalf("marketplace Go source contains inline SQL marker %q", forbidden)
		}
	}
}

func TestMarketplaceQueryKeepsCursorAndFacetScopeInPostgres(t *testing.T) {
	queries, err := loadQueries("../../core_queries.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		"cardinality($6::TEXT[])=0 OR kind=ANY($6::TEXT[])",
		"cardinality($5::BIGINT[])=0",
		"rank>$14::INT",
		"kind>$17::TEXT",
		"LIMIT $19",
		"CASE WHEN $20::BOOLEAN",
	} {
		if !strings.Contains(queries.ListMarketplace, fragment) {
			t.Errorf("marketplace keyset/facet query missing %q", fragment)
		}
	}
}
