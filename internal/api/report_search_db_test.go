package api

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/golang-jwt/jwt/v5"
	"github.com/grassrootseconomics/ethutils"
)

// End-to-end coverage of report search through the real route table, middleware chain, store,
// and database (SEARCH_FILTER_PLAN.md Slice 4). Skipped unless CORE_TEST_DSN is set.
//
// The feed is authenticated, so these mint a service JWT against the API's own signing key
// rather than reaching past the middleware. That is the point: the filter parameters have to
// work *through* the auth gate, and a test that called the store directly would not prove the
// handler wires them at all.
//
// Fixtures are committed, like the rest of this package's live tests, so isolation is each
// test's own responsibility. Every fixture embeds a token no imported report contains.

const (
	reportSearchToken = "Xhqzreport"
)

type (
	reportTagFacetPayload struct {
		Tag   string `json:"tag"`
		Count uint64 `json:"count"`
	}

	reportVoucherFacetPayload struct {
		ID      uint64 `json:"id"`
		ChainID int64  `json:"chainId"`
		Address string `json:"address"`
		Name    string `json:"name"`
		Symbol  string `json:"symbol"`
		Count   uint64 `json:"count"`
	}

	reportPoolFacetPayload = reportVoucherFacetPayload

	reportPagePayload struct {
		Reports []struct {
			ID          uint64   `json:"id"`
			Title       string   `json:"title"`
			Description string   `json:"description"`
			Tags        []string `json:"tags"`
			Vouchers    []struct {
				ID      uint64 `json:"id"`
				ChainID int64  `json:"chainId"`
				Address string `json:"address"`
			} `json:"vouchers"`
			Pools []struct {
				ID      uint64 `json:"id"`
				ChainID int64  `json:"chainId"`
				Address string `json:"address"`
			} `json:"pools"`
		} `json:"reports"`
		Next *struct {
			UpdatedAt string `json:"updatedAt"`
			ID        uint64 `json:"id"`
		} `json:"next"`
		// A pointer so an absent key is distinguishable from an empty facet.
		TagFacets *[]reportTagFacetPayload `json:"tagFacets"`
		// Voucher facets share the same first-page-only contract.
		VoucherFacets *[]reportVoucherFacetPayload `json:"voucherFacets"`
		PoolFacets    *[]reportPoolFacetPayload    `json:"poolFacets"`
	}
)

// newReportSearchAPI builds an API with a real store and a signing key, plus the bearer token
// that gets past the authenticated group.
func newReportSearchAPI(t *testing.T) (*API, string) {
	t.Helper()

	dsn := os.Getenv("CORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CORE_TEST_DSN not set; skipping live report search tests")
	}

	logg := slog.New(slog.DiscardHandler)
	pgStore, err := store.NewPgStore(store.PgOpts{
		Logg:                 logg,
		DSN:                  dsn,
		MigrationsFolderPath: "../../migrations",
		QueriesFolderPath:    "../../core_queries.sql",
	})
	if err != nil {
		t.Fatalf("connect store: %v", err)
	}
	t.Cleanup(pgStore.Pool().Close)

	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	a := New(APIOpts{
		Logg:         logg,
		CORS:         []string{"https://app.example"},
		Store:        pgStore,
		SigningKey:   private,
		VerifyingKey: public,
	})

	claims := JWTCustomClaims{
		Service:   true,
		PublicKey: ethutils.ZeroAddress.Hex(),
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "clc-core-test",
			Subject:   "report-search-test",
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(private)
	if err != nil {
		t.Fatalf("sign service token: %v", err)
	}

	return a, token
}

func seedReportVoucher(t *testing.T, a *API, name, symbol string) (uint64, string, func()) {
	t.Helper()
	ctx := context.Background()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin voucher seed: %v", err)
	}

	var id uint64
	var address string
	if err := tx.QueryRow(ctx, `
		INSERT INTO vouchers (chain_id, address, name, symbol, decimals, active)
		VALUES (42220, '0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'), $1, $2, 6, TRUE)
		RETURNING id, address`, name, symbol).Scan(&id, &address); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("seed report voucher: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit voucher seed: %v", err)
	}

	return id, address, func() {
		cleanupTx, cleanupErr := a.store.Pool().Begin(context.Background())
		if cleanupErr != nil {
			return
		}
		_, _ = cleanupTx.Exec(context.Background(), `DELETE FROM vouchers WHERE id = $1`, id)
		_ = cleanupTx.Commit(context.Background())
	}
}

func linkReportVoucher(t *testing.T, a *API, reportID, voucherID uint64) {
	t.Helper()
	ctx := context.Background()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin report-voucher link: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO report_vouchers (report_id, voucher_id) VALUES ($1, $2)`,
		reportID, voucherID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("link report voucher: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit report-voucher link: %v", err)
	}
}

func seedReportPool(t *testing.T, a *API, name, symbol string) (uint64, string, func()) {
	t.Helper()
	ctx := context.Background()
	var id uint64
	var address string
	if err := a.store.Pool().QueryRow(ctx, `
		INSERT INTO pools (chain_id, address, name, symbol, decimals)
		VALUES (42220, '0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'), $1, $2, 6)
		RETURNING id, address`, name, symbol).Scan(&id, &address); err != nil {
		t.Fatalf("seed report pool: %v", err)
	}
	return id, address, func() {
		_, _ = a.store.Pool().Exec(context.Background(), `DELETE FROM pools WHERE id = $1`, id)
	}
}

func linkReportPool(t *testing.T, a *API, reportID, poolID uint64) {
	t.Helper()
	if _, err := a.store.Pool().Exec(context.Background(),
		`INSERT INTO report_pools (report_id, pool_id) VALUES ($1, $2)`,
		reportID, poolID); err != nil {
		t.Fatalf("link report pool: %v", err)
	}
}

// seedApprovedReport commits one approved report and its direct tags, returning its id and a
// cleanup that removes it.
func seedApprovedReport(t *testing.T, a *API, title, description string, tags ...string) (uint64, func()) {
	t.Helper()
	ctx := context.Background()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}

	var id uint64
	if err := tx.QueryRow(ctx, `
		INSERT INTO reports (title, description, status, author_address, created_at, updated_at)
		VALUES ($1, $2, 'APPROVED', '0x0000000000000000000000000000000000000002', now(), now())
		RETURNING id`, title, description).Scan(&id); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("seed report: %v", err)
	}
	for _, tag := range tags {
		if _, err := tx.Exec(ctx,
			`INSERT INTO report_tags (report_id, tag_value) VALUES ($1, $2)`, id, tag); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("seed report tag %q: %v", tag, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	return id, func() {
		cleanupCtx := context.Background()
		cleanupTx, err := a.store.Pool().Begin(cleanupCtx)
		if err != nil {
			return
		}
		// report_tags cascades from reports, but deleting it explicitly keeps the cleanup
		// independent of that constraint.
		_, _ = cleanupTx.Exec(cleanupCtx, `DELETE FROM report_tags WHERE report_id = $1`, id)
		_, _ = cleanupTx.Exec(cleanupCtx, `DELETE FROM reports WHERE id = $1`, id)
		_ = cleanupTx.Commit(cleanupCtx)
	}
}

func getReportPage(t *testing.T, a *API, token string, params map[string][]string) (*httptest.ResponseRecorder, reportPagePayload) {
	t.Helper()

	values := url.Values{}
	for key, list := range params {
		for _, value := range list {
			values.Add(key, value)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v2/reports?"+values.Encode(), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, req)

	var envelope struct {
		Ok     bool              `json:"ok"`
		Result reportPagePayload `json:"result"`
	}
	if res.Code == http.StatusOK {
		if err := json.Unmarshal(res.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode report page: %v\nbody: %s", err, res.Body.String())
		}
	}
	return res, envelope.Result
}

func TestReportsEndpointFiltersByTermThroughTheAuthGate(t *testing.T) {
	a, token := newReportSearchAPI(t)
	marker := reportSearchToken + "Term"

	matchTitle, cleanTitle := seedApprovedReport(t, a, marker+" Harvest log", "unrelated")
	defer cleanTitle()
	matchDescription, cleanDescription := seedApprovedReport(t, a, "Unrelated", "about "+marker)
	defer cleanDescription()
	_, cleanOther := seedApprovedReport(t, a, "Neither", "nor")
	defer cleanOther()

	res, page := getReportPage(t, a, token, map[string][]string{"perPage": {"100"}, "q": {marker}})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}

	ids := make([]uint64, 0, len(page.Reports))
	for _, report := range page.Reports {
		ids = append(ids, report.ID)
	}
	slices.Sort(ids)
	want := []uint64{matchTitle, matchDescription}
	slices.Sort(want)
	if !slices.Equal(ids, want) {
		t.Errorf("filtered ids = %v, want the title and description matches %v", ids, want)
	}
}

func TestReportsEndpointFiltersByTagAndMatchMode(t *testing.T) {
	a, token := newReportSearchAPI(t)
	marker := reportSearchToken + "Tags"

	both, cleanBoth := seedApprovedReport(t, a, marker+" Both", "x", "Art", "Culture")
	defer cleanBoth()
	onlyArt, cleanArt := seedApprovedReport(t, a, marker+" OnlyArt", "x", "Art")
	defer cleanArt()

	ids := func(params map[string][]string) []uint64 {
		t.Helper()
		params["q"] = []string{marker}
		params["perPage"] = []string{"100"}
		res, page := getReportPage(t, a, token, params)
		if res.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
		}
		out := make([]uint64, 0, len(page.Reports))
		for _, report := range page.Reports {
			out = append(out, report.ID)
		}
		slices.Sort(out)
		return out
	}

	anyMatch := ids(map[string][]string{"tags": {"Art,Culture"}})
	wantAny := []uint64{both, onlyArt}
	slices.Sort(wantAny)
	if !slices.Equal(anyMatch, wantAny) {
		t.Errorf("any-match = %v, want %v", anyMatch, wantAny)
	}

	allMatch := ids(map[string][]string{"tags": {"Art", "Culture"}, "tagMatch": {"all"}})
	if !slices.Equal(allMatch, []uint64{both}) {
		t.Errorf("all-match = %v, want only the report carrying both tags", allMatch)
	}
}

// A filtered row carries the tag that matched it, unlike a filtered voucher row. Report tags
// are direct, and the feed already returns them.
func TestReportsEndpointReturnsTheTagsThatMatched(t *testing.T) {
	a, token := newReportSearchAPI(t)
	marker := reportSearchToken + "Rows"

	_, clean := seedApprovedReport(t, a, marker+" Row", "x", "Art", "Culture")
	defer clean()

	res, page := getReportPage(t, a, token, map[string][]string{
		"perPage": {"100"}, "q": {marker}, "tags": {"Art"},
	})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if len(page.Reports) != 1 {
		t.Fatalf("matched %d reports, want 1", len(page.Reports))
	}
	if !slices.Contains(page.Reports[0].Tags, "Art") {
		t.Errorf("row tags = %v, want the matched tag among them", page.Reports[0].Tags)
	}
}

func TestReportsEndpointFiltersAndFacetsByVoucher(t *testing.T) {
	a, token := newReportSearchAPI(t)
	marker := reportSearchToken + "Vouchers"

	firstVoucher, firstAddress, cleanFirstVoucher := seedReportVoucher(t, a, marker+" First", "RFV1")
	defer cleanFirstVoucher()
	secondVoucher, secondAddress, cleanSecondVoucher := seedReportVoucher(t, a, marker+" Second", "RFV2")
	defer cleanSecondVoucher()

	firstReport, cleanFirstReport := seedApprovedReport(t, a, marker+" First", "x")
	defer cleanFirstReport()
	bothReport, cleanBothReport := seedApprovedReport(t, a, marker+" Both", "x")
	defer cleanBothReport()
	secondReport, cleanSecondReport := seedApprovedReport(t, a, marker+" Second", "x")
	defer cleanSecondReport()
	linkReportVoucher(t, a, firstReport, firstVoucher)
	linkReportVoucher(t, a, bothReport, firstVoucher)
	linkReportVoucher(t, a, bothReport, secondVoucher)
	linkReportVoucher(t, a, secondReport, secondVoucher)

	ref := func(address string) string { return "42220:" + address }
	listIDs := func(vouchers, match string, facets bool) (reportPagePayload, []uint64) {
		t.Helper()
		params := map[string][]string{
			"perPage":  {"100"},
			"q":        {marker},
			"vouchers": {vouchers},
		}
		if match != "" {
			params["voucherMatch"] = []string{match}
		}
		if facets {
			params["facets"] = []string{"true"}
		}
		res, page := getReportPage(t, a, token, params)
		if res.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
		}
		ids := make([]uint64, 0, len(page.Reports))
		for _, report := range page.Reports {
			ids = append(ids, report.ID)
		}
		slices.Sort(ids)
		return page, ids
	}

	_, anyIDs := listIDs(ref(firstAddress)+","+ref(secondAddress), "", false)
	wantAny := []uint64{firstReport, bothReport, secondReport}
	slices.Sort(wantAny)
	if !slices.Equal(anyIDs, wantAny) {
		t.Errorf("any voucher IDs = %v, want %v", anyIDs, wantAny)
	}
	page, allIDs := listIDs(ref(firstAddress)+","+ref(secondAddress), "all", true)
	if !slices.Equal(allIDs, []uint64{bothReport}) {
		t.Errorf("all voucher IDs = %v, want [%d]", allIDs, bothReport)
	}
	if len(page.Reports) != 1 || len(page.Reports[0].Vouchers) != 2 ||
		page.Reports[0].Vouchers[0].ChainID != 42220 {
		t.Errorf("filtered report did not retain voucher identities: %+v", page.Reports)
	}
	if page.VoucherFacets == nil {
		t.Fatal("facets=true on the first page must return voucherFacets")
	}
	counts := make(map[uint64]uint64, len(*page.VoucherFacets))
	for _, facet := range *page.VoucherFacets {
		counts[facet.ID] = facet.Count
	}
	if counts[firstVoucher] != 2 || counts[secondVoucher] != 2 {
		t.Errorf("voucher facet counts = %v, want both at 2", counts)
	}
}

func TestReportsEndpointFiltersAndFacetsByPool(t *testing.T) {
	a, token := newReportSearchAPI(t)
	marker := reportSearchToken + "Pools"

	firstPool, firstAddress, cleanFirstPool := seedReportPool(t, a, marker+" First", "RFP1")
	defer cleanFirstPool()
	secondPool, secondAddress, cleanSecondPool := seedReportPool(t, a, marker+" Second", "RFP2")
	defer cleanSecondPool()

	firstReport, cleanFirstReport := seedApprovedReport(t, a, marker+" First", "x")
	defer cleanFirstReport()
	bothReport, cleanBothReport := seedApprovedReport(t, a, marker+" Both", "x")
	defer cleanBothReport()
	secondReport, cleanSecondReport := seedApprovedReport(t, a, marker+" Second", "x")
	defer cleanSecondReport()
	linkReportPool(t, a, firstReport, firstPool)
	linkReportPool(t, a, bothReport, firstPool)
	linkReportPool(t, a, bothReport, secondPool)
	linkReportPool(t, a, secondReport, secondPool)

	ref := func(address string) string { return "42220:" + address }
	request := func(match string, facets bool) (reportPagePayload, []uint64) {
		t.Helper()
		params := map[string][]string{
			"perPage": {"100"},
			"q":       {marker},
			"pools":   {ref(firstAddress) + "," + ref(secondAddress)},
		}
		if match != "" {
			params["poolMatch"] = []string{match}
		}
		if facets {
			params["facets"] = []string{"true"}
		}
		res, page := getReportPage(t, a, token, params)
		if res.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
		}
		ids := make([]uint64, 0, len(page.Reports))
		for _, report := range page.Reports {
			ids = append(ids, report.ID)
		}
		slices.Sort(ids)
		return page, ids
	}

	_, anyIDs := request("", false)
	wantAny := []uint64{firstReport, bothReport, secondReport}
	slices.Sort(wantAny)
	if !slices.Equal(anyIDs, wantAny) {
		t.Errorf("any pool IDs = %v, want %v", anyIDs, wantAny)
	}
	page, allIDs := request("all", true)
	if !slices.Equal(allIDs, []uint64{bothReport}) {
		t.Errorf("all pool IDs = %v, want [%d]", allIDs, bothReport)
	}
	if len(page.Reports) != 1 || len(page.Reports[0].Pools) != 2 ||
		page.Reports[0].Pools[0].ChainID != 42220 {
		t.Errorf("filtered report did not retain pool identities: %+v", page.Reports)
	}
	if page.PoolFacets == nil {
		t.Fatal("facets=true on the first page must return poolFacets")
	}
	counts := make(map[uint64]uint64, len(*page.PoolFacets))
	for _, facet := range *page.PoolFacets {
		counts[facet.ID] = facet.Count
	}
	if counts[firstPool] != 2 || counts[secondPool] != 2 {
		t.Errorf("pool facet counts = %v, want both at 2", counts)
	}
}

func TestReportsEndpointReturnsTagFacetsOnTheFirstPageOnly(t *testing.T) {
	a, token := newReportSearchAPI(t)
	marker := reportSearchToken + "Facets"

	for _, tags := range [][]string{{"Art"}, {"Art"}, {"Culture"}} {
		_, clean := seedApprovedReport(t, a, marker+" Facet", "x", tags...)
		defer clean()
	}

	// A tag that exists in the vocabulary but that this term cannot match, so the zero-count
	// assertion below holds on an empty database too. Relying on the imported catalog to supply
	// an unmatched tag passed locally and left the assertion vacuous on a fresh one.
	_, cleanUnmatched := seedApprovedReport(t, a, "Unmatched by the facet term", "x", "Mobility")
	defer cleanUnmatched()

	res, page := getReportPage(t, a, token, map[string][]string{
		"perPage": {"2"}, "q": {marker}, "facets": {"true"},
	})
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
	// Zero-count values are represented so a client can grey out a filter that exists.
	if counts["Mobility"] != 0 {
		t.Errorf("Mobility count = %d, want 0 — the term matches no report carrying it",
			counts["Mobility"])
	}
	if zeroes == 0 {
		t.Error("expected zero-count facet values to be represented")
	}

	// The page was short of nothing — three fixtures at perPage=2 — so a cursor is offered.
	if page.Next == nil {
		t.Fatal("a full page must offer a cursor")
	}

	// A facet describes the whole filtered set, so a cursor page omits the key entirely rather
	// than recomputing the same answer at the same cost.
	_, cursorPage := getReportPage(t, a, token, map[string][]string{
		"perPage":         {"2"},
		"q":               {marker},
		"facets":          {"true"},
		"cursorUpdatedAt": {page.Next.UpdatedAt},
		"cursorId":        {fmt.Sprint(page.Next.ID)},
	})
	if cursorPage.TagFacets != nil {
		t.Error("tagFacets must be absent from a cursor page")
	}
}

func TestReportsEndpointRejectsInvalidFilters(t *testing.T) {
	a, token := newReportSearchAPI(t)

	for name, params := range map[string]map[string][]string{
		"public author":    {"author": {reportAuthorAddress}},
		"unknown tag":      {"tags": {"NotARealTag"}},
		"one-char term":    {"q": {"a"}},
		"bad tagMatch":     {"tagMatch": {"sideways"}},
		"bad facets":       {"facets": {"maybe"}},
		"bad voucher":      {"vouchers": {"42220:not-an-address"}},
		"bad voucherMatch": {"voucherMatch": {"sideways"}},
		"unknown voucher":  {"vouchers": {"42220:0x9999999999999999999999999999999999999999"}},
		"bad pool":         {"pools": {"42220:not-an-address"}},
		"bad poolMatch":    {"poolMatch": {"sideways"}},
		"unknown pool":     {"pools": {"42220:0x9999999999999999999999999999999999999999"}},
		"too many tags":    {"tags": {"a,b,c,d,e,f,g,h,i,j,k,l,m"}},
	} {
		t.Run(name, func(t *testing.T) {
			res, _ := getReportPage(t, a, token, params)
			if res.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400\nbody: %s", res.Code, res.Body.String())
			}

			var errorBody struct {
				Ok      bool   `json:"ok"`
				Code    string `json:"code"`
				Message string `json:"description"`
			}
			if err := json.Unmarshal(res.Body.Bytes(), &errorBody); err != nil {
				t.Fatalf("decode error: %v", err)
			}
			if errorBody.Ok {
				t.Error("a rejected filter must not report ok")
			}
			// Every validation failure shares one code, which is why the client cannot branch on
			// it and keys on the HTTP status instead.
			if errorBody.Code != "E04" {
				t.Errorf("code = %q, want E04", errorBody.Code)
			}
		})
	}
}

// An unfiltered request must reach the untouched static statements and return the feed's
// existing shape, with no facet key.
func TestReportsEndpointLeavesTheUnfilteredFeedUnchanged(t *testing.T) {
	a, token := newReportSearchAPI(t)

	res, page := getReportPage(t, a, token, map[string][]string{"perPage": {"5"}})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if page.TagFacets != nil {
		t.Error("an unfiltered request must not carry tagFacets")
	}
	if len(page.Reports) == 0 {
		t.Skip("the local feed is empty; nothing to assert about its shape")
	}
}
