package store

import (
	"errors"
	"strings"
	"testing"
)

func TestNewAdminTransactionSearchAppliesTheCatalogTermRules(t *testing.T) {
	for name, tc := range map[string]struct {
		raw     string
		wantErr error
	}{
		"empty is no search":          {raw: "", wantErr: nil},
		"whitespace is no search":     {raw: "  \t ", wantErr: nil},
		"one character is rejected":   {raw: "x", wantErr: ErrCatalogTermTooShort},
		"two characters are accepted": {raw: "0x", wantErr: nil},
		"maximum is accepted":         {raw: strings.Repeat("a", CatalogTermMaxRunes), wantErr: nil},
		"over maximum is rejected":    {raw: strings.Repeat("a", CatalogTermMaxRunes+1), wantErr: ErrCatalogTermTooLong},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewAdminTransactionSearch(tc.raw)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("NewAdminTransactionSearch(%q) error = %v, want %v", tc.raw, err, tc.wantErr)
			}
		})
	}

	search, err := NewAdminTransactionSearch("  0xABCD  ")
	if err != nil {
		t.Fatalf("NewAdminTransactionSearch: %v", err)
	}
	if search.Term != "0xABCD" {
		t.Fatalf("Term = %q, want normalized value", search.Term)
	}
}

func TestAdminTransactionSearchMatchesOnlyTheConfiguredColumns(t *testing.T) {
	search := AdminTransactionSearch{Term: "abc"}
	for name, arms := range map[string][]adminTxArm{
		"voucher": adminVoucherArms,
		"pool":    adminPoolArms,
	} {
		for _, arm := range arms {
			t.Run(name+"/"+arm.kind, func(t *testing.T) {
				args := &sqlArgs{}
				columns := append([]string{"tx.tx_hash"}, arm.searchColumns...)
				predicate := search.predicate(
					columns,
					func(value string) string { return args.bind(value) },
					args.reuse,
				)

				assertNoRequestDataInSQL(t, arm.kind, predicate, search.Term, search.likeTerm())
				if got := strings.Count(predicate, "LIKE"); got != len(columns) {
					t.Fatalf("LIKE predicates = %d, want %d:\n%s", got, len(columns), predicate)
				}
				for _, column := range columns {
					if !strings.Contains(predicate, "lower("+column+")") {
						t.Fatalf("search must include %s:\n%s", column, predicate)
					}
				}
				if len(args.values) != 1 || args.values[0] != search.Term {
					t.Fatalf("search term must be bound once, got %#v", args.values)
				}
			})
		}
	}
}

func TestAdminTxSeqRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name     string
		txID     int64
		logIndex int64
	}{
		{"first event of a transaction", 1, 0},
		{"a later log in the same transaction", 1, 7},
		{"the highest transaction an int column can hold", 2_147_483_647, 0},
		{"the highest log index the packing allows", 2_147_483_647, adminTxSeqScale - 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			txID, logIndex := splitAdminTxSeq(adminTxSeq(tc.txID, tc.logIndex))
			if txID != tc.txID || logIndex != tc.logIndex {
				t.Fatalf("round trip gave (%d, %d), want (%d, %d)", txID, logIndex, tc.txID, tc.logIndex)
			}
		})
	}
}

// The cursor crosses JSON and a browser URL, where it is a float64. A packed
// value above 2^53 would be silently rounded and page the operator to the wrong
// row, so the widest possible one must stay under it.
func TestAdminTxSeqStaysExactInFloat64(t *testing.T) {
	widest := adminTxSeq(2_147_483_647, adminTxSeqScale-1)
	if float64(widest) != float64(widest) || uint64(float64(widest)) != widest {
		t.Fatalf("packed cursor %d does not survive a float64 round trip", widest)
	}
	const maxSafeInteger = uint64(1) << 53
	if widest >= maxSafeInteger {
		t.Fatalf("packed cursor %d exceeds the exact integer range %d", widest, maxSafeInteger)
	}
}

// Chain order is (tx_id, log_index) descending. Packing must preserve it, which
// is the whole reason the pages no longer cursor on a per-table id.
func TestAdminTxSeqOrdersByChainPosition(t *testing.T) {
	earlierLog := adminTxSeq(100, 2)
	laterLog := adminTxSeq(100, 3)
	laterTx := adminTxSeq(101, 0)

	if earlierLog >= laterLog || laterLog >= laterTx {
		t.Fatalf("packing lost chain order: %d, %d, %d", earlierLog, laterLog, laterTx)
	}
}

// A mint and a burn each emit an ERC-20 Transfer log with the zero address on
// one side, so token_transfer holds a mirror of every row in token_mint and
// token_burn. Without this exclusion one movement is two rows on the page, the
// second of them showing no minter.
func TestVoucherTransferArmExcludesMintAndBurnMirrors(t *testing.T) {
	var transfer adminTxArm
	for _, arm := range adminVoucherArms {
		if arm.kind == "transfer" {
			transfer = arm
		}
	}
	if transfer.kind == "" {
		t.Fatal("no transfer arm on the voucher surface")
	}
	if !strings.Contains(transfer.exclude, "ev.sender_address <> '"+adminTxZeroAddress+"'") {
		t.Fatalf("transfer arm does not exclude mint mirrors: %q", transfer.exclude)
	}
	if !strings.Contains(transfer.exclude, "ev.recipient_address <> '"+adminTxZeroAddress+"'") {
		t.Fatalf("transfer arm does not exclude burn mirrors: %q", transfer.exclude)
	}

	// The other two arms read their own tables and must not filter on an
	// address they do not have.
	for _, arm := range adminVoucherArms {
		if arm.kind != "transfer" && arm.exclude != "" {
			t.Fatalf("%s arm should not exclude rows, got %q", arm.kind, arm.exclude)
		}
	}
}

// Every arm of a surface is merged into one page, so all of them must project
// the same column set — a missing column is a scan error at request time rather
// than a compile error.
func TestAdminTxArmsShareOneProjection(t *testing.T) {
	for name, surface := range map[string][]adminTxArm{
		"voucher": adminVoucherArms,
		"pool":    adminPoolArms,
	} {
		want := aliasesOf(surface[0].columns)
		for _, arm := range surface[1:] {
			got := aliasesOf(arm.columns)
			if len(got) != len(want) {
				t.Fatalf("%s surface: %s arm projects %v, want %v", name, arm.kind, got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("%s surface: %s arm projects %v, want %v", name, arm.kind, got, want)
				}
			}
		}
	}
}

func aliasesOf(columns string) []string {
	aliases := make([]string, 0, 8)
	for column := range strings.SplitSeq(columns, ", ") {
		parts := strings.Split(column, " AS ")
		aliases = append(aliases, parts[len(parts)-1])
	}
	return aliases
}

func TestAdminOTXQueriesLoad(t *testing.T) {
	queries, err := loadQueries("../../core_queries.sql")
	if err != nil {
		t.Fatalf("load queries: %v", err)
	}
	for name, query := range map[string]string{
		"list-otx":      queries.ListOTX,
		"list-otx-next": queries.ListOTXNext,
		"list-otx-prev": queries.ListOTXPrev,
	} {
		if query == "" {
			t.Fatalf("expected %s to be loaded", name)
		}
		// raw_tx is the largest column on the row and nothing on the page reads
		// it; a hundred signed transactions per request is the payload this
		// statement exists to avoid.
		if strings.Contains(query, "raw_tx") {
			t.Fatalf("%s selects raw_tx", name)
		}
	}
}

func TestComposeAdminOTXSearchPageFiltersAndPagesAtTheBackend(t *testing.T) {
	search := AdminTransactionSearch{Term: "track%_id"}
	for name, page := range map[string]CatalogPage{
		"first": CatalogFirstPage,
		"next":  CatalogNextPage,
		"prev":  CatalogPrevPage,
	} {
		t.Run(name, func(t *testing.T) {
			query := composeAdminOTXSearchPage("RELAY_EXEC", "SUCCESS", search, page, 42, 25)
			assertPlaceholdersMatchArgs(t, name, query)
			assertNoRequestDataInSQL(t, name, query.sql, search.Term, search.likeTerm(), "RELAY_EXEC", "SUCCESS")

			if !strings.Contains(query.sql, "matches AS MATERIALIZED") {
				t.Fatalf("search must materialize its match set before paging:\n%s", query.sql)
			}
			for _, column := range []string{"lower(otx.tracking_id::text)", "lower(otx.tx_hash)"} {
				if !strings.Contains(query.sql, column) {
					t.Fatalf("search must include %s:\n%s", column, query.sql)
				}
			}
			if !strings.Contains(query.sql, `LIKE '%' || lower(`) || !strings.Contains(query.sql, `ESCAPE '\'`) {
				t.Fatalf("substring search must use the escaped LIKE predicate:\n%s", query.sql)
			}
			for _, predicate := range []string{"otx.otx_type = $", "d.status = $"} {
				if !strings.Contains(query.sql, predicate) {
					t.Fatalf("typed reducer %q missing:\n%s", predicate, query.sql)
				}
			}
			if !strings.HasSuffix(strings.TrimSpace(query.sql), "ORDER BY otx.id DESC;") {
				t.Fatalf("response must stay newest-first:\n%s", query.sql)
			}
		})
	}
}
