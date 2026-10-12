package store

import (
	"strings"
	"testing"
)

func TestTransferValuationQueriesAreRegistered(t *testing.T) {
	queries, err := loadQueries("../../core_queries.sql")
	if err != nil {
		t.Fatalf("load core queries: %v", err)
	}
	for name, query := range map[string]string{
		"insert": queries.InsertTransferValuations,
		"list":   queries.ListTransferValuations,
	} {
		if strings.TrimSpace(query) == "" {
			t.Fatalf("%s transfer valuation query is empty", name)
		}
	}
	if !strings.Contains(queries.InsertTransferValuations, "v.voucher_value > 0") {
		t.Fatal("snapshot insert must reject missing and non-positive valuation rates")
	}
	if !strings.Contains(queries.InsertTransferValuations, "ANY($2::text[])") {
		t.Fatal("snapshot insert must accept every distinct voucher in one relay")
	}
	if !strings.Contains(queries.ListTransferValuations, "o.tracking_id = s.tracking_id") {
		t.Fatal("snapshot lookup must follow tracking id across replacement transactions")
	}
}
