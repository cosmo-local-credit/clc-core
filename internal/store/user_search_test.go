package store

import (
	"errors"
	"strings"
	"testing"
)

func TestNewUserFilterAppliesTheCatalogTermRules(t *testing.T) {
	// The directory search shares the catalog's term contract rather than
	// inventing a second one, so an operator and a shopper get the same minimum,
	// the same maximum and the same error text. These cases mirror
	// TestNewCatalogFilterEnforcesTermLength; if the two ever disagree, one of
	// them has grown its own copy of the rule.
	for name, tc := range map[string]struct {
		raw     string
		wantErr error
	}{
		"empty is no search":         {raw: "", wantErr: nil},
		"whitespace is no search":    {raw: "   \t ", wantErr: nil},
		"one character rejected":     {raw: "s", wantErr: ErrCatalogTermTooShort},
		"padded one char rejected":   {raw: "   s   ", wantErr: ErrCatalogTermTooShort},
		"one multibyte rejected":     {raw: "é", wantErr: ErrCatalogTermTooShort},
		"two characters accepted":    {raw: "sa", wantErr: nil},
		"at the maximum accepted":    {raw: strings.Repeat("a", CatalogTermMaxRunes), wantErr: nil},
		"over the maximum rejected":  {raw: strings.Repeat("a", CatalogTermMaxRunes+1), wantErr: ErrCatalogTermTooLong},
		"multibyte counted as runes": {raw: strings.Repeat("é", CatalogTermMaxRunes), wantErr: nil},
	} {
		_, err := NewUserFilter(tc.raw, "")
		if !errors.Is(err, tc.wantErr) {
			t.Errorf("%s: NewUserFilter(%q) error = %v, want %v", name, tc.raw, err, tc.wantErr)
		}
	}
}

func TestNewUserFilterNormalizesTheTerm(t *testing.T) {
	filter, err := NewUserFilter("  Ada    Lovelace ", "")
	if err != nil {
		t.Fatalf("NewUserFilter: %v", err)
	}
	if filter.Term != "Ada Lovelace" {
		t.Errorf("Term = %q, want %q", filter.Term, "Ada Lovelace")
	}
}

func TestParseUserStatusAcceptsOnlyTheEnum(t *testing.T) {
	for name, tc := range map[string]struct {
		raw     string
		want    UserStatus
		wantErr error
	}{
		"empty is every status":  {raw: "", want: "", wantErr: nil},
		"whitespace is every":    {raw: "  ", want: "", wantErr: nil},
		"pending":                {raw: "PENDING", want: UserStatusPending, wantErr: nil},
		"approved":               {raw: "APPROVED", want: UserStatusApproved, wantErr: nil},
		"rejected":               {raw: "REJECTED", want: UserStatusRejected, wantErr: nil},
		"lowercase folded":       {raw: "pending", want: UserStatusPending, wantErr: nil},
		"padded and folded":      {raw: " Approved ", want: UserStatusApproved, wantErr: nil},
		"unknown rejected":       {raw: "ARCHIVED", want: "", wantErr: ErrUnknownUserStatus},
		"near miss rejected":     {raw: "PENDING ", want: UserStatusPending, wantErr: nil},
		"sql fragment rejected":  {raw: "PENDING' OR '1'='1", want: "", wantErr: ErrUnknownUserStatus},
		"enum of another column": {raw: "USER", want: "", wantErr: ErrUnknownUserStatus},
	} {
		got, err := ParseUserStatus(tc.raw)
		if !errors.Is(err, tc.wantErr) {
			t.Errorf("%s: ParseUserStatus(%q) error = %v, want %v", name, tc.raw, err, tc.wantErr)
		}
		if got != tc.want {
			t.Errorf("%s: ParseUserStatus(%q) = %q, want %q", name, tc.raw, got, tc.want)
		}
	}
}

// userCompositionCases is the closed set of allowed directory compositions:
// search present or not, status present or not, across all three page
// directions.
func userCompositionCases() map[string]struct {
	filter UserFilter
	page   CatalogPage
} {
	cases := map[string]struct {
		filter UserFilter
		page   CatalogPage
	}{}

	// Terms are chosen not to occur in the statement text itself: the composed SQL
	// names columns like blockchain_address, so a fixture term of "ad" would fail
	// the no-request-data assertion on the column name rather than on a leak.
	for filterName, filter := range map[string]UserFilter{
		"no filter":         {},
		"status only":       {Status: UserStatusPending},
		"prefix term":       {Term: "ka"},
		"substring term":    {Term: "ada"},
		"term and status":   {Term: "ada", Status: UserStatusApproved},
		"metacharacters":    {Term: `100%_x\y`},
		"address term":      {Term: "0x33a573149db22e759fB9A38Bcc461C12855c2645"},
		"multibyte term":    {Term: "chamá"},
		"term with a space": {Term: "ada lovelace"},
	} {
		for pageName, page := range map[string]CatalogPage{
			"first": CatalogFirstPage,
			"next":  CatalogNextPage,
			"prev":  CatalogPrevPage,
		} {
			cases[filterName+"/"+pageName] = struct {
				filter UserFilter
				page   CatalogPage
			}{filter: filter, page: page}
		}
	}
	return cases
}

func TestComposeUserPageEveryComposition(t *testing.T) {
	for name, tc := range userCompositionCases() {
		query := composeUserPage(tc.filter, tc.page, 4210, 25)

		assertPlaceholdersMatchArgs(t, name, query)
		// The whole point of composing rather than concatenating: no request value
		// reaches the statement text, only placeholders that refer to it.
		assertNoRequestDataInSQL(t, name, query.sql,
			tc.filter.Term, tc.filter.likeTerm(), string(tc.filter.Status))

		// Whatever the inner direction, the caller always receives descending IDs.
		if !strings.HasSuffix(strings.TrimSpace(query.sql), "ORDER BY u.id DESC;") {
			t.Errorf("%s: must return descending IDs:\n%s", name, query.sql)
		}
		// The page is selected before the row is hydrated.
		if !strings.Contains(query.sql, "page AS MATERIALIZED") {
			t.Errorf("%s: must page candidates before hydrating:\n%s", name, query.sql)
		}

		// Search is a driving-shape input: with a term the match set is
		// materialized before ordering, without one the page is taken directly.
		hasMatchSet := strings.Contains(query.sql, "matches AS MATERIALIZED")
		if (tc.filter.Term != "") != hasMatchSet {
			t.Errorf("%s: match set materialized = %v, want %v:\n%s",
				name, hasMatchSet, tc.filter.Term != "", query.sql)
		}

		// The status reducer is emitted or omitted, and always crosses as a bound
		// parameter cast to the enum.
		hasStatus := strings.Contains(query.sql, "u.status = $")
		if (tc.filter.Status != "") != hasStatus {
			t.Errorf("%s: status predicate present = %v, want %v:\n%s",
				name, hasStatus, tc.filter.Status != "", query.sql)
		}
		if hasStatus && !strings.Contains(query.sql, "::user_status") {
			t.Errorf("%s: a status must be cast to the enum:\n%s", name, query.sql)
		}

		// The cursor comparison comes from the typed direction, never a request
		// string, and a first page has none.
		switch tc.page {
		case CatalogFirstPage:
			if strings.Contains(query.sql, ".id < $") || strings.Contains(query.sql, ".id > $") {
				t.Errorf("%s: a first page must carry no cursor predicate:\n%s", name, query.sql)
			}
			if !strings.Contains(query.sql, "ORDER BY u.id DESC") {
				t.Errorf("%s: first page must scan descending:\n%s", name, query.sql)
			}
		case CatalogNextPage:
			if !strings.Contains(query.sql, ".id < $") {
				t.Errorf("%s: a next page walks below the cursor:\n%s", name, query.sql)
			}
		case CatalogPrevPage:
			if !strings.Contains(query.sql, ".id > $") {
				t.Errorf("%s: a previous page walks above the cursor:\n%s", name, query.sql)
			}
			if !strings.Contains(query.sql, "ASC") {
				t.Errorf("%s: a previous page scans ascending before the outer sort:\n%s", name, query.sql)
			}
		}

		if tc.filter.Term != "" {
			// Prefix and substring are distinct constant fragments.
			wantSubstring := !tc.filter.prefixOnly()
			gotSubstring := strings.Contains(query.sql, `LIKE '%' || lower(`)
			if wantSubstring != gotSubstring {
				t.Errorf("%s: substring fragment = %v, want %v:\n%s",
					name, gotSubstring, wantSubstring, query.sql)
			}
			if !strings.Contains(query.sql, `ESCAPE '\'`) {
				t.Errorf("%s: every LIKE must declare its escape character:\n%s", name, query.sql)
			}
			// All four identity and contact columns are searched, and nothing else.
			for _, column := range []string{
				"lower(u.name)", "lower(u.email)", "lower(u.phone)", "lower(u.blockchain_address)",
			} {
				if !strings.Contains(query.sql, column) {
					t.Errorf("%s: search must match %s:\n%s", name, column, query.sql)
				}
			}
			if got := strings.Count(query.sql, "LIKE"); got != 4 {
				t.Errorf("%s: want one LIKE per searched column, got %d:\n%s", name, got, query.sql)
			}
			for _, forbidden := range []string{"lower(u.country)", "lower(u.role)"} {
				if strings.Contains(query.sql, forbidden) {
					t.Errorf("%s: %s is a classification, not search text:\n%s", name, forbidden, query.sql)
				}
			}
		}

		// The projection is the shared account shape, whatever narrowed it.
		for _, column := range []string{
			"blockchain_address AS ethereum_address",
			`COALESCE(u.email, '') AS email`,
			"u.max_pools",
			"u.created_at",
		} {
			if !strings.Contains(query.sql, column) {
				t.Errorf("%s: projection must include %s:\n%s", name, column, query.sql)
			}
		}
	}
}

func TestComposeUserPageBindsOneTermForEveryColumn(t *testing.T) {
	// Four columns match one term. Binding it per column would make the argument
	// list vary with the column count, and every extra copy is another value the
	// statement cache has to carry.
	query := composeUserPage(UserFilter{Term: "ada"}, CatalogFirstPage, 0, 25)

	terms := 0
	for _, arg := range query.args {
		if text, ok := arg.(string); ok && text == "ada" {
			terms++
		}
	}
	if terms != 1 {
		t.Errorf("term bound %d times, want once: %#v", terms, query.args)
	}
	if got := strings.Count(query.sql, "$1"); got != 4 {
		t.Errorf("term placeholder referenced %d times, want 4:\n%s", got, query.sql)
	}
}

func TestComposeUserPageEscapesLikeMetacharacters(t *testing.T) {
	// A term containing % or _ must match those literally rather than widening
	// the pattern to everything.
	query := composeUserPage(UserFilter{Term: `100%_x`}, CatalogFirstPage, 0, 25)

	found := false
	for _, arg := range query.args {
		if text, ok := arg.(string); ok && text == `100\%\_x` {
			found = true
		}
	}
	if !found {
		t.Errorf("want the escaped term bound, got %#v", query.args)
	}
}
