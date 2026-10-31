package store

import (
	"strings"
	"testing"
)

func TestComposeRetirementAdminPage(t *testing.T) {
	cases := []struct {
		name         string
		status       string
		resourceType string
		page         CatalogPage
	}{
		{name: "pending vouchers first", status: "pending", resourceType: "voucher", page: CatalogFirstPage},
		{name: "pending pools next", status: "pending", resourceType: "pool", page: CatalogNextPage},
		{name: "all previous", status: "all", resourceType: "", page: CatalogPrevPage},
		{name: "unfiltered first", status: "", resourceType: "", page: CatalogFirstPage},
	}

	for _, tc := range cases {
		query, err := composeRetirementAdminPage(tc.status, tc.resourceType, tc.page, 4210, 25)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		assertPlaceholdersMatchArgs(t, tc.name, query)
		assertNoRequestDataInSQL(t, tc.name, query.sql, tc.status, "4210", "25")

		if !strings.Contains(query.sql, "page AS MATERIALIZED") {
			t.Errorf("%s: must page ids before hydrating:\n%s", tc.name, query.sql)
		}
		if !strings.HasSuffix(strings.TrimSpace(query.sql), "ORDER BY r.id DESC") {
			t.Errorf("%s: must return descending ids:\n%s", tc.name, query.sql)
		}
		if !strings.Contains(query.sql, "INNER JOIN retirement_requests r ON r.id = page.id") {
			t.Errorf("%s: must hydrate the chosen ids:\n%s", tc.name, query.sql)
		}

		hasStatus := strings.Contains(query.sql, "r.status=")
		if (tc.status != "" && tc.status != "all") != hasStatus {
			t.Errorf("%s: status predicate = %v, want %v:\n%s", tc.name, hasStatus, tc.status != "" && tc.status != "all", query.sql)
		}
		// The projection's CASE also mentions voucher_id IS NOT NULL. Only a
		// WHERE/AND predicate is the resource filter.
		hasVoucher := strings.Contains(query.sql, "WHERE r.voucher_id IS NOT NULL") ||
			strings.Contains(query.sql, "AND r.voucher_id IS NOT NULL")
		hasPool := strings.Contains(query.sql, "WHERE r.pool_id IS NOT NULL") ||
			strings.Contains(query.sql, "AND r.pool_id IS NOT NULL")
		if (tc.resourceType == "voucher") != hasVoucher || (tc.resourceType == "pool") != hasPool {
			t.Errorf("%s: voucher filter = %v, pool filter = %v:\n%s", tc.name, hasVoucher, hasPool, query.sql)
		}

		switch tc.page {
		case CatalogFirstPage:
			if strings.Contains(query.sql, "r.id < $") || strings.Contains(query.sql, "r.id > $") {
				t.Errorf("%s: a first page must carry no cursor predicate:\n%s", tc.name, query.sql)
			}
			if !strings.Contains(query.sql, "ORDER BY r.id DESC") {
				t.Errorf("%s: first page must scan descending:\n%s", tc.name, query.sql)
			}
		case CatalogNextPage:
			if !strings.Contains(query.sql, "r.id < $") {
				t.Errorf("%s: a next page walks below the cursor:\n%s", tc.name, query.sql)
			}
		case CatalogPrevPage:
			if !strings.Contains(query.sql, "r.id > $") {
				t.Errorf("%s: a previous page walks above the cursor:\n%s", tc.name, query.sql)
			}
			if !strings.Contains(query.sql, "ORDER BY r.id ASC") {
				t.Errorf("%s: a previous page scans ascending before the outer sort:\n%s", tc.name, query.sql)
			}
		}
	}
}

func TestComposeRetirementAdminPageRejectsUnknownResource(t *testing.T) {
	if _, err := composeRetirementAdminPage("pending", "token", CatalogFirstPage, 0, 10); err == nil {
		t.Fatal("an unknown resource type must not reach SQL")
	}
}
