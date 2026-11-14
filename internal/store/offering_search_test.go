package store

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestOfferingDiscoveryUsesNamedQueriesWithAllPublicPredicates(t *testing.T) {
	queries, err := loadQueries("../../core_queries.sql")
	if err != nil {
		t.Fatal(err)
	}
	filter, err := NewCatalogFilter("repair", []string{"Services"}, TagMatchAll)
	if err != nil {
		t.Fatal(err)
	}
	filter, err = filter.WithPoolIDs([]uint64{7, 8})
	if err != nil {
		t.Fatal(err)
	}
	filter.Nearby = &CatalogLocation{Lat: -1.2, Lng: 36.8}
	query, args := offeringPageQuery(queries, filter, OfferingCursor{ID: 20, Distance: 15}, 21)
	if query != queries.ListOfferingsFilteredNearby || len(args) != 15 {
		t.Fatalf("nearby selection = (%t, %d args)", query == queries.ListOfferingsFilteredNearby, len(args))
	}
	for _, fragment := range []string{
		"o.available", "v.active", "COALESCE(o.geo_lat, v.geo_lat) IS NOT NULL",
		"o.commodity_name", "o.description", "search_ot.tag_value",
		"selected_ot.tag_value", "pt.is_allowed", "distance_meters ASC",
		"v.icon_url AS voucher_image_url", "effective_location_name",
		"voucher_uoa", "price_in_unit_of_account",
	} {
		if !strings.Contains(query, fragment) {
			t.Errorf("offering query missing %q", fragment)
		}
	}
}

func TestOfferingDiscoverySelectsGlobalAndNearbyKeysets(t *testing.T) {
	queries, err := loadQueries("../../core_queries.sql")
	if err != nil {
		t.Fatal(err)
	}
	global, globalArgs := offeringPageQuery(queries, CatalogFilter{}, OfferingCursor{ID: 9}, 20)
	if global != queries.ListOfferingsFilteredGlobal || len(globalArgs) != 18 || !strings.Contains(global, "id < $6") {
		t.Fatalf("global query selection or keyset is invalid")
	}
	nearby, nearbyArgs := offeringPageQuery(queries, CatalogFilter{Nearby: &CatalogLocation{Lat: 0, Lng: 0}}, OfferingCursor{ID: 9, Distance: 12}, 20)
	if nearby != queries.ListOfferingsFilteredNearby || len(nearbyArgs) != 15 || !strings.Contains(nearby, "(distance_meters, id) >") {
		t.Fatalf("nearby query selection or keyset is invalid")
	}
}

func TestOfferingViewportUsesEffectiveLocation(t *testing.T) {
	queries, err := loadQueries("../../core_queries.sql")
	if err != nil {
		t.Fatal(err)
	}
	filter := CatalogFilter{Bounds: []CatalogBounds{{MinLng: 36, MinLat: -2, MaxLng: 37, MaxLat: -1}}}
	query, args := offeringPageQuery(queries, filter, OfferingCursor{}, 20)
	if len(args) != 18 || args[7] != true || args[12] != false {
		t.Fatalf("unexpected bbox args: %#v", args)
	}
	if !strings.Contains(query, "point(COALESCE(o.geo_lng,v.geo_lng),COALESCE(o.geo_lat,v.geo_lat))") {
		t.Fatal("offering viewport must use the offering location with voucher fallback")
	}
}

func TestOfferingFacetOmitsItsOwnTagSelection(t *testing.T) {
	queries, err := loadQueries("../../core_queries.sql")
	if err != nil {
		t.Fatal(err)
	}
	query, _ := offeringFacetQuery(queries, CatalogFilter{Tags: []string{"Art"}, PoolIDs: []uint64{3}})
	if strings.Contains(query, "selected_ot") {
		t.Fatal("facet count must omit its own tag selection")
	}
	if !strings.Contains(query, "pt.pool_id") {
		t.Fatal("facet count must retain pool selection")
	}
}

func TestOfferingDiscoveryHasNoInlineSQLComposer(t *testing.T) {
	source, err := os.ReadFile("offering_search.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"SELECT ", "FROM offerings", "composeOffering"} {
		if strings.Contains(string(source), forbidden) {
			t.Fatalf("offering discovery Go source contains inline SQL marker %q", forbidden)
		}
	}
}

func TestEveryOfferingProjectionUsesExactVoucherUOAPrice(t *testing.T) {
	queries, err := loadQueries("../../core_queries.sql")
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		queries.ListOfferings, queries.ListOfferingsNext, queries.ListOfferingsPrev,
		queries.ListVoucherOfferings, queries.ListVoucherOfferingsNext, queries.ListVoucherOfferingsPrev,
		queries.ListPoolOfferings, queries.ListPoolOfferingsNext, queries.ListPoolOfferingsPrev,
		queries.GetVoucherOffering, queries.GetOfferingForMutation,
		queries.ListOfferingsFilteredGlobal, queries.ListOfferingsFilteredNearby,
	}
	for index, query := range statements {
		for _, fragment := range []string{
			"v.voucher_uoa AS voucher_uoa",
			"(o.price * v.voucher_value)::TEXT",
			"v.voucher_value > 0",
			"NULLIF(BTRIM(o.unit), '') IS NOT NULL",
		} {
			if !strings.Contains(query, fragment) {
				t.Errorf("offering statement %d missing %q", index, fragment)
			}
		}
	}
}

// The Credit filter's held vouchers ride as the last argument of every
// discovery query that takes it; the SQL must expect exactly that many.
func TestCreditHeldVouchersAreTheLastPlaceholder(t *testing.T) {
	queries, err := loadQueries("../../core_queries.sql")
	if err != nil {
		t.Fatal(err)
	}
	filter := CatalogFilter{CreditVoucherIDs: []uint64{4, 9}}
	near := &CatalogLocation{Lat: -1.2, Lng: 36.8}
	type built struct {
		name  string
		query string
		args  []any
	}
	var cases []built
	add := func(name, query string, args []any) { cases = append(cases, built{name, query, args}) }
	q, a := offeringPageQuery(queries, filter, OfferingCursor{}, 20)
	add("offerings global", q, a)
	q, a = offeringPageQuery(queries, CatalogFilter{CreditVoucherIDs: filter.CreditVoucherIDs, Nearby: near}, OfferingCursor{}, 20)
	add("offerings nearby", q, a)
	q, a = offeringPageQuery(queries, CatalogFilter{CreditVoucherIDs: filter.CreditVoucherIDs, SortOrigin: near}, OfferingCursor{}, 20)
	add("offerings sorted", q, a)
	q, a = offeringFacetQuery(queries, filter)
	add("offering facets global", q, a)
	q, a = offeringFacetQuery(queries, CatalogFilter{CreditVoucherIDs: filter.CreditVoucherIDs, Nearby: near})
	add("offering facets nearby", q, a)
	q, a = curatedCatalogPageQuery(queries, 0, filter, nil, CuratedCatalogCursor{}, 20)
	add("catalogs global", q, a)
	q, a = curatedCatalogPageQuery(queries, 0, filter, near, CuratedCatalogCursor{}, 20)
	add("catalogs nearby", q, a)
	for _, c := range cases {
		highest := 0
		for _, m := range placeholderPattern.FindAllStringSubmatch(c.query, -1) {
			if n, _ := strconv.Atoi(m[1]); n > highest {
				highest = n
			}
		}
		if highest != len(c.args) {
			t.Errorf("%s: SQL expects %d arguments, Go passes %d", c.name, highest, len(c.args))
			continue
		}
		held, ok := c.args[len(c.args)-1].([]int64)
		if !ok || len(held) != 2 || held[0] != 4 || held[1] != 9 {
			t.Errorf("%s: last argument = %#v, want the held voucher ids", c.name, c.args[len(c.args)-1])
		}
		if !strings.Contains(c.query, "=ANY($"+strconv.Itoa(highest)+"::BIGINT[])") {
			t.Errorf("%s: the held vouchers placeholder $%d is not compared", c.name, highest)
		}
	}
}
