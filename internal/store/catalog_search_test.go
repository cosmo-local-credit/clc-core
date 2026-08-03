package store

import (
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestNormalizeCatalogTermCollapsesWhitespace(t *testing.T) {
	for name, tc := range map[string]struct {
		raw  string
		want string
	}{
		"already normal":     {raw: "sarafu network", want: "sarafu network"},
		"leading trailing":   {raw: "  sarafu  ", want: "sarafu"},
		"internal runs":      {raw: "sarafu    network", want: "sarafu network"},
		"tabs and newlines":  {raw: "sarafu\t\nnetwork", want: "sarafu network"},
		"only whitespace":    {raw: "   \t ", want: ""},
		"empty":              {raw: "", want: ""},
		"multibyte survives": {raw: "  chamá  kikundi ", want: "chamá kikundi"},
	} {
		if got := NormalizeCatalogTerm(tc.raw); got != tc.want {
			t.Errorf("%s: NormalizeCatalogTerm(%q) = %q, want %q", name, tc.raw, got, tc.want)
		}
	}
}

func TestNewCatalogFilterEnforcesTermLength(t *testing.T) {
	// Length is measured in runes and checked after normalization, so padding
	// cannot smuggle a one-character search past the minimum.
	for name, tc := range map[string]struct {
		raw     string
		wantErr error
	}{
		"empty is no search":         {raw: "", wantErr: nil},
		"whitespace is no search":    {raw: "     ", wantErr: nil},
		"one character rejected":     {raw: "s", wantErr: ErrCatalogTermTooShort},
		"padded one char rejected":   {raw: "   s   ", wantErr: ErrCatalogTermTooShort},
		"one multibyte rejected":     {raw: "é", wantErr: ErrCatalogTermTooShort},
		"two characters accepted":    {raw: "sa", wantErr: nil},
		"three characters accepted":  {raw: "sar", wantErr: nil},
		"at the maximum accepted":    {raw: strings.Repeat("a", CatalogTermMaxRunes), wantErr: nil},
		"over the maximum rejected":  {raw: strings.Repeat("a", CatalogTermMaxRunes+1), wantErr: ErrCatalogTermTooLong},
		"multibyte counted as runes": {raw: strings.Repeat("é", CatalogTermMaxRunes), wantErr: nil},
	} {
		_, err := NewCatalogFilter(tc.raw, nil, TagMatchAny)
		if !errors.Is(err, tc.wantErr) {
			t.Errorf("%s: NewCatalogFilter(%q) error = %v, want %v", name, tc.raw, err, tc.wantErr)
		}
	}

	// A multibyte term at the maximum rune count is well over the byte limit, so
	// a byte-based check would have rejected it.
	long := strings.Repeat("é", CatalogTermMaxRunes)
	if len(long) <= CatalogTermMaxRunes {
		t.Fatalf("fixture is not multibyte enough to distinguish runes from bytes: %d bytes", len(long))
	}
}

func TestNewCatalogFilterNormalizesTags(t *testing.T) {
	// Deduplication must happen before the query runs: the all-tags form
	// compares a match count against cardinality($1), so a duplicated value
	// would raise the threshold and silently drop matching rows.
	filter, err := NewCatalogFilter("", []string{"Culture", " Art ", "Culture", "", "   ", "Art"}, TagMatchAny)
	if err != nil {
		t.Fatalf("NewCatalogFilter() error = %v", err)
	}

	want := []string{"Art", "Culture"}
	if !slices.Equal(filter.Tags, want) {
		t.Errorf("Tags = %v, want %v (deduplicated, trimmed, sorted)", filter.Tags, want)
	}

	// Sorting makes two equivalent requests produce identical parameters.
	reordered, err := NewCatalogFilter("", []string{"Culture", "Art"}, TagMatchAny)
	if err != nil {
		t.Fatalf("NewCatalogFilter() error = %v", err)
	}
	if !slices.Equal(filter.Tags, reordered.Tags) {
		t.Errorf("tag order changed the parameters: %v vs %v", filter.Tags, reordered.Tags)
	}
}

func TestNewCatalogFilterRejectsTooManyTags(t *testing.T) {
	atLimit := make([]string, 0, CatalogMaxTags)
	for i := range CatalogMaxTags {
		atLimit = append(atLimit, "tag"+strconv.Itoa(i))
	}
	if _, err := NewCatalogFilter("", atLimit, TagMatchAny); err != nil {
		t.Errorf("%d tags rejected: %v", CatalogMaxTags, err)
	}

	// Duplicates collapse before the count is checked, so a caller repeating one
	// value cannot trip the limit.
	overWithDuplicates := append(slices.Clone(atLimit), atLimit...)
	if _, err := NewCatalogFilter("", overWithDuplicates, TagMatchAny); err != nil {
		t.Errorf("duplicates must collapse before the tag limit applies: %v", err)
	}

	over := append(slices.Clone(atLimit), "one-too-many")
	if _, err := NewCatalogFilter("", over, TagMatchAny); !errors.Is(err, ErrCatalogTooManyTags) {
		t.Errorf("error = %v, want ErrCatalogTooManyTags", err)
	}
}

func TestNewCatalogFilterRejectsUnknownTagMatch(t *testing.T) {
	if _, err := NewCatalogFilter("", nil, TagMatch(99)); !errors.Is(err, ErrCatalogUnknownTagMatch) {
		t.Errorf("error = %v, want ErrCatalogUnknownTagMatch", err)
	}
}

func TestCatalogFilterActive(t *testing.T) {
	for name, tc := range map[string]struct {
		filter CatalogFilter
		want   bool
	}{
		"empty":         {filter: CatalogFilter{}, want: false},
		"term only":     {filter: CatalogFilter{Term: "sar"}, want: true},
		"tags only":     {filter: CatalogFilter{Tags: []string{"Art"}}, want: true},
		"vouchers only": {filter: CatalogFilter{VoucherIDs: []uint64{42}}, want: true},
		"term and tags": {filter: CatalogFilter{Term: "sar", Tags: []string{"Art"}}, want: true},
	} {
		if got := tc.filter.Active(); got != tc.want {
			t.Errorf("%s: Active() = %v, want %v", name, got, tc.want)
		}
	}
}

func TestCatalogFilterWithVoucherIDsNormalizesAndBounds(t *testing.T) {
	filter, err := (CatalogFilter{}).WithVoucherIDs([]uint64{9, 2, 9, 4}, TagMatchAll)
	if err != nil {
		t.Fatalf("WithVoucherIDs() error = %v", err)
	}
	if !slices.Equal(filter.VoucherIDs, []uint64{2, 4, 9}) {
		t.Errorf("VoucherIDs = %v, want sorted unique IDs", filter.VoucherIDs)
	}
	if filter.VoucherMatch != TagMatchAll {
		t.Errorf("VoucherMatch = %v, want all", filter.VoucherMatch)
	}

	over := make([]uint64, CatalogMaxVouchers+1)
	for index := range over {
		over[index] = uint64(index + 1)
	}
	if _, err := (CatalogFilter{}).WithVoucherIDs(over, TagMatchAny); !errors.Is(err, ErrCatalogTooManyVouchers) {
		t.Errorf("over-limit error = %v, want ErrCatalogTooManyVouchers", err)
	}
	if _, err := (CatalogFilter{}).WithVoucherIDs(nil, TagMatch(99)); !errors.Is(err, ErrCatalogUnknownVoucherMatch) {
		t.Errorf("unknown match error = %v, want ErrCatalogUnknownVoucherMatch", err)
	}
}

// A caller searching for a literal "%" or "_" must get that literal. Without
// escaping, "%" would match everything and "_" would match any character,
// widening the query beyond what was asked.
func TestCatalogFilterEscapesLikeMetacharacters(t *testing.T) {
	for name, tc := range map[string]struct {
		term string
		want string
	}{
		"no metacharacters":  {term: "sarafu", want: "sarafu"},
		"percent":            {term: "50%", want: `50\%`},
		"underscore":         {term: "a_b", want: `a\_b`},
		"backslash":          {term: `a\b`, want: `a\\b`},
		"all three":          {term: `a\%_`, want: `a\\\%\_`},
		"only metachars":     {term: "%%", want: `\%\%`},
		"backslash then pct": {term: `\%`, want: `\\\%`},
	} {
		got := CatalogFilter{Term: tc.term}.likeTerm()
		if got != tc.want {
			t.Errorf("%s: likeTerm(%q) = %q, want %q", name, tc.term, got, tc.want)
		}
	}

	// The backslash must be doubled first. Escaping "%" before "\" would turn
	// `\` + `%` into `\` + `\%` and then double the original backslash into
	// `\\\%`... but escaping in the wrong order yields `\\%`, where the
	// caller's backslash escapes the metacharacter marker instead of itself.
	if got := (CatalogFilter{Term: `\`}).likeTerm(); got != `\\` {
		t.Errorf("a lone backslash must be doubled, got %q", got)
	}
}

// Two characters is the boundary between prefix and substring matching:
// PostgreSQL cannot extract a full trigram from a two-character pattern, so a
// substring match at that length degrades to a full index scan.
func TestCatalogFilterPrefixBoundary(t *testing.T) {
	for name, tc := range map[string]struct {
		term string
		want bool
	}{
		"two ascii":     {term: "sa", want: true},
		"two multibyte": {term: "éé", want: true},
		"three ascii":   {term: "sar", want: false},
		"long":          {term: "sarafu network", want: false},
	} {
		if got := (CatalogFilter{Term: tc.term}).prefixOnly(); got != tc.want {
			t.Errorf("%s: prefixOnly(%q) = %v, want %v", name, tc.term, got, tc.want)
		}
	}
}

var (
	placeholderPattern = regexp.MustCompile(`\$(\d+)`)
)

// cteBody returns the text of one named MATERIALIZED CTE, so a test can assert on
// which predicates that stage carries rather than on the whole statement.
func cteBody(t *testing.T, sql, name string) string {
	t.Helper()

	opener := name + " AS MATERIALIZED ("
	start := strings.Index(sql, opener)
	if start < 0 {
		t.Fatalf("no %q CTE in:\n%s", name, sql)
	}
	body, _, closed := strings.Cut(sql[start+len(opener):], "\n)")
	if !closed {
		t.Fatalf("unterminated %q CTE in:\n%s", name, sql)
	}
	return body
}

// assertPlaceholdersMatchArgs proves the composer's placeholder numbering and
// its argument slice cannot drift apart: every $N in the SQL must have an
// argument, and every argument must be referenced.
func assertPlaceholdersMatchArgs(t *testing.T, label string, query composed) {
	t.Helper()

	referenced := map[int]bool{}
	for _, match := range placeholderPattern.FindAllStringSubmatch(query.sql, -1) {
		n, err := strconv.Atoi(match[1])
		if err != nil {
			t.Fatalf("%s: unparseable placeholder %q", label, match[0])
		}
		referenced[n] = true
	}

	for n := 1; n <= len(query.args); n++ {
		if !referenced[n] {
			t.Errorf("%s: argument %d is bound but never referenced in SQL:\n%s", label, n, query.sql)
		}
	}
	for n := range referenced {
		if n < 1 || n > len(query.args) {
			t.Errorf("%s: SQL references $%d but only %d arguments are bound:\n%s",
				label, n, len(query.args), query.sql)
		}
	}
}

// assertNoRequestDataInSQL proves the composed text carries no caller-supplied
// value. Query values must always be bound parameters.
func assertNoRequestDataInSQL(t *testing.T, label, sql string, values ...string) {
	t.Helper()
	for _, value := range values {
		if value != "" && strings.Contains(sql, value) {
			t.Errorf("%s: composed SQL contains the request value %q:\n%s", label, value, sql)
		}
	}
}

// catalogCompositionCases is the closed set of allowed voucher compositions:
// viewport present or not, search present or not, tags absent / any / all,
// across all three page directions.
func catalogCompositionCases(t *testing.T) map[string]struct {
	bounds *MarketVoucherBounds
	filter CatalogFilter
	page   CatalogPage
} {
	t.Helper()

	viewport := &MarketVoucherBounds{MinLng: 33.9, MinLat: -4.7, MaxLng: 41.9, MaxLat: 5.1}
	cases := map[string]struct {
		bounds *MarketVoucherBounds
		filter CatalogFilter
		page   CatalogPage
	}{}

	for boundsName, bounds := range map[string]*MarketVoucherBounds{
		"unbounded": nil,
		"bounded":   viewport,
	} {
		for filterName, filter := range map[string]CatalogFilter{
			"no filter":          {},
			"substring":          {Term: "sarafu"},
			"prefix":             {Term: "sa"},
			"tags any":           {Tags: []string{"Art", "Culture"}, TagMatch: TagMatchAny},
			"tags all":           {Tags: []string{"Art", "Culture"}, TagMatch: TagMatchAll},
			"substring tags any": {Term: "sarafu", Tags: []string{"Art"}, TagMatch: TagMatchAny},
			"substring tags all": {Term: "sarafu", Tags: []string{"Art"}, TagMatch: TagMatchAll},
			"prefix tags all":    {Term: "sa", Tags: []string{"Art"}, TagMatch: TagMatchAll},
		} {
			for pageName, page := range map[string]CatalogPage{
				"first": CatalogFirstPage,
				"next":  CatalogNextPage,
				"prev":  CatalogPrevPage,
			} {
				cases[boundsName+"/"+filterName+"/"+pageName] = struct {
					bounds *MarketVoucherBounds
					filter CatalogFilter
					page   CatalogPage
				}{bounds: bounds, filter: filter, page: page}
			}
		}
	}
	return cases
}

func TestComposeMarketVoucherPageEveryComposition(t *testing.T) {
	for name, tc := range catalogCompositionCases(t) {
		query := composeMarketVoucherPage(tc.bounds, tc.filter, tc.page, 2212, 100)

		assertPlaceholdersMatchArgs(t, name, query)
		assertNoRequestDataInSQL(t, name, query.sql, tc.filter.Term, tc.filter.likeTerm())
		for _, tag := range tc.filter.Tags {
			assertNoRequestDataInSQL(t, name, query.sql, tag)
		}

		// Visibility is never optional.
		if !strings.Contains(query.sql, "v.active") || !strings.Contains(query.sql, "v.geo_lat IS NOT NULL") {
			t.Errorf("%s: must keep the catalog's visibility predicates:\n%s", name, query.sql)
		}
		// The page is selected before the per-row kind lookup runs.
		if !strings.Contains(query.sql, "page AS MATERIALIZED") {
			t.Errorf("%s: must page candidates before hydrating:\n%s", name, query.sql)
		}
		// Whatever the inner direction, the caller always receives descending IDs.
		if !strings.HasSuffix(strings.TrimSpace(query.sql), "ORDER BY v.id DESC;") {
			t.Errorf("%s: must return descending IDs:\n%s", name, query.sql)
		}
		// List queries never read report bodies or offering descriptions.
		for _, forbidden := range []string{"reports", "r.body", "o.description", "v.terms", "banner_url"} {
			if strings.Contains(query.sql, forbidden) {
				t.Errorf("%s: list query must not read %s:\n%s", name, forbidden, query.sql)
			}
		}

		// Search is a driving-shape input: with a term the match set is
		// materialized before ordering, without one it is not.
		hasMatchSet := strings.Contains(query.sql, "matches AS MATERIALIZED")
		if tc.filter.Term != "" && !hasMatchSet {
			t.Errorf("%s: a search must materialize its match set before ordering:\n%s", name, query.sql)
		}
		if tc.filter.Term == "" && hasMatchSet {
			t.Errorf("%s: without a search the page must be taken directly:\n%s", name, query.sql)
		}

		// Prefix and substring are distinct fragments.
		if tc.filter.Term != "" {
			wantSubstring := !tc.filter.prefixOnly()
			gotSubstring := strings.Contains(query.sql, `LIKE '%' || lower(`)
			if wantSubstring != gotSubstring {
				t.Errorf("%s: substring fragment = %v, want %v:\n%s", name, gotSubstring, wantSubstring, query.sql)
			}
			if !strings.Contains(query.sql, `ESCAPE '\'`) {
				t.Errorf("%s: every LIKE must declare its escape character:\n%s", name, query.sql)
			}
		}

		// The viewport predicate is emitted or omitted, never hidden behind a
		// nullable OR that would cost the GiST index.
		hasBox := strings.Contains(query.sql, "point(v.geo_lng, v.geo_lat) <@ box(")
		if (tc.bounds != nil) != hasBox {
			t.Errorf("%s: viewport predicate present = %v, want %v:\n%s", name, hasBox, tc.bounds != nil, query.sql)
		}
		if strings.Contains(query.sql, "IS NULL OR") {
			t.Errorf("%s: driving predicates must not hide behind a nullable OR:\n%s", name, query.sql)
		}

		// Tag mode selects between two constant fragments.
		if len(tc.filter.Tags) > 0 {
			wantAll := tc.filter.TagMatch == TagMatchAll
			gotAll := strings.Contains(query.sql, "COUNT(DISTINCT ot.tag_value)")
			if wantAll != gotAll {
				t.Errorf("%s: all-tags fragment = %v, want %v:\n%s", name, gotAll, wantAll, query.sql)
			}
			// Derived tags fan out: one voucher with three offerings carrying
			// the same tag is one match, so the all-tags count must be DISTINCT.
			if wantAll && !strings.Contains(query.sql, "COUNT(DISTINCT ot.tag_value)") {
				t.Errorf("%s: derived all-tags count must be DISTINCT:\n%s", name, query.sql)
			}
			if !strings.Contains(query.sql, "o.available") {
				t.Errorf("%s: derived tags must only read available offerings:\n%s", name, query.sql)
			}
		}

		// The cursor is applied for exactly the two cursored directions.
		hasCursor := strings.Contains(query.sql, ".id < $") || strings.Contains(query.sql, ".id > $")
		if (tc.page != CatalogFirstPage) != hasCursor {
			t.Errorf("%s: cursor predicate present = %v, want %v:\n%s",
				name, hasCursor, tc.page != CatalogFirstPage, query.sql)
		}
		switch tc.page {
		case CatalogPrevPage:
			if !strings.Contains(query.sql, ".id > $") || !strings.Contains(query.sql, "ORDER BY m.id ASC") &&
				!strings.Contains(query.sql, "ORDER BY v.id ASC") {
				t.Errorf("%s: a previous page scans ascending above the cursor:\n%s", name, query.sql)
			}
		case CatalogNextPage:
			if !strings.Contains(query.sql, ".id < $") {
				t.Errorf("%s: a next page scans below the cursor:\n%s", name, query.sql)
			}
		case CatalogFirstPage:
		}
	}
}

// Statement text must not vary with how many tags were selected, or the
// statement cache would hold one entry per tag count.
func TestComposedQueryTextIsIndependentOfTagCount(t *testing.T) {
	one, err := NewCatalogFilter("sarafu", []string{"Art"}, TagMatchAny)
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	many, err := NewCatalogFilter("sarafu", []string{"Art", "Culture", "Festivals", "Mobility", "Retreat"}, TagMatchAny)
	if err != nil {
		t.Fatalf("filter: %v", err)
	}

	for name, pair := range map[string][2]composed{
		"voucher page": {
			composeMarketVoucherPage(nil, one, CatalogNextPage, 10, 50),
			composeMarketVoucherPage(nil, many, CatalogNextPage, 10, 50),
		},
		"voucher facet counts": {
			composeMarketVoucherTagCounts(nil, one),
			composeMarketVoucherTagCounts(nil, many),
		},
		"pool page": {
			composePoolPage(one, CatalogNextPage, 10, 50),
			composePoolPage(many, CatalogNextPage, 10, 50),
		},
		"pool facet counts": {
			composePoolTagCounts(one),
			composePoolTagCounts(many),
		},
	} {
		if pair[0].sql != pair[1].sql {
			t.Errorf("%s: statement text varies with tag count:\n--- one ---\n%s\n--- many ---\n%s",
				name, pair[0].sql, pair[1].sql)
		}
		if len(pair[0].args) != len(pair[1].args) {
			t.Errorf("%s: argument count varies with tag count: %d vs %d",
				name, len(pair[0].args), len(pair[1].args))
		}
	}
}

// The composer must produce a bounded set of distinct statement texts, or the
// prepared-statement cache grows without limit.
func TestComposedVoucherStatementSetIsBounded(t *testing.T) {
	texts := map[string]struct{}{}
	for _, tc := range catalogCompositionCases(t) {
		query := composeMarketVoucherPage(tc.bounds, tc.filter, tc.page, 2212, 100)
		texts[query.sql] = struct{}{}
	}

	// 2 viewports x 3 page directions x (no-search + 2 tag modes, and
	// prefix/substring search x 3 tag states) = 48 shapes at most. The bound is
	// what matters: it must be small, fixed, and independent of request data.
	const maxShapes = 48
	if len(texts) > maxShapes {
		t.Errorf("composer produced %d distinct statements, want at most %d", len(texts), maxShapes)
	}
	if len(texts) < 2 {
		t.Errorf("composer collapsed every composition into %d statement(s)", len(texts))
	}
}

func TestComposeMarketVoucherTagCountsAreDisjunctive(t *testing.T) {
	filter, err := NewCatalogFilter("sarafu", []string{"Art"}, TagMatchAny)
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	query := composeMarketVoucherTagCounts(nil, filter)

	assertPlaceholdersMatchArgs(t, "voucher facet counts", query)
	assertNoRequestDataInSQL(t, "voucher facet counts", query.sql, filter.Term, "Art")

	// The candidate set carries the search but not the tag filter, so selecting
	// one tag does not zero every other tag's count.
	candidates := cteBody(t, query.sql, "candidates")
	if !strings.Contains(candidates, "LIKE") {
		t.Errorf("count candidates must apply the search term:\n%s", candidates)
	}
	if strings.Contains(candidates, "offering_tags") {
		t.Errorf("count candidates must omit the tag filter for disjunctive counts:\n%s", candidates)
	}
	if !strings.Contains(candidates, "v.active") || !strings.Contains(candidates, "v.geo_lat IS NOT NULL") {
		t.Errorf("counts must use the same visibility rule as the result query:\n%s", candidates)
	}

	// Driving from the candidate set outwards is what keeps the trigram index
	// driving the search; starting at the junction collapsed it into a filter.
	if !strings.Contains(query.sql, "FROM candidates c\nINNER JOIN offerings o ON o.voucher_id = c.id AND o.available") {
		t.Errorf("counts must drive from candidates outwards, not from the tag junction inwards:\n%s", query.sql)
	}
	// Derived joins fan out; the count must be per voucher.
	if !strings.Contains(query.sql, "COUNT(DISTINCT c.id)") {
		t.Errorf("derived counts must be DISTINCT per voucher:\n%s", query.sql)
	}
	// The vocabulary is a separate, parameterless statement.
	if strings.Contains(query.sql, "facet_vocabulary") || strings.Contains(query.sql, "token_tag") {
		t.Errorf("the vocabulary must not be recomputed inside the count query:\n%s", query.sql)
	}
}

// The two facet rules that are not the database's business: a selected value
// survives with a zero count, and every vocabulary value is represented.
func TestMergeTagFacets(t *testing.T) {
	vocabulary := []string{"Culture", "Art", "Mobility"}
	counts := map[string]uint64{"Art": 7, "Culture": 2}

	t.Run("returns every vocabulary value in tag order", func(t *testing.T) {
		got := mergeTagFacets(vocabulary, counts, nil)
		want := []TagFacet{
			{Tag: "Art", Count: 7},
			{Tag: "Culture", Count: 2},
			{Tag: "Mobility", Count: 0},
		}
		if !slices.Equal(got, want) {
			t.Errorf("facets = %v, want %v", got, want)
		}
	})

	t.Run("a selected value absent from the vocabulary survives with zero", func(t *testing.T) {
		got := mergeTagFacets(vocabulary, counts, []string{"Retreat"})
		found := false
		for _, facet := range got {
			if facet.Tag == "Retreat" {
				found = true
				if facet.Count != 0 {
					t.Errorf("Retreat count = %d, want 0", facet.Count)
				}
			}
		}
		if !found {
			t.Error("a selected tag must stay representable even with no matches")
		}
		if !slices.IsSortedFunc(got, func(a, b TagFacet) int { return strings.Compare(a.Tag, b.Tag) }) {
			t.Errorf("facets must stay sorted after the union: %v", got)
		}
	})

	t.Run("a selected value already in the vocabulary is not duplicated", func(t *testing.T) {
		got := mergeTagFacets(vocabulary, counts, []string{"Art"})
		seen := 0
		for _, facet := range got {
			if facet.Tag == "Art" {
				seen++
			}
		}
		if seen != 1 {
			t.Errorf("Art appears %d times, want 1", seen)
		}
	})

	t.Run("an empty vocabulary yields an empty, non-nil facet list", func(t *testing.T) {
		got := mergeTagFacets(nil, nil, nil)
		if got == nil {
			t.Error("facets must serialize as [] rather than null")
		}
		if len(got) != 0 {
			t.Errorf("facets = %v, want empty", got)
		}
	})
}

func TestComposePoolTagCountsUseDirectTags(t *testing.T) {
	filter, err := NewCatalogFilter("kijani", []string{"Art"}, TagMatchAll)
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	query := composePoolTagCounts(filter)

	assertPlaceholdersMatchArgs(t, "pool facet counts", query)
	assertNoRequestDataInSQL(t, "pool facet counts", query.sql, filter.Term, "Art")

	if !strings.Contains(query.sql, "INNER JOIN pool_tags pt ON pt.pool_id = c.id") {
		t.Errorf("pool counts must read the direct pool_tags junction:\n%s", query.sql)
	}
	// pool_tags' primary key gives one row per (pool, tag), so no DISTINCT.
	if strings.Contains(query.sql, "COUNT(DISTINCT") {
		t.Errorf("direct pool counts need no DISTINCT; the primary key guarantees it:\n%s", query.sql)
	}
	// Pools filter on their own curated tags, not on member-voucher tags.
	for _, forbidden := range []string{"pool_tokens", "offering_tags", "voucher_tags"} {
		if strings.Contains(query.sql, forbidden) {
			t.Errorf("pool counts must not derive tags from %s:\n%s", forbidden, query.sql)
		}
	}
}

func TestComposePoolPageEveryComposition(t *testing.T) {
	for filterName, filter := range map[string]CatalogFilter{
		"substring":          {Term: "kijani"},
		"prefix":             {Term: "ki"},
		"tags any":           {Tags: []string{"Art"}, TagMatch: TagMatchAny},
		"tags all":           {Tags: []string{"Art", "Culture"}, TagMatch: TagMatchAll},
		"substring tags all": {Term: "kijani", Tags: []string{"Art"}, TagMatch: TagMatchAll},
	} {
		for pageName, page := range map[string]CatalogPage{
			"first": CatalogFirstPage,
			"next":  CatalogNextPage,
			"prev":  CatalogPrevPage,
		} {
			name := filterName + "/" + pageName
			query := composePoolPage(filter, page, 42, 25)

			assertPlaceholdersMatchArgs(t, name, query)
			assertNoRequestDataInSQL(t, name, query.sql, filter.Term, filter.likeTerm())
			for _, tag := range filter.Tags {
				assertNoRequestDataInSQL(t, name, query.sql, tag)
			}

			if !strings.Contains(query.sql, "page AS MATERIALIZED") {
				t.Errorf("%s: must page candidates before aggregating tags and counts:\n%s", name, query.sql)
			}
			// voucher_count is returned by the endpoint so the client never
			// issues one request per pool row.
			if !strings.Contains(query.sql, "AS voucher_count") {
				t.Errorf("%s: must return voucher_count:\n%s", name, query.sql)
			}
			if !strings.Contains(query.sql, "pv.is_allowed") {
				t.Errorf("%s: voucher_count must count allowed members only:\n%s", name, query.sql)
			}
			// Tags are aggregated over the page, not the whole scanned set.
			if !strings.Contains(query.sql, "LEFT JOIN LATERAL") || strings.Contains(query.sql, "GROUP BY p.id") {
				t.Errorf("%s: tags must be aggregated per paged row, not by grouping the scan:\n%s", name, query.sql)
			}
			if !strings.HasSuffix(strings.TrimSpace(query.sql), "ORDER BY p.id DESC;") {
				t.Errorf("%s: must return descending IDs:\n%s", name, query.sql)
			}

			hasMatchSet := strings.Contains(query.sql, "matches AS MATERIALIZED")
			if (filter.Term != "") != hasMatchSet {
				t.Errorf("%s: match-set materialization = %v, want %v:\n%s",
					name, hasMatchSet, filter.Term != "", query.sql)
			}
			if len(filter.Tags) > 0 {
				wantAll := filter.TagMatch == TagMatchAll
				gotAll := strings.Contains(query.sql, "SELECT COUNT(*)\n            FROM pool_tags pt")
				if wantAll != gotAll {
					t.Errorf("%s: all-tags fragment = %v, want %v:\n%s", name, gotAll, wantAll, query.sql)
				}
			}
		}
	}
}

func TestVoucherRelationshipPredicatesAndFacets(t *testing.T) {
	for name, tc := range map[string]struct {
		filter  CatalogFilter
		wantAll bool
	}{
		"any": {filter: CatalogFilter{VoucherIDs: []uint64{7, 11}, VoucherMatch: TagMatchAny}},
		"all": {filter: CatalogFilter{VoucherIDs: []uint64{7, 11}, VoucherMatch: TagMatchAll}, wantAll: true},
	} {
		t.Run("reports/"+name, func(t *testing.T) {
			query := composeReportPage(tc.filter, ReportCursor{}, false, 20)
			assertPlaceholdersMatchArgs(t, "report vouchers", query)
			if !strings.Contains(query.sql, "FROM report_vouchers rv") ||
				!strings.Contains(query.sql, "rv.voucher_id = ANY(") {
				t.Errorf("report page must filter through report_vouchers:\n%s", query.sql)
			}
			if gotAll := strings.Contains(query.sql, ") = cardinality("); gotAll != tc.wantAll {
				t.Errorf("all-match fragment = %v, want %v:\n%s", gotAll, tc.wantAll, query.sql)
			}
			if _, ok := query.args[0].([]int64); !ok {
				t.Errorf("voucher IDs must bind as []int64 for PostgreSQL, got %T", query.args[0])
			}
		})

		t.Run("pools/"+name, func(t *testing.T) {
			query := composePoolPage(tc.filter, CatalogFirstPage, 0, 20)
			assertPlaceholdersMatchArgs(t, "pool vouchers", query)
			if !strings.Contains(query.sql, "FROM pool_tokens pv") ||
				!strings.Contains(query.sql, "pv.is_allowed") ||
				!strings.Contains(query.sql, "pv.voucher_id = ANY(") {
				t.Errorf("pool page must filter through allowed pool_tokens:\n%s", query.sql)
			}
			if gotAll := strings.Contains(query.sql, ") = cardinality("); gotAll != tc.wantAll {
				t.Errorf("all-match fragment = %v, want %v:\n%s", gotAll, tc.wantAll, query.sql)
			}
		})
	}

	filter := CatalogFilter{
		Tags:         []string{"Art"},
		TagMatch:     TagMatchAny,
		VoucherIDs:   []uint64{7},
		VoucherMatch: TagMatchAny,
	}
	for name, query := range map[string]composed{
		"report tags": composeReportTagCounts(filter),
		"pool tags":   composePoolTagCounts(filter),
	} {
		if !strings.Contains(query.sql, "voucher_id = ANY(") {
			t.Errorf("%s must retain the voucher filter:\n%s", name, query.sql)
		}
	}
	for name, query := range map[string]composed{
		"report vouchers": composeReportVoucherCounts(filter),
		"pool vouchers":   composePoolVoucherCounts(filter),
	} {
		assertPlaceholdersMatchArgs(t, name, query)
		if strings.Contains(query.sql, "voucher_id = ANY(") {
			t.Errorf("%s must omit its own selection for disjunctive counts:\n%s", name, query.sql)
		}
		if !strings.Contains(query.sql, "tag_value = ANY(") {
			t.Errorf("%s must retain the tag filter:\n%s", name, query.sql)
		}
	}
}

func TestLoadCatalogSearchQueries(t *testing.T) {
	queries, err := loadQueries("../../core_queries.sql")
	if err != nil {
		t.Fatalf("load core queries: %v", err)
	}

	if queries.UnknownTokenTags == "" {
		t.Error("expected unknown-token-tags query to be loaded")
	}
	// One array parameter, so the statement text does not vary with tag count.
	if !strings.Contains(queries.UnknownTokenTags, "unnest($1::TEXT[])") {
		t.Errorf("unknown-token-tags must take the request as one array parameter:\n%s", queries.UnknownTokenTags)
	}
	if !strings.Contains(queries.UnknownTokenTags, "FROM token_tag tt") {
		t.Errorf("unknown-token-tags must check the token_tag vocabulary:\n%s", queries.UnknownTokenTags)
	}

	for name, query := range map[string]string{
		"list-voucher-tag-vocabulary": queries.ListVoucherTagVocabulary,
		"list-pool-tag-vocabulary":    queries.ListPoolTagVocabulary,
	} {
		if query == "" {
			t.Errorf("expected %s query to be loaded", name)
			continue
		}
		// The vocabulary must not depend on the request, or it could not be
		// cached and would collapse as the caller types.
		if strings.Contains(query, "$1") {
			t.Errorf("%s must be parameterless:\n%s", name, query)
		}
		// Only values actually in use, never the full 44-value token_tag list.
		if strings.Contains(query, "token_tag") {
			t.Errorf("%s must not offer permanently unused global values:\n%s", name, query)
		}
	}
	// The voucher vocabulary is offering-derived and visibility-scoped.
	if !strings.Contains(queries.ListVoucherTagVocabulary, "o.available AND v.active AND v.geo_lat IS NOT NULL") {
		t.Errorf("voucher vocabulary must only count tags a search could return:\n%s",
			queries.ListVoucherTagVocabulary)
	}

	if queries.SetCatalogStatementTimeout == "" {
		t.Error("expected set-catalog-statement-timeout query to be loaded")
	}
	// set_config's third argument is is_local: the timeout must not leak to the
	// next transaction that reuses this pooled connection.
	if !strings.Contains(queries.SetCatalogStatementTimeout, "set_config('statement_timeout', $1, true)") {
		t.Errorf("statement timeout must be transaction-local and parameterized:\n%s", queries.SetCatalogStatementTimeout)
	}
	if queries.ResolveVoucherRefs == "" {
		t.Error("expected resolve-voucher-refs query to be loaded")
	}
	if !strings.Contains(queries.ResolveVoucherRefs, "unnest($1::BIGINT[], $2::TEXT[])") ||
		!strings.Contains(queries.ResolveVoucherRefs, "v.chain_id = ref.chain_id") {
		t.Errorf("resolve-voucher-refs must resolve stable chain/address pairs:\n%s", queries.ResolveVoucherRefs)
	}
}
