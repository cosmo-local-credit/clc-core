package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
)

// End-to-end coverage of the public catalog: real route table, real middleware
// chain, real store, real database. Skipped unless CORE_TEST_DSN is set.
//
//	CORE_TEST_DSN=postgres://postgres:postgres@127.0.0.1:5432/clc_core go test ./internal/api/
func newMarketAPIWithStore(t *testing.T) *API {
	t.Helper()

	dsn := os.Getenv("CORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CORE_TEST_DSN not set; skipping live market endpoint tests")
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

	return New(APIOpts{
		Logg:  logg,
		CORS:  []string{"https://app.example"},
		Store: pgStore,
	})
}

type (
	marketVoucherPayload struct {
		ID           uint64   `json:"id"`
		ChainID      int64    `json:"chainId"`
		Address      string   `json:"address"`
		Name         *string  `json:"name"`
		Symbol       *string  `json:"symbol"`
		IconURL      *string  `json:"iconUrl"`
		LocationName *string  `json:"locationName"`
		GeoLat       *float64 `json:"geoLat"`
		GeoLng       *float64 `json:"geoLng"`
		Kind         string   `json:"kind"`
	}

	marketPageResult struct {
		Vouchers []marketVoucherPayload `json:"vouchers"`
		First    *uint64                `json:"first"`
		Last     *uint64                `json:"last"`
	}
)

func getMarketPage(t *testing.T, a *API, target, remoteAddr string) (*httptest.ResponseRecorder, marketPageResult) {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = remoteAddr
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("GET %s: status = %d, body = %s", target, res.Code, res.Body.String())
	}

	var envelope struct {
		Ok          bool             `json:"ok"`
		Description string           `json:"description"`
		Result      marketPageResult `json:"result"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode %s: %v", target, err)
	}
	if !envelope.Ok {
		t.Fatalf("GET %s returned ok=false", target)
	}
	if envelope.Description != "Market vouchers" {
		t.Errorf("description = %q, want %q", envelope.Description, "Market vouchers")
	}
	return res, envelope.Result
}

func TestGuestReceivesTheCatalogWithoutCredentials(t *testing.T) {
	a := newMarketAPIWithStore(t)

	// One visible voucher of its own, so the assertion below holds on a database that has no
	// imported catalog. Relying on the import passed locally and failed on CI's fresh database.
	_, clean := seedSearchFixtures(t, a, "GuestCatalogFixture", []searchFixture{
		{name: "Guest Catalog Fixture", symbol: "GCF"},
	})
	defer clean()

	// No cookie, no Authorization header — exactly what the Market tab sends.
	res, page := getMarketPage(t, a, "/api/v2/vouchers?perPage=25", "198.51.100.40:1234")

	if len(page.Vouchers) == 0 {
		t.Fatal("expected the catalog to return rows")
	}
	if page.First == nil || page.Last == nil {
		t.Fatal("first and last must always be numbers, never null")
	}
	if *page.First < *page.Last {
		t.Errorf("first (%d) must be >= last (%d) on a descending page", *page.First, *page.Last)
	}

	if got := res.Header().Get("Cache-Control"); got != "public, max-age=60, stale-while-revalidate=300" {
		t.Errorf("Cache-Control = %q", got)
	}
	if vary := strings.Join(res.Header().Values("Vary"), ", "); !strings.Contains(vary, "Origin") {
		t.Errorf("Vary = %q, want Origin", vary)
	}

	for _, v := range page.Vouchers {
		if v.GeoLat == nil || v.GeoLng == nil {
			t.Fatalf("voucher %d has incomplete coordinates", v.ID)
		}
		if v.Kind != "goods" && v.Kind != "services" && v.Kind != "voucher" {
			t.Errorf("voucher %d has unexpected kind %q", v.ID, v.Kind)
		}
		if v.ChainID == 0 {
			t.Errorf("voucher %d is missing chainId", v.ID)
		}
	}

	// The import artifact must not reach the wire under any name.
	if strings.Contains(res.Body.String(), "internal") {
		t.Error("the market response must not expose the internal import artifact")
	}
}

func TestCatalogPerPageIsCappedAtFiveHundred(t *testing.T) {
	a := newMarketAPIWithStore(t)

	_, capped := getMarketPage(t, a, "/api/v2/vouchers?perPage=100000", "198.51.100.41:1234")
	if len(capped.Vouchers) > marketMaxPerPage {
		t.Fatalf("returned %d rows, want at most %d", len(capped.Vouchers), marketMaxPerPage)
	}

	_, defaulted := getMarketPage(t, a, "/api/v2/vouchers?perPage=0", "198.51.100.42:1234")
	if len(defaulted.Vouchers) > marketMaxPerPage {
		t.Fatalf("perPage=0 returned %d rows, want the %d default", len(defaulted.Vouchers), marketMaxPerPage)
	}
}

func TestCatalogNextPageWalksDownTheCursor(t *testing.T) {
	a := newMarketAPIWithStore(t)

	_, first := getMarketPage(t, a, "/api/v2/vouchers?perPage=10", "198.51.100.43:1234")
	if len(first.Vouchers) < 10 {
		t.Skip("not enough catalog rows to page")
	}

	_, next := getMarketPage(t, a,
		"/api/v2/vouchers?perPage=10&next=true&cursor="+strconv.FormatUint(*first.Last, 10), "198.51.100.43:1234")
	if len(next.Vouchers) == 0 {
		t.Fatal("expected a second page")
	}
	if next.Vouchers[0].ID >= *first.Last {
		t.Errorf("next page starts at %d, want strictly below the cursor %d", next.Vouchers[0].ID, *first.Last)
	}
	if *next.Last >= *first.Last {
		t.Errorf("next page last = %d must strictly decrease from %d", *next.Last, *first.Last)
	}
}

func TestCatalogEmptyPageSerializesAsAnArray(t *testing.T) {
	a := newMarketAPIWithStore(t)

	// A tiny box in the open Southern Ocean matches nothing.
	res, page := getMarketPage(t, a,
		"/api/v2/vouchers?perPage=25&bbox=-179.9,-89.9,-179.8,-89.8", "198.51.100.44:1234")

	if len(page.Vouchers) != 0 {
		t.Fatalf("expected an empty page, got %d rows", len(page.Vouchers))
	}
	if page.First == nil || page.Last == nil || *page.First != 0 || *page.Last != 0 {
		t.Errorf("empty page must report first=0 and last=0 as numbers")
	}
	if !strings.Contains(res.Body.String(), `"vouchers":[]`) {
		t.Errorf("empty results must serialize as [], never null: %s", res.Body.String())
	}
}

func TestCatalogBboxNarrowsTheResultSet(t *testing.T) {
	a := newMarketAPIWithStore(t)

	_, world := getMarketPage(t, a, "/api/v2/vouchers?perPage=500", "198.51.100.45:1234")
	_, nairobi := getMarketPage(t, a,
		"/api/v2/vouchers?perPage=500&bbox=36.6,-1.5,37.1,-1.1", "198.51.100.45:1234")

	if len(nairobi.Vouchers) == 0 {
		t.Skip("no vouchers inside the Nairobi viewport")
	}
	if len(nairobi.Vouchers) >= len(world.Vouchers) {
		t.Errorf("bounded page (%d) should be smaller than the unbounded page (%d)",
			len(nairobi.Vouchers), len(world.Vouchers))
	}
	for _, v := range nairobi.Vouchers {
		if *v.GeoLng < 36.6 || *v.GeoLng > 37.1 || *v.GeoLat < -1.5 || *v.GeoLat > -1.1 {
			t.Errorf("voucher %d at (%f, %f) is outside the requested box", v.ID, *v.GeoLng, *v.GeoLat)
		}
	}
}

func TestCatalogRejectsAMalformedBboxBeforeQuerying(t *testing.T) {
	a := newMarketAPIWithStore(t)

	for _, raw := range []string{"nope", "1,2,3", "36.6,-91,37.1,-1.1", "179,-1.5,-179,-1.1"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v2/vouchers?bbox="+raw, nil)
		req.RemoteAddr = "198.51.100.46:1234"
		res := httptest.NewRecorder()
		a.router.ServeHTTP(res, req)

		if res.Code != http.StatusBadRequest {
			t.Errorf("bbox=%q: status = %d, want 400", raw, res.Code)
			continue
		}
		var body apiresp.ErrResponse
		if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
			t.Errorf("bbox=%q: expected a JSON error: %v", raw, err)
			continue
		}
		if body.ErrCode != apiresp.ErrCodeValidationFailed {
			t.Errorf("bbox=%q: code = %q, want %q", raw, body.ErrCode, apiresp.ErrCodeValidationFailed)
		}
	}
}
