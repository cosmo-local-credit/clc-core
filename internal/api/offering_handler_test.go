package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/labstack/echo/v5"
)

const (
	ownerAddress = "0x11a573149db22e759fB9A38Bcc461C12855c2600"
)

// Authority follows the voucher relationship, not offerings.seller_address.
// Casing is irrelevant because the middleware checksums a claim while imported
// catalog rows are lower-case.
func TestCanWriteVoucherOfferings(t *testing.T) {
	for name, tc := range map[string]struct {
		callerAddress string
		callerRole    string
		ownerAddress  *string
		want          bool
	}{
		"owner exact":            {ownerAddress, "USER", new(ownerAddress), true},
		"owner lowercased":       {ownerAddress, "USER", new(strings.ToLower(ownerAddress)), true},
		"owner uppercase caller": {strings.ToUpper(ownerAddress), "USER", new(ownerAddress), true},
		"different user":         {otherAddress, "USER", new(ownerAddress), false},
		"superadmin any voucher": {otherAddress, "SUPERADMIN", new(ownerAddress), true},
		"superadmin ownerless":   {otherAddress, "SUPERADMIN", nil, true},
		"user ownerless":         {ownerAddress, "USER", nil, false},
		"no caller address":      {"", "USER", new(ownerAddress), false},
		// An empty owner string is not a match for an empty caller: two unknowns
		// are not the same person.
		"empty owner string": {"", "USER", new(""), false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := canWriteVoucherOfferings(tc.callerAddress, tc.callerRole, tc.ownerAddress); got != tc.want {
				t.Fatalf("canWriteVoucherOfferings() = %v, want %v", got, tc.want)
			}
		})
	}
}

// The seller is always server-selected. A caller has no field for it, and the
// value never comes from the caller unless the caller is the superadmin
// publishing for a voucher that has no owner to attribute it to.
func TestOfferingSeller(t *testing.T) {
	for name, tc := range map[string]struct {
		callerAddress string
		callerRole    string
		ownerAddress  *string
		wantSeller    string
		wantOK        bool
	}{
		"owner creates for self": {
			ownerAddress, "USER", new(ownerAddress), ownerAddress, true,
		},
		// The voucher owner, not the superadmin: a normal create must not rewrite
		// provenance just because an operator performed it.
		"superadmin creates for an owned voucher": {
			otherAddress, "SUPERADMIN", new(ownerAddress), ownerAddress, true,
		},
		"superadmin creates for an ownerless voucher": {
			otherAddress, "SUPERADMIN", nil, otherAddress, true,
		},
		"user creates for an ownerless voucher": {
			ownerAddress, "USER", nil, "", false,
		},
		"stranger creates for an owned voucher": {
			otherAddress, "USER", new(ownerAddress), "", false,
		},
		// seller_address is format-checked NOT NULL, so a superadmin session with
		// no usable address cannot create rather than writing a junk value.
		"superadmin with no address": {
			"", "SUPERADMIN", nil, "", false,
		},
		"superadmin with a malformed address": {
			"not-an-address", "SUPERADMIN", nil, "", false,
		},
		// An owner recorded as the empty string is no owner at all.
		"empty owner falls through to the superadmin path": {
			otherAddress, "SUPERADMIN", new(""), otherAddress, true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			seller, ok := offeringSeller(tc.callerAddress, tc.callerRole, tc.ownerAddress)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if seller != tc.wantSeller {
				t.Fatalf("seller = %q, want %q", seller, tc.wantSeller)
			}
		})
	}
}

func bindOffering(t *testing.T, body string) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	a := New(APIOpts{Logg: slog.New(slog.DiscardHandler)})
	req := httptest.NewRequest(http.MethodPost, "/api/v2/vouchers/"+ownerAddress+"/offerings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	ctx := a.router.NewContext(req, res)
	_, handled, err := a.bindOfferingWrite(ctx)
	if err != nil && !handled {
		t.Fatalf("bindOfferingWrite() error = %v", err)
	}
	return res, handled
}

func validOfferingBody(overrides map[string]any) string {
	body := map[string]any{
		"commodityName": "Fresh maize flour",
		"description":   "Stone-ground flour packed on collection day.",
		"unit":          "kg",
		"price":         "120.50",
	}
	for k, v := range overrides {
		if v == nil {
			delete(body, k)
			continue
		}
		body[k] = v
	}
	encoded, _ := json.Marshal(body)
	return string(encoded)
}

func TestBindOfferingWriteRejects(t *testing.T) {
	for name, body := range map[string]string{
		"blank name after trimming": validOfferingBody(map[string]any{"commodityName": "   "}),
		"blank description":         validOfferingBody(map[string]any{"description": "\t\n "}),
		"missing name":              validOfferingBody(map[string]any{"commodityName": nil}),
		"over-long name":            validOfferingBody(map[string]any{"commodityName": strings.Repeat("a", 121)}),
		"over-long description":     validOfferingBody(map[string]any{"description": strings.Repeat("a", 2001)}),
		"missing unit":              validOfferingBody(map[string]any{"unit": nil}),
		"blank unit":                validOfferingBody(map[string]any{"unit": "  "}),
		"exponent price":            validOfferingBody(map[string]any{"price": "1e3"}),
		"separator price":           validOfferingBody(map[string]any{"price": "1,000.50"}),
		"underscore price":          validOfferingBody(map[string]any{"price": "1_000"}),
		"signed price":              validOfferingBody(map[string]any{"price": "+120.50"}),
		"negative price":            validOfferingBody(map[string]any{"price": "-1"}),
		"zero price":                validOfferingBody(map[string]any{"price": "0"}),
		"zero decimal price":        validOfferingBody(map[string]any{"price": "0.00"}),
		"non-numeric price":         validOfferingBody(map[string]any{"price": "free"}),
		"trailing dot price":        validOfferingBody(map[string]any{"price": "120."}),
		"missing price":             validOfferingBody(map[string]any{"price": nil}),
		"data uri image":            validOfferingBody(map[string]any{"imageUrl": "data:image/png;base64,AAAA"}),
		"file scheme image":         validOfferingBody(map[string]any{"imageUrl": "file:///etc/passwd"}),
		"scheme-relative image":     validOfferingBody(map[string]any{"imageUrl": "//content.example/a.webp"}),
		"javascript scheme image":   validOfferingBody(map[string]any{"imageUrl": "javascript:alert(1)"}),
	} {
		t.Run(name, func(t *testing.T) {
			res, handled := bindOffering(t, body)
			if !handled {
				t.Fatalf("body accepted, want rejection: %s", body)
			}
			if res.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 body=%s", res.Code, res.Body.String())
			}
			var envelope apiresp.ErrResponse
			if err := json.Unmarshal(res.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if envelope.ErrCode != apiresp.ErrCodeValidationFailed && envelope.ErrCode != apiresp.ErrCodeInvalidJSON {
				t.Fatalf("code = %q, want a validation code", envelope.ErrCode)
			}
		})
	}
}

// The accepted shape, and what normalization does to it.
func TestBindOfferingWriteNormalizes(t *testing.T) {
	a := New(APIOpts{Logg: slog.New(slog.DiscardHandler)})

	body := validOfferingBody(map[string]any{
		"commodityName": "  Fresh maize flour  ",
		"description":   "  Stone-ground.  ",
		"unit":          " KG ",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v2/vouchers/"+ownerAddress+"/offerings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx := a.router.NewContext(req, httptest.NewRecorder())

	write, handled, err := a.bindOfferingWrite(ctx)
	if handled || err != nil {
		t.Fatalf("valid body rejected: %s", body)
	}
	if write.CommodityName != "Fresh maize flour" {
		t.Errorf("name = %q, want it trimmed", write.CommodityName)
	}
	if write.Description != "Stone-ground." {
		t.Errorf("description = %q, want it trimmed", write.Description)
	}
	// Binding trims only. The transaction-backed vocabulary resolver later maps
	// this case-insensitively to the existing canonical "kg" value.
	if write.Unit != "KG" {
		t.Errorf("unit = %q, want the trimmed value", write.Unit)
	}

}

// A decimal that JavaScript's Number and Go's float64 both round must survive
// the handler as the exact text the caller sent. It never becomes a number here:
// it goes to NUMERIC as a string and comes back through ::TEXT.
func TestBindOfferingWritePreservesExactDecimalText(t *testing.T) {
	a := New(APIOpts{Logg: slog.New(slog.DiscardHandler)})

	for _, price := range []string{
		"120.50", // trailing zero is meaningful and must not be trimmed
		"0.000000000000000001",
		"9007199254740993",           // Number.MAX_SAFE_INTEGER + 2
		"12345678901234567890.12345", // wider than float64's mantissa
	} {
		t.Run(price, func(t *testing.T) {
			body := validOfferingBody(map[string]any{"price": price})
			req := httptest.NewRequest(http.MethodPost, "/api/v2/vouchers/"+ownerAddress+"/offerings", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			ctx := a.router.NewContext(req, httptest.NewRecorder())
			write, handled, err := a.bindOfferingWrite(ctx)
			if handled || err != nil {
				t.Fatalf("price %q rejected", price)
			}
			if write.Price != price {
				t.Fatalf("price = %q, want the submitted text %q", write.Price, price)
			}
		})
	}
}

// Absent, null and empty all mean "no photo". They have to, because the form's
// clear control and a client that omits the key are the same intent, and PUT is
// assign-all so omission clears rather than preserves.
func TestBindOfferingWriteImageURL(t *testing.T) {
	a := New(APIOpts{Logg: slog.New(slog.DiscardHandler)})

	for name, tc := range map[string]struct {
		override map[string]any
		wantNil  bool
		want     string
	}{
		"omitted":          {map[string]any{}, true, ""},
		"null":             {map[string]any{"imageUrl": nil}, true, ""},
		"http":             {map[string]any{"imageUrl": "http://content.example/offering/a_800_b.webp"}, false, "http://content.example/offering/a_800_b.webp"},
		"https":            {map[string]any{"imageUrl": "https://content.example/offering/a_800_b.webp"}, false, "https://content.example/offering/a_800_b.webp"},
		"whitespace":       {map[string]any{"imageUrl": "  https://content.example/offering/a_800_b.webp  "}, false, "https://content.example/offering/a_800_b.webp"},
		"uppercase scheme": {map[string]any{"imageUrl": "HTTPS://content.example/offering/a_800_b.webp"}, false, "HTTPS://content.example/offering/a_800_b.webp"},
	} {
		t.Run(name, func(t *testing.T) {
			body := validOfferingBody(tc.override)
			req := httptest.NewRequest(http.MethodPut, "/api/v2/vouchers/"+ownerAddress+"/offerings/1", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			ctx := a.router.NewContext(req, httptest.NewRecorder())
			write, handled, err := a.bindOfferingWrite(ctx)
			if handled || err != nil {
				t.Fatalf("body rejected: %s", body)
			}
			if tc.wantNil {
				if write.ImageURL != nil {
					t.Fatalf("imageUrl = %q, want nil", *write.ImageURL)
				}
				return
			}
			if write.ImageURL == nil || *write.ImageURL != tc.want {
				t.Fatalf("imageUrl = %v, want %q", write.ImageURL, tc.want)
			}
		})
	}
}

// An empty-string image is the same as no image, not a constraint violation.
func TestBindOfferingWriteEmptyImageIsCleared(t *testing.T) {
	a := New(APIOpts{Logg: slog.New(slog.DiscardHandler)})
	body := validOfferingBody(map[string]any{"imageUrl": "   "})
	req := httptest.NewRequest(http.MethodPut, "/api/v2/vouchers/"+ownerAddress+"/offerings/1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx := a.router.NewContext(req, httptest.NewRecorder())
	write, handled, err := a.bindOfferingWrite(ctx)
	if handled || err != nil {
		t.Fatalf("body rejected: %s", body)
	}
	if write.ImageURL != nil {
		t.Fatalf("imageUrl = %q, want nil", *write.ImageURL)
	}
}

func TestOfferingRouteParamsRejectBadValues(t *testing.T) {
	e := echo.New()
	for name, tc := range map[string]struct{ address, id string }{
		"bad address": {"not-an-address", "1"},
		"zero id":     {ownerAddress, "0"},
		"negative id": {ownerAddress, "-1"},
		"non-numeric": {ownerAddress, "abc"},
		"empty id":    {ownerAddress, ""},
		"overflow id": {ownerAddress, "99999999999999999999999"},
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v2/vouchers/x/offerings/y", nil)
			res := httptest.NewRecorder()
			ctx := e.NewContext(req, res)
			ctx.SetPathValues(echo.PathValues{
				{Name: "address", Value: tc.address},
				{Name: "id", Value: tc.id},
			})
			if _, _, handled, err := offeringRouteParams(ctx); !handled || err != nil {
				t.Fatalf("handled = %v, err = %v, want handled with no error", handled, err)
			}
			if res.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", res.Code)
			}
		})
	}
}

// Every mutation route requires a user session, and refuses a service token, via
// requireUserAuth. Asserted through the real route table so a route mounted on
// the wrong group fails here rather than in production.
func TestOfferingMutationsRequireAUserSession(t *testing.T) {
	a := New(APIOpts{Logg: slog.New(slog.DiscardHandler), CORS: []string{"https://app.example"}})

	for _, tc := range []struct {
		method, target string
	}{
		{http.MethodPost, "/api/v2/vouchers/" + ownerAddress + "/offerings"},
		{http.MethodPut, "/api/v2/vouchers/" + ownerAddress + "/offerings/1"},
		{http.MethodDelete, "/api/v2/vouchers/" + ownerAddress + "/offerings/1"},
	} {
		t.Run(tc.method, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.target, strings.NewReader(validOfferingBody(nil)))
			req.Header.Set("Content-Type", "application/json")
			req.RemoteAddr = "198.51.100.44:5000"
			res := httptest.NewRecorder()
			a.router.ServeHTTP(res, req)

			if res.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 body=%s", res.Code, res.Body.String())
			}
		})
	}
}

// The item read is public: no cookie, no header, and it must not answer 401.
// Registered on the root router with the market rate limit, like the list beside
// it, so the Market tab works for a signed-out visitor.
func TestVoucherOfferingItemReadIsPublic(t *testing.T) {
	a := New(APIOpts{Logg: slog.New(slog.DiscardHandler), CORS: []string{"https://app.example"}})

	req := httptest.NewRequest(http.MethodGet, "/api/v2/vouchers/not-an-address/offerings/1", nil)
	req.RemoteAddr = "198.51.100.45:5000"
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, req)

	// A protected registration would answer 401 before address validation.
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want the public handler's 400 body=%s", res.Code, res.Body.String())
	}
	// Errors are not cacheable, matching the list route.
	if got := res.Header().Get("Cache-Control"); got != "" {
		t.Fatalf("error Cache-Control = %q, want empty", got)
	}
}

// The Go unit list and the offering_unit lookup are two copies of one closed set.
// The API has to hold a copy because the database cannot be the primary check —
// see the comment on apiresp.OfferingUnits — so this is the guard that keeps the
// copies honest.
func TestOfferingUnitsMatchTheMigration(t *testing.T) {
	for _, path := range []string{
		"../../migrations/023_offering_crud.sql",
		"../../migrations/001_init.sql",
	} {
		t.Run(path, func(t *testing.T) {
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			seed := offeringUnitSeed(t, string(content))

			if len(seed) != len(apiresp.OfferingUnits) {
				t.Fatalf("%s seeds %d units, apiresp.OfferingUnits has %d: %v vs %v",
					path, len(seed), len(apiresp.OfferingUnits), seed, apiresp.OfferingUnits)
			}
			for _, unit := range apiresp.OfferingUnits {
				if !slices.Contains(seed, unit) {
					t.Errorf("%s does not seed %q", path, unit)
				}
			}
			for _, unit := range seed {
				if !slices.Contains(apiresp.OfferingUnits, unit) {
					t.Errorf("%s seeds %q, which the API would reject", path, unit)
				}
			}
		})
	}
}

// offeringUnitSeed extracts the values of the INSERT INTO offering_unit block.
func offeringUnitSeed(t *testing.T, sql string) []string {
	t.Helper()

	start := strings.Index(sql, "INSERT INTO offering_unit (value) VALUES")
	if start == -1 {
		t.Fatal("no offering_unit seed found")
	}
	end := strings.Index(sql[start:], ";")
	if end == -1 {
		t.Fatal("unterminated offering_unit seed")
	}

	var units []string
	for line := range strings.SplitSeq(sql[start:start+end], "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "('") {
			continue
		}
		value := line[2:]
		if before, _, ok := strings.Cut(value, "'"); ok {
			units = append(units, before)
		}
	}
	return units
}
