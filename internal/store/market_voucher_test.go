package store

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestLoadMarketVoucherQueries(t *testing.T) {
	queries, err := loadQueries("../../core_queries.sql")
	if err != nil {
		t.Fatalf("load core queries: %v", err)
	}

	unbounded := map[string]string{
		"list-market-vouchers":      queries.ListMarketVouchers,
		"list-market-vouchers-next": queries.ListMarketVouchersNext,
		"list-market-vouchers-prev": queries.ListMarketVouchersPrev,
	}
	bounded := map[string]string{
		"list-market-vouchers-bounded":      queries.ListMarketVouchersBounded,
		"list-market-vouchers-next-bounded": queries.ListMarketVouchersNextBounded,
		"list-market-vouchers-prev-bounded": queries.ListMarketVouchersPrevBounded,
	}

	for name, query := range unbounded {
		if query == "" {
			t.Errorf("expected %s query to be loaded", name)
		}
		if strings.Contains(query, "<@") {
			t.Errorf("%s must not carry the bbox predicate", name)
		}
	}

	for name, query := range bounded {
		if query == "" {
			t.Errorf("expected %s query to be loaded", name)
			continue
		}
		// Core PostgreSQL GiST point_ops; PostGIS is deliberately not required.
		if !strings.Contains(query, "point(v.geo_lng, v.geo_lat) <@") {
			t.Errorf("%s must filter with the core point <@ box operator", name)
		}
		if !strings.Contains(query, "box(point(") {
			t.Errorf("%s must build the bounding box with box(point(...))", name)
		}
	}

	all := map[string]string{}
	for name, query := range unbounded {
		all[name] = query
	}
	for name, query := range bounded {
		all[name] = query
	}

	for name, query := range all {
		if !strings.Contains(query, "v.active") {
			t.Errorf("%s must return active vouchers only", name)
		}
		if !strings.Contains(query, "v.geo_lat IS NOT NULL") {
			t.Errorf("%s must require complete coordinates", name)
		}
		// `internal` is an import artifact, not a visibility flag (MIGRATION.md 4.7).
		if strings.Contains(query, "internal") {
			t.Errorf("%s must not filter on the internal import artifact", name)
		}
		if !strings.Contains(query, "o.available") {
			t.Errorf("%s must derive kind from available offerings only", name)
		}
		// Ties between GOOD and SERVICE resolve to goods via the ASC tiebreak.
		if !strings.Contains(query, "ORDER BY COUNT(*) DESC, o.commodity_type ASC") {
			t.Errorf("%s must break a GOOD/SERVICE count tie toward GOOD", name)
		}
		if !strings.Contains(query, "AS MATERIALIZED") {
			t.Errorf("%s must limit the voucher page before deriving kinds", name)
		}
	}
}

func TestLoadMarketVoucherDetailQuery(t *testing.T) {
	queries, err := loadQueries("../../core_queries.sql")
	if err != nil {
		t.Fatalf("load core queries: %v", err)
	}

	query := queries.GetMarketVoucherByAddress
	if query == "" {
		t.Fatal("expected get-market-voucher-by-address query to be loaded")
	}
	if !strings.Contains(query, "lower(v.address) = lower($1)") {
		t.Error("detail lookup must be case-insensitive; a pasted link may be lowercased")
	}
	if !strings.Contains(query, "v.active") {
		t.Error("detail must apply the same visibility rule as the list")
	}
	// Unlike the list, coordinates are optional: a voucher without them still
	// has a readable page, it simply renders no map.
	if strings.Contains(query, "v.geo_lat IS NOT NULL") {
		t.Error("detail must not require coordinates")
	}
	// NUMERIC would arrive as a float and lose precision; the client formats text.
	if !strings.Contains(query, "v.voucher_value::TEXT") {
		t.Error("voucher_value must cross the wire as text, not a float")
	}
	if strings.Contains(query, "internal") {
		t.Error("detail must not read the internal import artifact")
	}
	for _, column := range []string{"v.icon_url", "v.banner_url", "v.email", "v.owner_address", "v.voucher_uoa"} {
		if !strings.Contains(query, column) {
			t.Errorf("detail must select %s, which the card or page renders", column)
		}
	}
}

func TestMarketVoucherBoundsValidate(t *testing.T) {
	nairobi := MarketVoucherBounds{MinLng: 36.6, MinLat: -1.5, MaxLng: 37.1, MaxLat: -1.1}
	if err := nairobi.Validate(); err != nil {
		t.Fatalf("valid bounds rejected: %v", err)
	}

	// A degenerate point box is a legitimate zero-area viewport.
	point := MarketVoucherBounds{MinLng: 36.8, MinLat: -1.3, MaxLng: 36.8, MaxLat: -1.3}
	if err := point.Validate(); err != nil {
		t.Fatalf("degenerate bounds rejected: %v", err)
	}

	for name, bounds := range map[string]MarketVoucherBounds{
		// A single box may never cross the antimeridian; callers split instead.
		"inverted longitude": {MinLng: 37.1, MinLat: -1.5, MaxLng: 36.6, MaxLat: -1.1},
		"inverted latitude":  {MinLng: 36.6, MinLat: -1.1, MaxLng: 37.1, MaxLat: -1.5},
		"latitude above 90":  {MinLng: 0, MinLat: 0, MaxLng: 1, MaxLat: 90.1},
		"latitude below -90": {MinLng: 0, MinLat: -90.1, MaxLng: 1, MaxLat: 0},
		"longitude above 180": {
			MinLng: 0, MinLat: 0, MaxLng: 180.1, MaxLat: 1,
		},
		"longitude below -180": {
			MinLng: -180.1, MinLat: 0, MaxLng: 0, MaxLat: 1,
		},
		"NaN": {MinLng: math.NaN(), MinLat: 0, MaxLng: 1, MaxLat: 1},
		"Inf": {MinLng: 0, MinLat: 0, MaxLng: math.Inf(1), MaxLat: 1},
	} {
		if err := bounds.Validate(); err == nil {
			t.Errorf("expected %s bounds to be rejected", name)
		}
	}
}

func TestMarketVoucherJSONContract(t *testing.T) {
	name := "Priscilah Malii"
	symbol := "VOUCHER"
	location := "C96, KEN"
	encoded, err := json.Marshal(MarketVoucher{
		ID:           2710,
		ChainID:      42220,
		Address:      "0xAbC0000000000000000000000000000000000123",
		Name:         &name,
		Symbol:       &symbol,
		LocationName: &location,
		GeoLat:       -1.450979,
		GeoLng:       38.235115,
		Kind:         "voucher",
	})
	if err != nil {
		t.Fatalf("marshal market voucher: %v", err)
	}

	payload := string(encoded)
	for _, field := range []string{
		`"id":2710`, `"chainId":42220`, `"name":"Priscilah Malii"`,
		`"symbol":"VOUCHER"`, `"locationName":"C96, KEN"`,
		`"geoLat":-1.450979`, `"geoLng":38.235115`, `"kind":"voucher"`,
	} {
		if !strings.Contains(payload, field) {
			t.Errorf("expected market voucher JSON to contain %s, got %s", field, payload)
		}
	}
	// Checksummed storage casing must survive to the client verbatim.
	if !strings.Contains(payload, `"address":"0xAbC0000000000000000000000000000000000123"`) {
		t.Errorf("expected checksummed address to be preserved, got %s", payload)
	}
	// `internal` has no market visibility semantics and is not part of the contract.
	if strings.Contains(payload, "internal") {
		t.Errorf("market voucher must not expose the internal import artifact: %s", payload)
	}
	// Distance is presentation state relative to optional browser geolocation.
	if strings.Contains(payload, "distance") {
		t.Errorf("market voucher must not carry a server-calculated distance: %s", payload)
	}
}

func TestMarketVoucherDetailJSONContract(t *testing.T) {
	text := func(value string) *string { return &value }
	decimals := 6
	lat, lng := -1.450979, 38.235115

	encoded, err := json.Marshal(MarketVoucherDetail{
		ID:           2710,
		ChainID:      42220,
		Address:      "0xAbC0000000000000000000000000000000000123",
		Name:         text("Priscilah Malii"),
		Symbol:       text("MALII"),
		Decimals:     &decimals,
		OwnerAddress: text("0xDeF0000000000000000000000000000000000456"),
		Description:  text("Community voucher"),
		VoucherUOA:   text("KSH"),
		VoucherValue: text("10"),
		LocationName: text("C96, KEN"),
		GeoLat:       &lat,
		GeoLng:       &lng,
		Email:        text("issuer@example.org"),
		Website:      text("https://example.org"),
		Phone:        text("+254700000000"),
		IconURL:      text("https://content.example/icon.jpg"),
		BannerURL:    text("https://content.example/banner.jpg"),
		Kind:         "goods",
	})
	if err != nil {
		t.Fatalf("marshal voucher detail: %v", err)
	}

	payload := string(encoded)
	// Exactly the keys the card and the voucher page read.
	for _, field := range []string{
		`"id":2710`, `"chainId":42220`, `"name":"Priscilah Malii"`, `"symbol":"MALII"`,
		`"decimals":6`, `"ownerAddress":"0xDeF0000000000000000000000000000000000456"`,
		`"voucherUoa":"KSH"`, `"voucherValue":"10"`, `"locationName":"C96, KEN"`,
		`"geoLat":-1.450979`, `"geoLng":38.235115`, `"email":"issuer@example.org"`,
		`"website":"https://example.org"`, `"phone":"+254700000000"`,
		`"iconUrl":"https://content.example/icon.jpg"`,
		`"bannerUrl":"https://content.example/banner.jpg"`, `"kind":"goods"`,
	} {
		if !strings.Contains(payload, field) {
			t.Errorf("expected voucher detail JSON to contain %s, got %s", field, payload)
		}
	}
	if !strings.Contains(payload, `"address":"0xAbC0000000000000000000000000000000000123"`) {
		t.Errorf("expected checksummed address to be preserved, got %s", payload)
	}
	// voucherValue is text so a NUMERIC like 10.50 keeps its scale.
	if strings.Contains(payload, `"voucherValue":10`) {
		t.Errorf("voucherValue must serialize as a string, got %s", payload)
	}
	if strings.Contains(payload, "internal") {
		t.Errorf("voucher detail must not expose the internal import artifact: %s", payload)
	}
}

// A voucher with no images, no contact details, and no coordinates is the
// common case — roughly two thirds of the imported catalog has no icon. Those
// fields must arrive as explicit nulls the client can branch on, never as
// empty strings or zeroed coordinates off the Gulf of Guinea.
func TestMarketVoucherDetailSerializesMissingFieldsAsNull(t *testing.T) {
	encoded, err := json.Marshal(MarketVoucherDetail{
		ID:      2710,
		ChainID: 42220,
		Address: "0xAbC0000000000000000000000000000000000123",
		Kind:    "voucher",
	})
	if err != nil {
		t.Fatalf("marshal voucher detail: %v", err)
	}

	payload := string(encoded)
	for _, field := range []string{
		`"iconUrl":null`, `"bannerUrl":null`, `"email":null`, `"ownerAddress":null`,
		`"voucherUoa":null`, `"voucherValue":null`, `"geoLat":null`, `"geoLng":null`,
	} {
		if !strings.Contains(payload, field) {
			t.Errorf("expected %s, got %s", field, payload)
		}
	}
	if strings.Contains(payload, `"geoLat":0`) {
		t.Errorf("an absent coordinate must not serialize as 0: %s", payload)
	}
}
