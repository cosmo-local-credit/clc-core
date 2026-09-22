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
		"list-pool-vouchers":               queries.ListPoolVouchers,
		"list-pool-vouchers-next":          queries.ListPoolVouchersNext,
		"list-pool-vouchers-prev":          queries.ListPoolVouchersPrev,
		"list-offerings":                   queries.ListOfferings,
		"list-offerings-next":              queries.ListOfferingsNext,
		"list-offerings-prev":              queries.ListOfferingsPrev,
		"list-voucher-offerings":           queries.ListVoucherOfferings,
		"list-voucher-offerings-next":      queries.ListVoucherOfferingsNext,
		"list-voucher-offerings-prev":      queries.ListVoucherOfferingsPrev,
		"list-pool-offerings":              queries.ListPoolOfferings,
		"list-pool-offerings-next":         queries.ListPoolOfferingsNext,
		"list-pool-offerings-prev":         queries.ListPoolOfferingsPrev,
		"dashboard-catalog-snapshot":       queries.DashboardCatalogSnapshot,
		"dashboard-catalog-series":         queries.DashboardCatalogSeries,
		"create-report":                    queries.CreateReport,
		"lock-report-author":               queries.LockReportAuthor,
		"update-report":                    queries.UpdateReport,
		"approve-report":                   queries.ApproveReport,
		"delete-report":                    queries.DeleteReport,
		"delete-report-tags":               queries.DeleteReportTags,
		"insert-report-tag":                queries.InsertReportTag,
		"delete-report-vouchers":           queries.DeleteReportVouchers,
		"insert-report-voucher":            queries.InsertReportVoucher,
		"delete-report-images":             queries.DeleteReportImages,
		"insert-report-image":              queries.InsertReportImage,
		"delete-report-attachments":        queries.DeleteReportAttachments,
		"insert-report-attachment":         queries.InsertReportAttachment,
		"get-voucher-offering":             queries.GetVoucherOffering,
		"get-offering-for-mutation":        queries.GetOfferingForMutation,
		"lock-voucher-for-offering-create": queries.LockVoucherForOfferingCreate,
		"set-voucher-active":               queries.SetVoucherActive,
		"admin-get-voucher-by-address":     queries.AdminGetVoucherByAddress,
		"lock-offering-voucher-owner":      queries.LockOfferingVoucherOwner,
		"create-offering":                  queries.CreateOffering,
		"update-offering":                  queries.UpdateOffering,
		"delete-offering":                  queries.DeleteOffering,
	} {
		if query == "" {
			t.Errorf("expected %s query to be loaded", name)
		}
	}
	if queries.DashboardCatalogSeries != "" && !strings.Contains(queries.DashboardCatalogSeries, "generate_series") {
		t.Fatal("expected catalog series to fill buckets in SQL")
	}

	if !strings.Contains(queries.ListPoolVouchers, "oracle_feeds") {
		t.Fatal("expected pool vouchers to include their oracle feed")
	}
	if !strings.Contains(queries.ListOracleFeeds, "WHERE chain_id = $1") {
		t.Fatal("oracle feed choices must be scoped to the requested chain")
	}
	if !strings.Contains(queries.GetOracleFeedByName, "chain_id = $1 AND name = $2") {
		t.Fatal("oracle feed names must be looked up within one chain")
	}
	if !strings.Contains(queries.SetPoolTokenOracle, "chain_id = p.chain_id") {
		t.Fatal("oracle events must resolve an aggregator on the pool's own chain")
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

	// The retire/restore write must be keyed by id, not address: the handler has
	// already locked the row it authorized against, and re-finding it by address
	// would reopen the window that lock closes.
	if !strings.Contains(queries.SetVoucherActive, "WHERE id = $1") {
		t.Fatal("set-voucher-active must write the row the caller locked, by id")
	}
	if !strings.Contains(queries.SetVoucherActive, "SET active = $2") {
		t.Fatal("set-voucher-active must take the new visibility as a parameter, not hard-code one direction")
	}

	// The console's reads exist to reach what the public ones hide. A visibility
	// predicate creeping into either would make retiring a voucher also remove
	// the screen an operator restores it from.
	if strings.Contains(queries.AdminGetVoucherByAddress, "v.active;") ||
		strings.Contains(queries.AdminGetVoucherByAddress, "AND v.active") {
		t.Fatal("admin-get-voucher-by-address must resolve retired vouchers too")
	}
	if !strings.Contains(queries.AdminGetVoucherByAddress, "v.active,") {
		t.Fatal("admin-get-voucher-by-address must project active so the console can render status")
	}
	if !strings.Contains(queries.GetMarketVoucherByAddress, "AND v.active") {
		t.Fatal("get-market-voucher-by-address must keep hiding retired vouchers from the public")
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

// The offering write statements. Each assertion here stands for an authorization
// or data-integrity property that has no other enforcement point: the SQL is
// where "the caller cannot choose the seller" actually lives.
func TestOfferingWriteQueries(t *testing.T) {
	queries, err := loadQueries("../../core_queries.sql")
	if err != nil {
		t.Fatalf("load core queries: %v", err)
	}

	// The public item read answers exactly as the public list does, so an unlisted
	// row or a retired voucher is a 404 and never a readable object.
	if !strings.Contains(queries.GetVoucherOffering, "o.available AND v.active") {
		t.Error("get-voucher-offering must filter availability and voucher activity")
	}
	if !strings.Contains(queries.GetVoucherOffering, "lower(v.address) = lower($1)") {
		t.Error("get-voucher-offering must scope the id to the named voucher")
	}
	// Same projection as the lists, so a client never reconciles two shapes.
	for name, query := range map[string]string{
		"get-voucher-offering":      queries.GetVoucherOffering,
		"get-offering-for-mutation": queries.GetOfferingForMutation,
	} {
		for _, field := range []string{
			"o.quantity::TEXT AS quantity", "o.price::TEXT AS price", "offering_tags",
			"v.symbol AS voucher_symbol", "v.decimals AS voucher_decimals",
		} {
			if !strings.Contains(query, field) {
				t.Errorf("%s must carry %s like the list projection", name, field)
			}
		}
	}
	// Read-back ignores availability: a row is still the caller's right after
	// DELETE unlisted it.
	if strings.Contains(queries.GetOfferingForMutation, "o.available AND") {
		t.Error("get-offering-for-mutation must not filter availability")
	}

	// Authorization reads a locked row, never a request field.
	if !strings.Contains(queries.LockVoucherForOfferingCreate, "FOR UPDATE") {
		t.Error("lock-voucher-for-offering-create must lock the row it authorizes from")
	}
	if !strings.Contains(queries.LockVoucherForOfferingCreate, "owner_address") ||
		!strings.Contains(queries.LockVoucherForOfferingCreate, "active") {
		t.Error("lock-voucher-for-offering-create must report owner and activity")
	}
	// FOR UPDATE OF o, not a bare FOR UPDATE: two sellers editing two offerings on
	// one voucher must not serialize against each other.
	if !strings.Contains(queries.LockOfferingVoucherOwner, "FOR UPDATE OF o") {
		t.Error("lock-offering-voucher-owner must lock only the offering")
	}
	if !strings.Contains(queries.LockOfferingVoucherOwner, "lower(v.address) = lower($2)") {
		t.Error("lock-offering-voucher-owner must make a cross-voucher id a miss")
	}

	// The insert names every column it sets, so a column added later cannot be
	// silently filled from a caller's JSON.
	for _, fragment := range []string{
		"voucher_id, seller_address, commodity_name, commodity_type, description",
		"$7::NUMERIC",
		"VALUES ($1, $2, $3, $4, $5, 1, $6, NULL, $7::NUMERIC, true, NULL, NULL, NULL, $8)",
	} {
		if !strings.Contains(queries.CreateOffering, fragment) {
			t.Errorf("create-offering must contain %q", fragment)
		}
	}

	// Assign-all editing of product fields only. These columns are the immutable
	// relationship, provenance, or legacy import data.
	for _, forbidden := range []string{
		"voucher_id =", "seller_address =", "source_id =", "quantity =",
		"frequency =", "location_name =", "geo_lat =", "available =",
	} {
		if strings.Contains(queries.UpdateOffering, forbidden) {
			t.Errorf("update-offering must not set %s", forbidden)
		}
	}
	if !strings.Contains(queries.UpdateOffering, "price = $6::NUMERIC") {
		t.Error("update-offering must keep the price as decimal text bound to NUMERIC")
	}

	// Delete is an unlist. A physical DELETE would lose the Sarafu source_id a
	// later import needs to reconcile.
	if strings.Contains(queries.DeleteOffering, "DELETE FROM") {
		t.Error("delete-offering must soft delete, not remove the row")
	}
	if !strings.Contains(queries.DeleteOffering, "SET available = false") ||
		!strings.Contains(queries.DeleteOffering, "RETURNING id") {
		t.Error("delete-offering must unlist the row and return its id")
	}
}

func TestPoolProfileAuthorizationQuery(t *testing.T) {
	queries, err := loadQueries("../../core_queries.sql")
	if err != nil {
		t.Fatalf("load core queries: %v", err)
	}

	if !strings.Contains(queries.LockPoolForProfileUpdate, "owner_address") {
		t.Error("lock-pool-for-profile-update must report the relationship it authorizes from")
	}
	if !strings.Contains(queries.LockPoolForProfileUpdate, "FOR UPDATE") {
		t.Error("lock-pool-for-profile-update must lock the row it authorizes from")
	}
	if !strings.Contains(queries.LockPoolForProfileUpdate, "lower(address) = lower($1)") {
		t.Error("lock-pool-for-profile-update must match the catalog address case-insensitively")
	}
}
