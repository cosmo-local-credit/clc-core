package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type (
	marketOfferingPayload struct {
		ID             uint64 `json:"id"`
		VoucherAddress string `json:"voucherAddress"`
		Available      bool   `json:"available"`
	}

	marketOfferingPageResult struct {
		Offerings []marketOfferingPayload `json:"offerings"`
		First     *uint64                 `json:"first"`
		Last      *uint64                 `json:"last"`
	}
)

func getMarketOfferingPage(t *testing.T, a *API, address, remoteAddr string) (*httptest.ResponseRecorder, marketOfferingPageResult) {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/api/v2/vouchers/"+address+"/offerings?perPage=20", nil)
	req.RemoteAddr = remoteAddr
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("voucher offerings status = %d, body = %s", res.Code, res.Body.String())
	}

	var envelope struct {
		Ok          bool                     `json:"ok"`
		Description string                   `json:"description"`
		Result      marketOfferingPageResult `json:"result"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode voucher offerings: %v", err)
	}
	if !envelope.Ok {
		t.Fatal("voucher offerings returned ok=false")
	}
	return res, envelope.Result
}

func TestGuestReceivesAvailableVoucherOfferings(t *testing.T) {
	a := newMarketAPIWithStore(t)

	var address string
	err := a.store.Pool().QueryRow(t.Context(), `
		SELECT v.address
		FROM vouchers v
		WHERE v.active
		  AND EXISTS (
		      SELECT 1 FROM offerings o
		      WHERE o.voucher_id = v.id AND o.available
		  )
		LIMIT 1`).Scan(&address)
	if err != nil {
		t.Skipf("no active voucher with offerings: %v", err)
	}

	// No cookie or Authorization header.
	res, page := getMarketOfferingPage(t, a, address, "198.51.100.81:1234")
	if len(page.Offerings) == 0 {
		t.Fatal("expected the selected voucher to return offerings")
	}
	if page.First == nil || page.Last == nil {
		t.Fatal("first and last must be numbers, never null")
	}
	for _, offering := range page.Offerings {
		if !offering.Available {
			t.Errorf("offering %d is unavailable", offering.ID)
		}
		if !strings.EqualFold(offering.VoucherAddress, address) {
			t.Errorf("offering %d voucher = %q, want %q", offering.ID, offering.VoucherAddress, address)
		}
	}
	if got := res.Header().Get("Cache-Control"); got != "public, max-age=60, stale-while-revalidate=300" {
		t.Errorf("Cache-Control = %q", got)
	}
	if vary := strings.Join(res.Header().Values("Vary"), ", "); !strings.Contains(vary, "Origin") {
		t.Errorf("Vary = %q, want Origin", vary)
	}
}

func TestVoucherWithNoOfferingsSerializesAnEmptyPage(t *testing.T) {
	a := newMarketAPIWithStore(t)

	var address string
	err := a.store.Pool().QueryRow(t.Context(), `
		SELECT v.address
		FROM vouchers v
		WHERE v.active
		  AND NOT EXISTS (
		      SELECT 1 FROM offerings o
		      WHERE o.voucher_id = v.id AND o.available
		  )
		LIMIT 1`).Scan(&address)
	if err != nil {
		t.Skipf("no active voucher without offerings: %v", err)
	}

	res, page := getMarketOfferingPage(t, a, address, "198.51.100.82:1234")
	if len(page.Offerings) != 0 {
		t.Fatalf("expected an empty page, got %d offerings", len(page.Offerings))
	}
	if page.First == nil || page.Last == nil || *page.First != 0 || *page.Last != 0 {
		t.Error("empty page must report numeric first=0 and last=0")
	}
	if !strings.Contains(res.Body.String(), `"offerings":[]`) {
		t.Errorf("empty offerings must serialize as [], never null: %s", res.Body.String())
	}
}
