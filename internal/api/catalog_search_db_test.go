package api

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/store"
)

// End-to-end coverage of catalog search through the real route table, middleware
// chain, store, and database. Skipped unless CORE_TEST_DSN is set:
//
//	CORE_TEST_DSN=postgres://postgres:postgres@127.0.0.1:5432/clc_core go test ./internal/api/
//
// Unlike the store tests, these fixtures are committed: the requests under test
// open their own transactions and cannot see an uncommitted one. That makes
// isolation the test's own responsibility, so each test seeds under a token
// unique to it — a leaked fixture from one test then cannot be counted by
// another, which is exactly how a facet-count assertion would otherwise fail for
// an unrelated reason.

// Deliberately shares no substring with the store package's searchToken: these
// fixtures are committed, `go test ./...` runs both packages concurrently, and
// search matches substrings, so an overlapping token would let these rows satisfy
// the other package's assertions.
const (
	searchTokenAPI = "Wkpraqapi"
)

type (
	tagFacetPayload struct {
		Tag   string `json:"tag"`
		Count uint64 `json:"count"`
	}

	searchPageResult struct {
		Vouchers []marketVoucherPayload `json:"vouchers"`
		First    *uint64                `json:"first"`
		Last     *uint64                `json:"last"`
		// A pointer so an absent key is distinguishable from an empty facet.
		TagFacets *[]tagFacetPayload `json:"tagFacets"`
	}

	// searchFixture is one seeded voucher: a name, a symbol, and the tags on its
	// single available offering.
	searchFixture struct {
		name   string
		symbol string
		tags   []string
	}
)

// seedSearchFixtures commits the fixtures and returns their voucher IDs plus a
// cleanup.
//
// Cleanup deletes offerings before vouchers: offering_tags cascades from
// offerings, but offerings.voucher_id is ON DELETE NO ACTION, so deleting the
// voucher first fails on the foreign key and leaves the rows behind.
func seedSearchFixtures(t *testing.T, a *API, marker string, fixtures []searchFixture) ([]uint64, func()) {
	t.Helper()
	ctx := context.Background()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}

	ids := make([]uint64, 0, len(fixtures))
	for _, fixture := range fixtures {
		var voucherID uint64
		if err := tx.QueryRow(ctx, `
			INSERT INTO vouchers (chain_id, address, name, symbol, decimals, active, geo_lat, geo_lng, location_name)
			VALUES (42220, '0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'),
			        $1, $2, 6, TRUE, -1.29, 36.82, $3)
			RETURNING id`, fixture.name, fixture.symbol, marker).Scan(&voucherID); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("seed voucher %q: %v", fixture.name, err)
		}
		ids = append(ids, voucherID)

		var offeringID uint64
		if err := tx.QueryRow(ctx, `
			INSERT INTO offerings (voucher_id, seller_address, commodity_name, commodity_type, description, quantity, available)
			VALUES ($1, '0x0000000000000000000000000000000000000001', $2, 'GOOD', '', 1, TRUE)
			RETURNING id`, voucherID, marker).Scan(&offeringID); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("seed offering: %v", err)
		}
		for _, tag := range fixture.tags {
			if _, err := tx.Exec(ctx,
				`INSERT INTO offering_tags (offering_id, tag_value) VALUES ($1, $2)`,
				offeringID, tag); err != nil {
				_ = tx.Rollback(ctx)
				t.Fatalf("seed tag %q: %v", tag, err)
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	return ids, func() {
		cleanupTx, err := a.store.Pool().Begin(ctx)
		if err != nil {
			t.Errorf("begin cleanup: %v", err)
			return
		}
		defer cleanupTx.Rollback(ctx)

		if _, err := cleanupTx.Exec(ctx,
			`DELETE FROM offerings WHERE commodity_name = $1`, marker); err != nil {
			t.Errorf("cleanup offerings: %v", err)
			return
		}
		if _, err := cleanupTx.Exec(ctx,
			`DELETE FROM vouchers WHERE location_name = $1`, marker); err != nil {
			t.Errorf("cleanup vouchers: %v", err)
			return
		}
		if err := cleanupTx.Commit(ctx); err != nil {
			t.Errorf("commit cleanup: %v", err)
		}
	}
}

// searchURL builds a catalog request, escaping every value. Tag values contain
// spaces ("Common Spaces"), so they cannot be concatenated raw.
func searchURL(path string, params map[string][]string) string {
	values := url.Values{}
	for key, list := range params {
		for _, value := range list {
			values.Add(key, value)
		}
	}
	return path + "?" + values.Encode()
}

// searchRequestCounter gives every request its own source address. The public
// market routes are rate limited per IP at a burst of 40, and a test that checks
// one facet value per request exceeds that; the limiter has its own tests, so
// tripping it here would only obscure what these tests are about.
var (
	searchRequestCounter atomic.Uint64
)

func getSearchPage(t *testing.T, a *API, target string) (*httptest.ResponseRecorder, searchPageResult) {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = fmt.Sprintf("198.51.100.%d:5000", searchRequestCounter.Add(1)%256)
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, req)

	var envelope struct {
		Ok     bool             `json:"ok"`
		Result searchPageResult `json:"result"`
	}
	if res.Code == http.StatusOK {
		if err := json.Unmarshal(res.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode %s: %v", target, err)
		}
	}
	return res, envelope.Result
}

func searchVoucherIDs(page searchPageResult) []uint64 {
	ids := make([]uint64, 0, len(page.Vouchers))
	for _, v := range page.Vouchers {
		ids = append(ids, v.ID)
	}
	return ids
}

func sortedIDs(values ...uint64) []uint64 {
	slices.Sort(values)
	return values
}

func TestVouchersEndpointSearchesAndFilters(t *testing.T) {
	a := newMarketAPIWithStore(t)
	token := searchTokenAPI + "Filters"

	ids, cleanup := seedSearchFixtures(t, a, token, []searchFixture{
		{name: token + " Art House", symbol: "QAH", tags: []string{"Art"}},
		{name: token + " Culture Club", symbol: "QCC", tags: []string{"Culture"}},
		{name: token + " Both Ways", symbol: "QBW", tags: []string{"Art", "Culture"}},
		{name: "Unrelated Name", symbol: token + "SYM"},
	})
	defer cleanup()
	art, culture, both, bySymbol := ids[0], ids[1], ids[2], ids[3]

	got := func(t *testing.T, params map[string][]string) []uint64 {
		t.Helper()
		params["perPage"] = []string{"500"}
		target := searchURL("/api/v2/vouchers", params)
		res, page := getSearchPage(t, a, target)
		if res.Code != http.StatusOK {
			t.Fatalf("GET %s: status = %d, body = %s", target, res.Code, res.Body.String())
		}
		return sortedIDs(searchVoucherIDs(page)...)
	}

	t.Run("term matches names and symbols", func(t *testing.T) {
		want := sortedIDs(art, culture, both, bySymbol)
		if ids := got(t, map[string][]string{"q": {token}}); !slices.Equal(ids, want) {
			t.Errorf("ids = %v, want %v", ids, want)
		}
	})

	t.Run("tags default to matching any", func(t *testing.T) {
		want := sortedIDs(art, culture, both)
		ids := got(t, map[string][]string{"q": {token}, "tags": {"Art", "Culture"}})
		if !slices.Equal(ids, want) {
			t.Errorf("ids = %v, want %v", ids, want)
		}
	})

	t.Run("tagMatch=all requires every tag", func(t *testing.T) {
		ids := got(t, map[string][]string{"q": {token}, "tags": {"Art", "Culture"}, "tagMatch": {"all"}})
		if !slices.Equal(ids, []uint64{both}) {
			t.Errorf("ids = %v, want [%d]", ids, both)
		}
	})

	t.Run("a tag alone filters without a term", func(t *testing.T) {
		ids := got(t, map[string][]string{"tags": {"Culture"}})
		for _, want := range []uint64{culture, both} {
			if !slices.Contains(ids, want) {
				t.Errorf("voucher %d carries Culture but is missing from %v", want, ids)
			}
		}
		if slices.Contains(ids, art) {
			t.Errorf("voucher %d carries only Art but matched a Culture filter", art)
		}
	})

	t.Run("an unfiltered request keeps its response shape", func(t *testing.T) {
		res, page := getSearchPage(t, a, "/api/v2/vouchers?perPage=10")
		if res.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
		}
		if len(page.Vouchers) == 0 {
			t.Error("an unfiltered page must still return the catalog")
		}
		if page.TagFacets != nil {
			t.Error("tagFacets must be absent unless facets=true is requested")
		}
	})

	t.Run("public cache headers survive on a filtered page", func(t *testing.T) {
		res, _ := getSearchPage(t, a, searchURL("/api/v2/vouchers", map[string][]string{"q": {token}}))
		if cc := res.Header().Get("Cache-Control"); !strings.Contains(cc, "public") {
			t.Errorf("Cache-Control = %q, want a public policy", cc)
		}
		if vary := res.Header().Values("Vary"); !slices.ContainsFunc(vary, func(v string) bool {
			return strings.Contains(strings.ToLower(v), "origin")
		}) {
			t.Errorf("Vary = %v, want it to include Origin", vary)
		}
	})
}

// Every paging direction must carry every active filter. A direction that fell
// through to an unfiltered statement would widen the result silently.
func TestVouchersEndpointAppliesFiltersInEveryPagingDirection(t *testing.T) {
	a := newMarketAPIWithStore(t)
	token := searchTokenAPI + "Paging"

	fixtures := make([]searchFixture, 0, 8)
	for i := range 6 {
		fixtures = append(fixtures, searchFixture{
			name:   fmt.Sprintf("%s Page %d", token, i),
			symbol: fmt.Sprintf("QP%d", i),
			tags:   []string{"Art"},
		})
	}
	// Decoys: one matches the term but not the tag, one the tag but not the term.
	fixtures = append(fixtures,
		searchFixture{name: token + " Decoy", symbol: "QD1", tags: []string{"Culture"}},
		searchFixture{name: "Unrelated Decoy", symbol: "QD2", tags: []string{"Art"}},
	)

	ids, cleanup := seedSearchFixtures(t, a, token, fixtures)
	defer cleanup()

	tagged := ids[:6]
	allowed := map[uint64]bool{}
	for _, id := range tagged {
		allowed[id] = true
	}

	fetch := func(t *testing.T, label string, extra map[string][]string) searchPageResult {
		t.Helper()
		params := map[string][]string{"perPage": {"2"}, "q": {token}, "tags": {"Art"}}
		maps.Copy(params, extra)
		target := searchURL("/api/v2/vouchers", params)
		res, page := getSearchPage(t, a, target)
		if res.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, body = %s", label, res.Code, res.Body.String())
		}
		for _, v := range page.Vouchers {
			if !allowed[v.ID] {
				t.Errorf("%s leaked voucher %d, which does not satisfy both the term and the tag", label, v.ID)
			}
		}
		return page
	}

	first := fetch(t, "first page", nil)
	if len(first.Vouchers) != 2 {
		t.Fatalf("first page returned %d rows, want 2", len(first.Vouchers))
	}

	second := fetch(t, "second page", map[string][]string{
		"next":   {"true"},
		"cursor": {strconv.FormatUint(*first.Last, 10)},
	})
	if len(second.Vouchers) != 2 {
		t.Fatalf("second page returned %d rows, want 2", len(second.Vouchers))
	}

	// Paging back from the second page's head restores the first page, with the
	// filters still applied.
	previous := fetch(t, "previous page", map[string][]string{
		"cursor": {strconv.FormatUint(*second.First, 10)},
	})
	if !slices.Equal(searchVoucherIDs(previous), searchVoucherIDs(first)) {
		t.Errorf("previous page = %v, want the first page %v",
			searchVoucherIDs(previous), searchVoucherIDs(first))
	}

	third := fetch(t, "third page", map[string][]string{
		"next":   {"true"},
		"cursor": {strconv.FormatUint(*second.Last, 10)},
	})

	// Walking forward covers exactly the tagged fixtures, with no duplicates or
	// skips, and never the decoys.
	walked := slices.Concat(
		searchVoucherIDs(first), searchVoucherIDs(second), searchVoucherIDs(third))
	deduped := sortedIDs(walked...)
	deduped = slices.Compact(deduped)
	if len(deduped) != len(walked) {
		t.Errorf("paging returned duplicate rows: %v", walked)
	}
	if want := sortedIDs(tagged...); !slices.Equal(deduped, want) {
		t.Errorf("paged set = %v, want the six tagged fixtures %v", deduped, want)
	}
}

func TestVouchersEndpointReturnsTagFacetsOnTheFirstPageOnly(t *testing.T) {
	a := newMarketAPIWithStore(t)
	token := searchTokenAPI + "Facets"

	ids, cleanup := seedSearchFixtures(t, a, token, []searchFixture{
		{name: token + " Facet A", symbol: "QFA", tags: []string{"Art"}},
		{name: token + " Facet B", symbol: "QFB", tags: []string{"Art"}},
		{name: token + " Facet C", symbol: "QFC", tags: []string{"Culture"}},
		// Named without the token, so Mobility is in the vocabulary with a count of 0 under this
		// term. Without it the zero-count assertion below only held where an imported catalog
		// happened to supply an unmatched tag.
		{name: "Outside The Facet Term", symbol: "QFD", tags: []string{"Mobility"}},
	})
	defer cleanup()

	target := searchURL("/api/v2/vouchers", map[string][]string{
		"perPage": {"500"}, "facets": {"true"}, "q": {token},
	})
	res, page := getSearchPage(t, a, target)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if page.TagFacets == nil {
		t.Fatal("facets=true on a first page must return tagFacets")
	}

	counts := map[string]uint64{}
	zeroes := 0
	for _, facet := range *page.TagFacets {
		counts[facet.Tag] = facet.Count
		if facet.Count == 0 {
			zeroes++
		}
	}
	if counts["Art"] != 2 {
		t.Errorf("Art count = %d, want 2", counts["Art"])
	}
	if counts["Culture"] != 1 {
		t.Errorf("Culture count = %d, want 1", counts["Culture"])
	}
	// Zero-count values are represented, so a client can grey out a filter that
	// exists rather than hide it.
	if counts["Mobility"] != 0 {
		t.Errorf("Mobility count = %d, want 0 — the term matches no voucher carrying it",
			counts["Mobility"])
	}
	if zeroes == 0 {
		t.Error("expected zero-count facet values to be represented")
	}

	// Each facet count must equal what the same single-tag filter returns.
	for tag, count := range counts {
		single := searchURL("/api/v2/vouchers", map[string][]string{
			"perPage": {"500"}, "q": {token}, "tags": {tag},
		})
		res, filtered := getSearchPage(t, a, single)
		if res.Code != http.StatusOK {
			t.Fatalf("GET %s: status = %d", single, res.Code)
		}
		if uint64(len(filtered.Vouchers)) != count {
			t.Errorf("facet %q promised %d rows but the filter returned %d",
				tag, count, len(filtered.Vouchers))
		}
	}

	// A facet describes the whole filtered set, so a cursor page does not repeat
	// the work: the key is simply absent.
	cursorTarget := searchURL("/api/v2/vouchers", map[string][]string{
		"perPage": {"1"}, "facets": {"true"}, "next": {"true"},
		"cursor": {strconv.FormatUint(ids[2]+1, 10)}, "q": {token},
	})
	_, cursorPage := getSearchPage(t, a, cursorTarget)
	if cursorPage.TagFacets != nil {
		t.Error("a cursor page must not recompute tagFacets")
	}
}

func TestVouchersEndpointRejectsInvalidSearchParameters(t *testing.T) {
	a := newMarketAPIWithStore(t)

	for name, params := range map[string]map[string][]string{
		"one-character term": {"q": {"s"}},
		"overlong term":      {"q": {strings.Repeat("a", store.CatalogTermMaxRunes+1)}},
		"unknown tagMatch":   {"tagMatch": {"either"}},
		"unparseable facets": {"facets": {"2"}},
		// Rejected rather than dropped: answering with the tag ignored would
		// answer a different question, and would report facet counts for a
		// filter that was never applied.
		"unknown tag":            {"tags": {"NotARealTag"}},
		"unknown tag among good": {"tags": {"Art", "NotARealTag"}},
		// The vocabulary is served verbatim, so a case variant is a different
		// value and is not silently accepted.
		"case-variant tag": {"tags": {"art"}},
	} {
		target := searchURL("/api/v2/vouchers", params)
		res, _ := getSearchPage(t, a, target)
		if res.Code != http.StatusBadRequest {
			t.Errorf("%s (%s): status = %d, want 400. body = %s",
				name, target, res.Code, res.Body.String())
		}
	}

	// Too many tags, built from real vocabulary values so it is the count that is
	// rejected rather than the values.
	ctx := context.Background()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, "SELECT value FROM token_tag ORDER BY value LIMIT $1",
		store.CatalogMaxTags+1)
	if err != nil {
		t.Fatalf("read vocabulary: %v", err)
	}
	var vocabulary []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		vocabulary = append(vocabulary, value)
	}
	rows.Close()
	if len(vocabulary) <= store.CatalogMaxTags {
		t.Skipf("need %d tag values to exceed the limit, found %d",
			store.CatalogMaxTags+1, len(vocabulary))
	}

	target := searchURL("/api/v2/vouchers", map[string][]string{"tags": vocabulary})
	res, _ := getSearchPage(t, a, target)
	if res.Code != http.StatusBadRequest {
		t.Errorf("%d tags: status = %d, want 400. body = %s",
			len(vocabulary), res.Code, res.Body.String())
	}
}

// The term reaches PostgreSQL as a bound parameter, so LIKE metacharacters are
// literals and anything shaped like SQL is inert text.
func TestVouchersEndpointTreatsSearchTermsAsData(t *testing.T) {
	a := newMarketAPIWithStore(t)
	token := searchTokenAPI + "Escaping"

	ids, cleanup := seedSearchFixtures(t, a, token, []searchFixture{
		{name: token + " 50% Off", symbol: "QPC"},
		{name: token + " a_b Group", symbol: "QUS"},
		{name: token + " axb Group", symbol: "QAX"},
	})
	defer cleanup()
	percent, underscore, plain := ids[0], ids[1], ids[2]

	search := func(t *testing.T, term string) (*httptest.ResponseRecorder, []uint64) {
		t.Helper()
		target := searchURL("/api/v2/vouchers", map[string][]string{
			"perPage": {"500"}, "q": {term},
		})
		res, page := getSearchPage(t, a, target)
		return res, searchVoucherIDs(page)
	}

	t.Run("percent is a literal", func(t *testing.T) {
		res, got := search(t, "50%")
		if res.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
		}
		if !slices.Equal(got, []uint64{percent}) {
			t.Errorf("ids = %v, want [%d]; an unescaped %% would match the whole catalog", got, percent)
		}
	})

	t.Run("underscore is a literal", func(t *testing.T) {
		res, got := search(t, "a_b Group")
		if res.Code != http.StatusOK {
			t.Fatalf("status = %d", res.Code)
		}
		if !slices.Equal(got, []uint64{underscore}) {
			t.Errorf("ids = %v, want [%d]; an unescaped _ would also match %d (axb)",
				got, underscore, plain)
		}
	})

	t.Run("sql-shaped terms match nothing and do not error", func(t *testing.T) {
		for _, hostile := range []string{
			"'; DROP TABLE vouchers; --",
			"%' OR '1'='1",
			"_____",
			// Two characters: a lone backslash is one rune and is correctly
			// rejected by the minimum-length rule, which would prove nothing here.
			`\\`,
			`\%`,
			"' UNION SELECT null--",
		} {
			res, got := search(t, hostile)
			if res.Code != http.StatusOK {
				t.Errorf("q=%q: status = %d, want 200. body = %s", hostile, res.Code, res.Body.String())
				continue
			}
			if len(got) != 0 {
				t.Errorf("q=%q matched %v; the term must be literal data", hostile, got)
			}
		}
	})

	// The catalog is intact.
	ctx := context.Background()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	var count int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM vouchers").Scan(&count); err != nil {
		t.Fatalf("count vouchers: %v", err)
	}
	if count == 0 {
		t.Fatal("vouchers table is empty after hostile input")
	}
}

// Catalog reads must carry a statement timeout; PostgreSQL's default is
// unlimited, so without this a pathological plan holds a pooled connection.
func TestCatalogReadsSetAStatementTimeout(t *testing.T) {
	a := newMarketAPIWithStore(t)
	ctx := context.Background()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	var before string
	if err := tx.QueryRow(ctx, "SHOW statement_timeout").Scan(&before); err != nil {
		t.Fatalf("read statement_timeout: %v", err)
	}
	if err := a.store.SetCatalogStatementTimeout(ctx, tx); err != nil {
		t.Fatalf("SetCatalogStatementTimeout: %v", err)
	}

	var after string
	if err := tx.QueryRow(ctx, "SHOW statement_timeout").Scan(&after); err != nil {
		t.Fatalf("read statement_timeout: %v", err)
	}
	if after == before {
		t.Errorf("statement_timeout unchanged (%q); catalog reads would be unbounded", after)
	}
}

// Slice 2: the pool catalog gains the same filters, over the pool's own curated
// pool_tags — the values each row already returns.
//
// The pool routes sit on the authenticated API group, so this drives the store
// directly; the handler's own parsing and rejection paths are covered by the unit
// tests and by the public voucher endpoint above.
func TestPoolsCatalogSearchesAndFilters(t *testing.T) {
	a := newMarketAPIWithStore(t)
	ctx := context.Background()
	token := searchTokenAPI + "Pools"

	seedTx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}
	var poolIDs []uint64
	for _, fixture := range []searchFixture{
		{name: token + " Art Pool", symbol: "QAP", tags: []string{"Art"}},
		{name: token + " Both Pool", symbol: "QBP", tags: []string{"Art", "Culture"}},
		{name: token + " Plain Pool", symbol: "QPP"},
	} {
		var id uint64
		if err := seedTx.QueryRow(ctx, `
			INSERT INTO pools (chain_id, address, name, symbol, decimals, location_name)
			VALUES (42220, '0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'), $1, $2, 6, $3)
			RETURNING id`, fixture.name, fixture.symbol, token).Scan(&id); err != nil {
			_ = seedTx.Rollback(ctx)
			t.Fatalf("seed pool: %v", err)
		}
		poolIDs = append(poolIDs, id)
		for _, tag := range fixture.tags {
			if _, err := seedTx.Exec(ctx,
				`INSERT INTO pool_tags (pool_id, tag_value) VALUES ($1, $2)`, id, tag); err != nil {
				_ = seedTx.Rollback(ctx)
				t.Fatalf("seed pool tag: %v", err)
			}
		}
	}
	if err := seedTx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	defer func() {
		// pool_tags cascades from pools, so one delete is enough here.
		cleanupTx, err := a.store.Pool().Begin(ctx)
		if err != nil {
			t.Errorf("begin cleanup: %v", err)
			return
		}
		defer cleanupTx.Rollback(ctx)
		if _, err := cleanupTx.Exec(ctx,
			`DELETE FROM pools WHERE location_name = $1`, token); err != nil {
			t.Errorf("cleanup pools: %v", err)
			return
		}
		if err := cleanupTx.Commit(ctx); err != nil {
			t.Errorf("commit cleanup: %v", err)
		}
	}()

	readTx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin read: %v", err)
	}
	defer readTx.Rollback(ctx)

	list := func(t *testing.T, tags []string, match store.TagMatch) []store.Pool {
		t.Helper()
		filter, err := store.NewCatalogFilter(token, tags, match)
		if err != nil {
			t.Fatalf("filter: %v", err)
		}
		pools, err := a.store.ListPoolsFiltered(ctx, readTx, filter, store.CatalogFirstPage, 0, 100)
		if err != nil {
			t.Fatalf("list pools: %v", err)
		}
		return pools
	}
	listIDs := func(t *testing.T, tags []string, match store.TagMatch) []uint64 {
		t.Helper()
		pools := list(t, tags, match)
		ids := make([]uint64, 0, len(pools))
		for _, p := range pools {
			ids = append(ids, p.ID)
		}
		return sortedIDs(ids...)
	}

	t.Run("term matches pool names and symbols", func(t *testing.T) {
		if got := listIDs(t, nil, store.TagMatchAny); !slices.Equal(got, sortedIDs(poolIDs...)) {
			t.Errorf("ids = %v, want %v", got, sortedIDs(poolIDs...))
		}
	})

	t.Run("direct pool tags filter with any and all", func(t *testing.T) {
		want := sortedIDs(poolIDs[0], poolIDs[1])
		if got := listIDs(t, []string{"Art"}, store.TagMatchAny); !slices.Equal(got, want) {
			t.Errorf("any: ids = %v, want %v", got, want)
		}
		got := listIDs(t, []string{"Art", "Culture"}, store.TagMatchAll)
		if !slices.Equal(got, []uint64{poolIDs[1]}) {
			t.Errorf("all: ids = %v, want [%d]", got, poolIDs[1])
		}
	})

	t.Run("a filtered row carries the tag that matched it", func(t *testing.T) {
		for _, pool := range list(t, []string{"Art"}, store.TagMatchAll) {
			if !slices.Contains(pool.Tags, "Art") {
				t.Errorf("pool %d matched the Art filter but returned tags %v", pool.ID, pool.Tags)
			}
		}
	})

	t.Run("rows carry voucherCount so the client needs no request per row", func(t *testing.T) {
		pools := list(t, nil, store.TagMatchAny)
		if len(pools) == 0 {
			t.Fatal("expected the seeded pools")
		}
		for _, pool := range pools {
			// Zero is the correct count for these fixtures; what matters is that
			// the field is present, so membership is never fetched per row.
			if pool.VoucherCount != 0 {
				t.Errorf("pool %d: voucherCount = %d, want 0", pool.ID, pool.VoucherCount)
			}
		}
	})

	t.Run("paging applies the filter in every direction", func(t *testing.T) {
		filter, err := store.NewCatalogFilter(token, nil, store.TagMatchAny)
		if err != nil {
			t.Fatalf("filter: %v", err)
		}
		allowed := map[uint64]bool{}
		for _, id := range poolIDs {
			allowed[id] = true
		}

		first, err := a.store.ListPoolsFiltered(ctx, readTx, filter, store.CatalogFirstPage, 0, 2)
		if err != nil {
			t.Fatalf("first page: %v", err)
		}
		if len(first) != 2 {
			t.Fatalf("first page returned %d rows, want 2", len(first))
		}
		next, err := a.store.ListPoolsFiltered(ctx, readTx, filter,
			store.CatalogNextPage, first[len(first)-1].ID, 2)
		if err != nil {
			t.Fatalf("next page: %v", err)
		}
		previous, err := a.store.ListPoolsFiltered(ctx, readTx, filter,
			store.CatalogPrevPage, next[0].ID, 2)
		if err != nil {
			t.Fatalf("previous page: %v", err)
		}

		for label, rows := range map[string][]store.Pool{
			"first": first, "next": next, "previous": previous,
		} {
			for _, pool := range rows {
				if !allowed[pool.ID] {
					t.Errorf("%s page leaked unfiltered pool %d", label, pool.ID)
				}
			}
		}
		if len(previous) != len(first) || previous[0].ID != first[0].ID {
			t.Errorf("previous page did not restore the first page: %v vs %v", previous, first)
		}
	})

	t.Run("facet counts match the result queries", func(t *testing.T) {
		filter, err := store.NewCatalogFilter(token, nil, store.TagMatchAny)
		if err != nil {
			t.Fatalf("filter: %v", err)
		}
		facets, err := a.store.ListPoolTagFacets(ctx, readTx, filter)
		if err != nil {
			t.Fatalf("pool facets: %v", err)
		}
		if len(facets) == 0 {
			t.Fatal("expected a non-empty pool facet vocabulary")
		}
		for _, facet := range facets {
			rows := listIDs(t, []string{facet.Tag}, store.TagMatchAny)
			if uint64(len(rows)) != facet.Count {
				t.Errorf("pool facet %q promised %d rows but the filter returned %d",
					facet.Tag, facet.Count, len(rows))
			}
		}
	})
}

// The filter-free facet is the one response cached in process, so both halves of
// that decision need proving: the cacheable shape is actually served from cache,
// and every request-specific shape still reaches the database.
//
// The cache is asserted by freezing: a facet read before a fixture is committed
// must come back unchanged after it, because a cached response is returned
// verbatim. That makes the assertion independent of what else is in the catalog.
func TestVouchersEndpointCachesOnlyTheFilterFreeFacet(t *testing.T) {
	a := newMarketAPIWithStore(t)
	token := searchTokenAPI + "Cache"

	filterFree := searchURL("/api/v2/vouchers", map[string][]string{
		"perPage": {"1"}, "facets": {"true"},
	})

	res, before := getSearchPage(t, a, filterFree)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if before.TagFacets == nil {
		t.Fatal("facets=true with no filter must return tagFacets; it is the vocabulary bootstrap")
	}
	// Nothing narrows this request, so every value it returns is carried by
	// something visible.
	for _, facet := range *before.TagFacets {
		if facet.Count == 0 {
			t.Errorf("filter-free facet carries a zero count for %q", facet.Tag)
		}
	}

	_, cleanup := seedSearchFixtures(t, a, token, []searchFixture{
		{name: token + " Cached A", symbol: "QCA", tags: []string{"Art"}},
		{name: token + " Cached B", symbol: "QCB", tags: []string{"Art"}},
	})
	defer cleanup()

	_, after := getSearchPage(t, a, filterFree)
	if after.TagFacets == nil {
		t.Fatal("the second filter-free request returned no tagFacets")
	}
	if !slices.Equal(*before.TagFacets, *after.TagFacets) {
		t.Errorf("the filter-free facet changed after a fixture was committed, so it was "+
			"recomputed rather than served from cache.\nbefore: %v\nafter:  %v",
			*before.TagFacets, *after.TagFacets)
	}

	// A term makes the facet request-specific, so it must bypass the cache and see
	// the fixtures the cached response predates.
	_, filtered := getSearchPage(t, a, searchURL("/api/v2/vouchers", map[string][]string{
		"perPage": {"500"}, "facets": {"true"}, "q": {token},
	}))
	if filtered.TagFacets == nil {
		t.Fatal("a filtered faceted request returned no tagFacets")
	}
	counts := map[string]uint64{}
	for _, facet := range *filtered.TagFacets {
		counts[facet.Tag] = facet.Count
	}
	if counts["Art"] != 2 {
		t.Errorf("filtered Art count = %d, want 2; a filtered facet must not be served "+
			"from the filter-free cache", counts["Art"])
	}

	// A viewport narrows too, so a bounded request is request-specific even with no
	// term: this box contains none of the catalog, so every count in it is zero.
	_, bounded := getSearchPage(t, a, searchURL("/api/v2/vouchers", map[string][]string{
		"perPage": {"500"}, "facets": {"true"}, "bbox": {"100,40,101,41"},
	}))
	if bounded.TagFacets == nil {
		t.Fatal("a bounded faceted request returned no tagFacets")
	}
	for _, facet := range *bounded.TagFacets {
		if facet.Count != 0 {
			t.Errorf("bounded facet %q counts %d inside an empty box; it was served from "+
				"the filter-free cache", facet.Tag, facet.Count)
		}
	}
}
