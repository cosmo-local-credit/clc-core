package store

import (
	"strings"
	"testing"
)

func TestLoadCatalogQueries(t *testing.T) {
	queries, err := loadQueries("../../core_queries.sql")
	if err != nil {
		t.Fatalf("load core queries: %v", err)
	}

	for name, query := range map[string]string{
		"list-pool-vouchers":          queries.ListPoolVouchers,
		"list-pool-vouchers-next":     queries.ListPoolVouchersNext,
		"list-pool-vouchers-prev":     queries.ListPoolVouchersPrev,
		"list-offerings":              queries.ListOfferings,
		"list-offerings-next":         queries.ListOfferingsNext,
		"list-offerings-prev":         queries.ListOfferingsPrev,
		"list-voucher-offerings":      queries.ListVoucherOfferings,
		"list-voucher-offerings-next": queries.ListVoucherOfferingsNext,
		"list-voucher-offerings-prev": queries.ListVoucherOfferingsPrev,
		"list-pool-offerings":         queries.ListPoolOfferings,
		"list-pool-offerings-next":    queries.ListPoolOfferingsNext,
		"list-pool-offerings-prev":    queries.ListPoolOfferingsPrev,
	} {
		if query == "" {
			t.Errorf("expected %s query to be loaded", name)
		}
	}

	if !strings.Contains(queries.ListPoolVouchers, "oracle_feeds") {
		t.Fatal("expected pool vouchers to include their oracle feed")
	}
	if !strings.Contains(queries.ListPoolVouchers, "offer_count") {
		t.Fatal("expected pool vouchers to include offering counts")
	}
	if strings.Contains(queries.ListPoolVouchers, "exchange_rate") {
		t.Fatal("pool vouchers must not expose legacy fixed exchange rates")
	}
	if !strings.Contains(queries.ListOfferings, "offering_tags") {
		t.Fatal("expected offerings to include tags")
	}

	for name, query := range map[string]string{
		"list-voucher-offerings":      queries.ListVoucherOfferings,
		"list-voucher-offerings-next": queries.ListVoucherOfferingsNext,
		"list-voucher-offerings-prev": queries.ListVoucherOfferingsPrev,
	} {
		if !strings.Contains(query, "lower(v.address) = lower($1) AND o.available AND v.active") {
			t.Errorf("%s must filter by address, offering availability, and voucher activity", name)
		}
	}

	// A pool's offerings must answer exactly as the voucher's own page does, so
	// the availability and activity predicates are the same ones asserted above;
	// only the membership predicate is added.
	for name, query := range map[string]string{
		"list-pool-offerings":      queries.ListPoolOfferings,
		"list-pool-offerings-next": queries.ListPoolOfferingsNext,
		"list-pool-offerings-prev": queries.ListPoolOfferingsPrev,
	} {
		if !strings.Contains(query, "lower(p.address) = lower($1) AND pt.is_allowed AND o.available AND v.active") {
			t.Errorf("%s must filter by pool address, allowance, offering availability, and voucher activity", name)
		}
		if !strings.Contains(query, "offering_tags") {
			t.Errorf("%s must include offering tags", name)
		}
		if !strings.Contains(query, "v.symbol AS voucher_symbol") {
			t.Errorf("%s must name the voucher each offering belongs to", name)
		}
	}
}

func TestDeployQueriesPersistTermsIntoCatalogs(t *testing.T) {
	queries, err := loadQueries("../../core_queries.sql")
	if err != nil {
		t.Fatalf("load core queries: %v", err)
	}

	for name, query := range map[string]string{
		"insert deployed token": queries.InsertDeployedToken,
		"insert deployed pool":  queries.InsertDeployedPool,
	} {
		if !strings.Contains(query, "terms") {
			t.Errorf("%s must persist terms", name)
		}
	}

	for name, query := range map[string]string{
		"promote voucher": queries.UpsertVoucherFromDeploy,
		"promote pool":    queries.UpsertPoolFromDeploy,
	} {
		if !strings.Contains(query, "d.terms") {
			t.Errorf("%s must copy deploy terms into the catalog", name)
		}
		if !strings.Contains(query, "COALESCE(EXCLUDED.terms") {
			t.Errorf("%s must let non-empty deploy terms update the catalog", name)
		}
	}
}
