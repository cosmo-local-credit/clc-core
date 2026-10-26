package store

import (
	"strings"
	"testing"
)

func TestOwnedByPredicateBindsTheAccountOnce(t *testing.T) {
	args := &sqlArgs{}
	got := assetOwnerPredicate(args, "v", CatalogFilter{Owner: "0xabc"})
	if len(args.values) != 1 {
		t.Fatalf("bound %d values, want 1", len(args.values))
	}
	for _, want := range []string{"lower(v.owner_address) = lower($1)", "(v.chain_id, lower(v.owner_address)) IN",
		"SELECT ss.chain_id, ss.safe_address FROM safe_signers ss", "ss.signer = lower($1)"} {
		if !strings.Contains(got, want) {
			t.Errorf("predicate %q lacks %q", got, want)
		}
	}
	if assetOwnerPredicate(&sqlArgs{}, "v", CatalogFilter{}) != "" {
		t.Fatal("empty owner must add no predicate")
	}
	// A report's author is an account; Safes never sign in to write one.
	if author := ownerPredicate(&sqlArgs{}, "r.author_address", CatalogFilter{Owner: "0xabc"}); strings.Contains(author, "safe_signers") {
		t.Fatalf("author predicate %q consults Safe signers", author)
	}
}

func TestOwnedQueriesLoadAndMentionSafeSigners(t *testing.T) {
	queries, err := loadQueries("../../core_queries.sql")
	if err != nil {
		t.Fatal(err)
	}
	for name, q := range map[string]string{
		"owned": queries.ListOwnedVouchers, "next": queries.ListOwnedVouchersNext,
		"prev": queries.ListOwnedVouchersPrev, "counts": queries.AccountRelationshipCounts,
	} {
		if !strings.Contains(q, "safe_signers") {
			t.Errorf("%s query ignores Safe signers", name)
		}
	}
}
