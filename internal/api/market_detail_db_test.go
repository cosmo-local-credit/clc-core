package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Public voucher detail, end to end: real route table, real middleware chain,
// real store, real database. Skipped unless CORE_TEST_DSN is set.
//
//	CORE_TEST_DSN=postgres://postgres:postgres@127.0.0.1:5432/clc_core go test ./internal/api/

type (
	marketVoucherDetailPayload struct {
		ID           uint64   `json:"id"`
		ChainID      int64    `json:"chainId"`
		Address      string   `json:"address"`
		Name         *string  `json:"name"`
		Symbol       *string  `json:"symbol"`
		Decimals     *int     `json:"decimals"`
		OwnerAddress *string  `json:"ownerAddress"`
		Description  *string  `json:"description"`
		VoucherUoa   *string  `json:"voucherUoa"`
		VoucherValue *string  `json:"voucherValue"`
		LocationName *string  `json:"locationName"`
		GeoLat       *float64 `json:"geoLat"`
		GeoLng       *float64 `json:"geoLng"`
		Email        *string  `json:"email"`
		Website      *string  `json:"website"`
		Phone        *string  `json:"phone"`
		IconURL      *string  `json:"iconUrl"`
		BannerURL    *string  `json:"bannerUrl"`
		Kind         string   `json:"kind"`
	}
)

func getMarketVoucherDetail(t *testing.T, a *API, address, remoteAddr string) (*httptest.ResponseRecorder, marketVoucherDetailPayload) {
	t.Helper()

	target := "/api/v2/vouchers/" + address
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = remoteAddr
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("GET %s: status = %d, body = %s", target, res.Code, res.Body.String())
	}

	var envelope struct {
		Ok     bool `json:"ok"`
		Result struct {
			Voucher marketVoucherDetailPayload `json:"voucher"`
		} `json:"result"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode %s: %v", target, err)
	}
	if !envelope.Ok {
		t.Fatalf("GET %s returned ok=false", target)
	}
	return res, envelope.Result.Voucher
}

// The card and the voucher page are built entirely from this response, so the
// fields they render must survive the round trip for a row the catalog listed.
func TestGuestReceivesVoucherDetailForACatalogAddress(t *testing.T) {
	a := newMarketAPIWithStore(t)

	_, page := getMarketPage(t, a, "/api/v2/vouchers?perPage=1", "198.51.100.60:1234")
	if len(page.Vouchers) == 0 {
		t.Skip("no catalog rows to look up")
	}
	listed := page.Vouchers[0]

	// No cookie, no Authorization header — exactly what the Market tab sends.
	_, detail := getMarketVoucherDetail(t, a, listed.Address, "198.51.100.60:1234")

	if detail.ID != listed.ID {
		t.Errorf("id = %d, want the listed voucher %d", detail.ID, listed.ID)
	}
	if detail.Address != listed.Address {
		t.Errorf("address = %q, want %q", detail.Address, listed.Address)
	}
	if detail.Kind != listed.Kind {
		t.Errorf("kind = %q, want the same derivation as the list (%q)", detail.Kind, listed.Kind)
	}
	if detail.ChainID == 0 {
		t.Error("detail is missing chainId")
	}
	if detail.VoucherUoa == nil {
		t.Error("detail is missing voucherUoa, which the rate line needs")
	}
	if detail.VoucherValue == nil {
		t.Error("detail is missing voucherValue, which the rate line needs")
	}
}

// A deactivated voucher must be indistinguishable from one that never existed,
// otherwise the 404 body becomes an oracle for "this address is in our
// database but withdrawn".
func TestDeactivatedVoucherIsNotFound(t *testing.T) {
	a := newMarketAPIWithStore(t)

	var inactive string
	row := a.store.Pool().QueryRow(t.Context(),
		"SELECT address FROM vouchers WHERE NOT active LIMIT 1")
	if err := row.Scan(&inactive); err != nil {
		t.Skipf("no inactive voucher to test against: %v", err)
	}

	unknown := requestVoucherDetail(t, a, "0x000000000000000000000000000000000000dEaD", "198.51.100.61:1234")
	withdrawn := requestVoucherDetail(t, a, inactive, "198.51.100.61:1234")

	if withdrawn.Code != http.StatusNotFound {
		t.Fatalf("inactive voucher status = %d, want 404", withdrawn.Code)
	}
	if withdrawn.Body.String() != unknown.Body.String() {
		t.Errorf("an inactive voucher must answer exactly like an unknown one:\n inactive = %s\n unknown  = %s",
			withdrawn.Body.String(), unknown.Body.String())
	}
}

// Addresses reach this endpoint from wherever a link was pasted, so casing is
// not something the client can be trusted to preserve. The response keeps the
// stored checksummed form regardless of how it was asked for.
func TestVoucherDetailIgnoresAddressCasing(t *testing.T) {
	a := newMarketAPIWithStore(t)

	_, page := getMarketPage(t, a, "/api/v2/vouchers?perPage=1", "198.51.100.62:1234")
	if len(page.Vouchers) == 0 {
		t.Skip("no catalog rows to look up")
	}
	stored := page.Vouchers[0].Address

	_, lowered := getMarketVoucherDetail(t, a, strings.ToLower(stored), "198.51.100.62:1234")
	_, uppered := getMarketVoucherDetail(t, a, "0x"+strings.ToUpper(strings.TrimPrefix(stored, "0x")), "198.51.100.62:1234")

	if lowered.ID != page.Vouchers[0].ID || uppered.ID != page.Vouchers[0].ID {
		t.Fatalf("ids = %d and %d, want %d for either casing", lowered.ID, uppered.ID, page.Vouchers[0].ID)
	}
	if lowered.Address != stored || uppered.Address != stored {
		t.Errorf("addresses = %q and %q, want the stored casing %q", lowered.Address, uppered.Address, stored)
	}
}

func requestVoucherDetail(t *testing.T, a *API, address, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/api/v2/vouchers/"+address, nil)
	req.RemoteAddr = remoteAddr
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, req)
	return res
}
