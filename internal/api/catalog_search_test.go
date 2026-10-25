package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// parseSearchQuery drives parseCatalogSearch through a real echo context, so the
// test exercises the same query-string decoding the route does.
func parseSearchQuery(t *testing.T, rawQuery string) (catalogSearchRequest, error) {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/api/v2/vouchers?"+rawQuery, nil)
	e := echo.New()
	c := e.NewContext(req, httptest.NewRecorder())
	return parseCatalogSearch(c)
}

func TestParseCatalogSearchReadsTheTerm(t *testing.T) {
	for name, tc := range map[string]struct {
		query string
		want  string
	}{
		"absent":              {query: "", want: ""},
		"empty":               {query: "q=", want: ""},
		"simple":              {query: "q=sarafu", want: "sarafu"},
		"encoded space":       {query: "q=sarafu%20network", want: "sarafu network"},
		"plus space":          {query: "q=sarafu+network", want: "sarafu network"},
		"padding is trimmed":  {query: "q=%20%20sarafu%20%20", want: "sarafu"},
		"runs are collapsed":  {query: "q=sarafu%20%20%20network", want: "sarafu network"},
		"whitespace only":     {query: "q=%20%20%20", want: ""},
		"multibyte preserved": {query: "q=cham%C3%A1", want: "chamá"},
	} {
		got, err := parseSearchQuery(t, tc.query)
		if err != nil {
			t.Errorf("%s: parseCatalogSearch(%q) error = %v", name, tc.query, err)
			continue
		}
		if got.filter.Term != tc.want {
			t.Errorf("%s: term = %q, want %q", name, got.filter.Term, tc.want)
		}
	}
}

func TestParseCatalogSearchRejectsUnusableTerms(t *testing.T) {
	for name, tc := range map[string]struct {
		query   string
		wantErr error
	}{
		"one character":        {query: "q=s", wantErr: store.ErrCatalogTermTooShort},
		"one character padded": {query: "q=%20s%20", wantErr: store.ErrCatalogTermTooShort},
		"too long": {
			query:   "q=" + strings.Repeat("a", store.CatalogTermMaxRunes+1),
			wantErr: store.ErrCatalogTermTooLong,
		},
	} {
		if _, err := parseSearchQuery(t, tc.query); !errors.Is(err, tc.wantErr) {
			t.Errorf("%s: error = %v, want %v", name, err, tc.wantErr)
		}
	}
}

// Tags arrive either repeated or comma-separated, and both must normalize to the
// same deduplicated, sorted set: no token_tag value contains a comma.
func TestParseCatalogSearchNormalizesTags(t *testing.T) {
	for name, tc := range map[string]struct {
		query string
		want  []string
	}{
		"absent":                 {query: "", want: nil},
		"single":                 {query: "tags=Art", want: []string{"Art"}},
		"repeated":               {query: "tags=Art&tags=Culture", want: []string{"Art", "Culture"}},
		"comma separated":        {query: "tags=Art,Culture", want: []string{"Art", "Culture"}},
		"mixed forms":            {query: "tags=Art,Culture&tags=Mobility", want: []string{"Art", "Culture", "Mobility"}},
		"sorted":                 {query: "tags=Mobility,Art", want: []string{"Art", "Mobility"}},
		"deduplicated":           {query: "tags=Art&tags=Art,Art", want: []string{"Art"}},
		"blank values dropped":   {query: "tags=Art,,Culture,", want: []string{"Art", "Culture"}},
		"whitespace trimmed":     {query: "tags=%20Art%20,%20Culture", want: []string{"Art", "Culture"}},
		"only blanks":            {query: "tags=,,", want: nil},
		"multiword value intact": {query: "tags=Food%20production", want: []string{"Food production"}},
	} {
		got, err := parseSearchQuery(t, tc.query)
		if err != nil {
			t.Errorf("%s: parseCatalogSearch(%q) error = %v", name, tc.query, err)
			continue
		}
		if !slices.Equal(got.filter.Tags, tc.want) {
			t.Errorf("%s: tags = %v, want %v", name, got.filter.Tags, tc.want)
		}
	}

	// Two spellings of the same selection must produce identical parameters, so
	// they are one cache key upstream.
	commas, err := parseSearchQuery(t, "tags=Mobility,Art")
	if err != nil {
		t.Fatalf("comma form: %v", err)
	}
	repeated, err := parseSearchQuery(t, "tags=Art&tags=Mobility")
	if err != nil {
		t.Fatalf("repeated form: %v", err)
	}
	if !slices.Equal(commas.filter.Tags, repeated.filter.Tags) {
		t.Errorf("equivalent tag spellings differ: %v vs %v", commas.filter.Tags, repeated.filter.Tags)
	}
}

func TestParseCatalogSearchRejectsTooManyTags(t *testing.T) {
	values := make([]string, 0, store.CatalogMaxTags+1)
	for i := range store.CatalogMaxTags + 1 {
		values = append(values, "tag"+string(rune('a'+i)))
	}

	atLimit := url.Values{"tags": values[:store.CatalogMaxTags]}
	if _, err := parseSearchQuery(t, atLimit.Encode()); err != nil {
		t.Errorf("%d tags rejected: %v", store.CatalogMaxTags, err)
	}

	over := url.Values{"tags": values}
	if _, err := parseSearchQuery(t, over.Encode()); !errors.Is(err, store.ErrCatalogTooManyTags) {
		t.Errorf("error = %v, want ErrCatalogTooManyTags", err)
	}
}

func TestParseCatalogSearchReadsTagMatch(t *testing.T) {
	for name, tc := range map[string]struct {
		query   string
		want    store.TagMatch
		wantErr error
	}{
		"absent defaults to any": {query: "", want: store.TagMatchAny},
		"empty defaults to any":  {query: "tagMatch=", want: store.TagMatchAny},
		"any":                    {query: "tagMatch=any", want: store.TagMatchAny},
		"all":                    {query: "tagMatch=all", want: store.TagMatchAll},
		"case insensitive":       {query: "tagMatch=ALL", want: store.TagMatchAll},
		"padded":                 {query: "tagMatch=%20all%20", want: store.TagMatchAll},
		"unknown rejected":       {query: "tagMatch=either", wantErr: store.ErrCatalogUnknownTagMatch},
		"sql-ish rejected":       {query: "tagMatch=OR%201=1", wantErr: store.ErrCatalogUnknownTagMatch},
	} {
		got, err := parseSearchQuery(t, tc.query)
		if !errors.Is(err, tc.wantErr) {
			t.Errorf("%s: error = %v, want %v", name, err, tc.wantErr)
			continue
		}
		if tc.wantErr == nil && got.filter.TagMatch != tc.want {
			t.Errorf("%s: tagMatch = %v, want %v", name, got.filter.TagMatch, tc.want)
		}
	}
}

func TestParseCatalogSearchReadsFacetsFlag(t *testing.T) {
	for name, tc := range map[string]struct {
		query   string
		want    bool
		wantErr error
	}{
		"absent":     {query: "", want: false},
		"empty":      {query: "facets=", want: false},
		"true":       {query: "facets=true", want: true},
		"one":        {query: "facets=1", want: true},
		"false":      {query: "facets=false", want: false},
		"zero":       {query: "facets=0", want: false},
		"not a bool": {query: "facets=maybe", wantErr: errInvalidFacets},
		"empty-ish":  {query: "facets=%20", want: false},
		// A semicolon makes url.ParseQuery fail; Query() drops the pair rather
		// than returning it, so the parameter reads as absent.
		"semicolon is dropped": {query: "facets=1;DROP", want: false},
		"out of range":         {query: "facets=2", wantErr: errInvalidFacets},
		"with a filter":        {query: "q=sarafu&facets=true", want: true},
	} {
		got, err := parseSearchQuery(t, tc.query)
		if !errors.Is(err, tc.wantErr) {
			t.Errorf("%s: error = %v, want %v", name, err, tc.wantErr)
			continue
		}
		if tc.wantErr == nil && got.facets != tc.want {
			t.Errorf("%s: facets = %v, want %v", name, got.facets, tc.want)
		}
	}
}

// A request with no search parameters must produce an inactive filter, which is
// what routes the read to the existing static statements.
func TestParseCatalogSearchWithoutParametersIsInactive(t *testing.T) {
	got, err := parseSearchQuery(t, "perPage=100&cursor=2212&next=true")
	if err != nil {
		t.Fatalf("parseCatalogSearch: %v", err)
	}
	if got.filter.Active() {
		t.Errorf("a request with no search parameters must not be an active filter: %+v", got.filter)
	}
}

func TestParseCatalogSearchNormalizesRepeatedBounds(t *testing.T) {
	got, err := parseSearchQuery(t,
		"bbox=179,-2,180,2&bbox=-180,-2,-179,2")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.filter.Bounds) != 2 || got.filter.Bounds[0].MinLng != -180 || got.filter.Bounds[1].MinLng != 179 {
		t.Fatalf("bounds = %#v", got.filter.Bounds)
	}
	if !got.filter.Active() {
		t.Fatal("bounded discovery must select the filtered query path")
	}
}

func TestParseCatalogSearchRejectsBoundsConflictsAndMalformedValues(t *testing.T) {
	for name, query := range map[string]string{
		"nearby conflict": "nearby=true&bbox=36,-2,37,-1",
		"too many":        "bbox=0,0,1,1&bbox=1,0,2,1&bbox=2,0,3,1",
		"crossing":        "bbox=179,-2,-179,2",
		"malformed":       "bbox=west,south,east,north",
	} {
		if _, err := parseSearchQuery(t, query); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestParseCatalogRelationshipSearchNormalizesVouchers(t *testing.T) {
	const (
		first  = "0x1111111111111111111111111111111111111111"
		second = "0x2222222222222222222222222222222222222222"
	)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v2/reports?vouchers=42220:"+second+",42220:"+first+"&vouchers=42220:"+first+"&voucherMatch=ALL", nil)
	c := echo.New().NewContext(req, httptest.NewRecorder())

	got, err := parseCatalogRelationshipSearch(c)
	if err != nil {
		t.Fatalf("parseCatalogRelationshipSearch() error = %v", err)
	}
	if len(got.voucherRefs) != 2 {
		t.Fatalf("voucherRefs = %v, want two unique refs", got.voucherRefs)
	}
	if got.voucherRefs[0].Address != first || got.voucherRefs[1].Address != second {
		t.Errorf("voucherRefs = %v, want canonical sorted refs", got.voucherRefs)
	}
	if got.voucherMatch != store.TagMatchAll {
		t.Errorf("voucherMatch = %v, want all", got.voucherMatch)
	}
}

func TestParseCatalogRelationshipSearchRejectsInvalidVouchers(t *testing.T) {
	for name, query := range map[string]string{
		"missing chain":   "vouchers=0x1111111111111111111111111111111111111111",
		"zero chain":      "vouchers=0:0x1111111111111111111111111111111111111111",
		"invalid address": "vouchers=42220:not-an-address",
		"unknown match":   "voucherMatch=sideways",
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/v2/reports?"+query, nil)
		c := echo.New().NewContext(req, httptest.NewRecorder())
		if _, err := parseCatalogRelationshipSearch(c); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestParseReportRelationshipSearchNormalizesCreditablePools(t *testing.T) {
	const (
		first  = "0x1111111111111111111111111111111111111111"
		second = "0x2222222222222222222222222222222222222222"
	)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v2/reports?creditablePools=100:"+second+",100:"+first+
			"&creditablePools=100:"+first, nil)
	c := echo.New().NewContext(req, httptest.NewRecorder())

	got, err := parseReportRelationshipSearch(c)
	if err != nil {
		t.Fatalf("parseReportRelationshipSearch() error = %v", err)
	}
	if len(got.creditablePoolRefs) != 2 || got.creditablePoolRefs[0].Address != first ||
		got.creditablePoolRefs[1].Address != second {
		t.Fatalf("creditablePoolRefs = %#v, want sorted unique refs", got.creditablePoolRefs)
	}
}

func TestParseReportRelationshipSearchRejectsInvalidCreditablePools(t *testing.T) {
	for name, query := range map[string]string{
		"missing chain":   "creditablePools=0x1111111111111111111111111111111111111111",
		"zero chain":      "creditablePools=0:0x1111111111111111111111111111111111111111",
		"invalid address": "creditablePools=100:not-an-address",
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/v2/reports?"+query, nil)
		c := echo.New().NewContext(req, httptest.NewRecorder())
		if _, err := parseReportRelationshipSearch(c); !errors.Is(err, errInvalidPoolRef) {
			t.Errorf("%s: error = %v, want errInvalidPoolRef", name, err)
		}
	}
}

func TestParseMarketDiscoverySearchNormalizesPools(t *testing.T) {
	const (
		first  = "0x1111111111111111111111111111111111111111"
		second = "0x2222222222222222222222222222222222222222"
	)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v2/offerings?pools=100:"+second+",100:"+first+"&pools=100:"+first, nil)
	c := echo.New().NewContext(req, httptest.NewRecorder())
	got, err := parseMarketDiscoverySearch(c)
	if err != nil {
		t.Fatalf("parseMarketDiscoverySearch() error = %v", err)
	}
	if len(got.poolRefs) != 2 || got.poolRefs[0].Address != first || got.poolRefs[1].Address != second {
		t.Fatalf("poolRefs = %#v, want sorted unique refs", got.poolRefs)
	}
}

func TestParseMarketDiscoverySearchRejectsInvalidAndTooManyPools(t *testing.T) {
	for name, query := range map[string]string{
		"missing chain":   "pools=0x1111111111111111111111111111111111111111",
		"zero chain":      "pools=0:0x1111111111111111111111111111111111111111",
		"invalid address": "pools=100:not-an-address",
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/v2/vouchers?"+query, nil)
		c := echo.New().NewContext(req, httptest.NewRecorder())
		if _, err := parseMarketDiscoverySearch(c); !errors.Is(err, errInvalidPoolRef) {
			t.Errorf("%s: error = %v, want errInvalidPoolRef", name, err)
		}
	}

	values := make([]string, 0, store.CatalogMaxPools+1)
	for index := range store.CatalogMaxPools + 1 {
		values = append(values, "100:0x"+fmt.Sprintf("%040x", index+1))
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v2/offerings?"+url.Values{"pools": values}.Encode(), nil)
	c := echo.New().NewContext(req, httptest.NewRecorder())
	if _, err := parseMarketDiscoverySearch(c); !errors.Is(err, store.ErrCatalogTooManyPools) {
		t.Fatalf("over-limit error = %v, want ErrCatalogTooManyPools", err)
	}
}

// The facet is a property of the whole filtered set, so it is computed for the
// first page only and simply absent from cursor pages.
func TestCatalogTagFacetExtrasOnlyRunsOnTheFirstPage(t *testing.T) {
	firstPage := normalizePagination(apiresp.CursorPaginationRequest{})
	cursorPage := normalizePagination(apiresp.CursorPaginationRequest{Cursor: 2212, Next: true})
	if !firstPage.FirstPage || cursorPage.FirstPage {
		t.Fatal("fixture pagination is wrong")
	}

	for name, tc := range map[string]struct {
		search     catalogSearchRequest
		pagination paginationParams
		wantHook   bool
	}{
		"asked for, first page":  {search: catalogSearchRequest{facets: true}, pagination: firstPage, wantHook: true},
		"asked for, cursor page": {search: catalogSearchRequest{facets: true}, pagination: cursorPage, wantHook: false},
		"not asked, first page":  {search: catalogSearchRequest{facets: false}, pagination: firstPage, wantHook: false},
		"not asked, cursor page": {search: catalogSearchRequest{facets: false}, pagination: cursorPage, wantHook: false},
	} {
		hook := catalogTagFacetExtras(tc.search, tc.pagination, nil)
		if (hook != nil) != tc.wantHook {
			t.Errorf("%s: hook present = %v, want %v", name, hook != nil, tc.wantHook)
		}
	}
}

func TestCatalogTagFacetExtrasAttachesTheFacetKey(t *testing.T) {
	facets := []store.TagFacet{{Tag: "Art", Count: 3}, {Tag: "Culture", Count: 0}}
	hook := catalogTagFacetExtras(
		catalogSearchRequest{facets: true},
		normalizePagination(apiresp.CursorPaginationRequest{}),
		func(pgx.Tx) ([]store.TagFacet, error) { return facets, nil },
	)
	if hook == nil {
		t.Fatal("expected a facet hook")
	}

	extras, err := hook(nil)
	if err != nil {
		t.Fatalf("hook: %v", err)
	}
	got, ok := extras["tagFacets"].([]store.TagFacet)
	if !ok {
		t.Fatalf("tagFacets = %#v, want []store.TagFacet", extras["tagFacets"])
	}
	if !slices.Equal(got, facets) {
		t.Errorf("tagFacets = %v, want %v", got, facets)
	}
	// The hook must not smuggle in keys that would collide with the page's own.
	for _, reserved := range []string{"vouchers", "pools", "first", "last"} {
		if _, present := extras[reserved]; present {
			t.Errorf("facet hook must not set the reserved key %q", reserved)
		}
	}
}
