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
}
