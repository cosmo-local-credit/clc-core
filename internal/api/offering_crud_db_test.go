package api

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/golang-jwt/jwt/v5"
)

// End-to-end coverage of offering CRUD through the real route table, auth
// middleware, store and database. Skipped unless CORE_TEST_DSN is set:
//
//	CORE_TEST_DSN=postgres://postgres:postgres@127.0.0.1:5432/clc_core go test ./internal/api/
//
// Fixtures are committed, because the requests under test open their own
// connections and cannot see an uncommitted transaction. Every fixture carries a
// marker unique to this file so cleanup cannot touch another suite's rows.
const (
	offeringMarker       = "Zqofferingcrud"
	offeringOwnerAddress = "0x33c573149db22e759fB9A38Bcc461C12855c2633"
	offeringStrangerAddr = "0x44d573149db22e759fB9A38Bcc461C12855c2644"
	offeringOperatorAddr = "0x55e573149db22e759fB9A38Bcc461C12855c2655"

	// Free text no canonical unit list will ever contain, of the kind the Sarafu
	// import actually carried.
	legacyOfferingUnit = "50kg sack"
)

type offeringFixtures struct {
	ownedAddress     string
	ownedID          uint64
	inactiveAddress  string
	ownerlessAddress string
	// An imported row whose unit is free text the canonical set does not contain
	// and whose seller is nobody's current owner.
	legacyOfferingID uint64
	otherVoucherAddr string
	otherOfferingID  uint64
}

func newOfferingAPI(t *testing.T) (*API, ed25519.PrivateKey) {
	t.Helper()

	dsn := os.Getenv("CORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CORE_TEST_DSN not set; skipping live offering CRUD tests")
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

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	return New(APIOpts{
		Logg:         logg,
		CORS:         []string{"https://app.example"},
		Store:        pgStore,
		SigningKey:   priv,
		VerifyingKey: pub,
	}), priv
}

// offeringToken mints a session for an arbitrary address and role, which the
// shared mintToken cannot do — every authorization case here turns on who the
// caller is.
func offeringToken(t *testing.T, priv ed25519.PrivateKey, address, role string) string {
	t.Helper()

	claims := UserJWTClaims{
		UserID:          7,
		EthereumAddress: address,
		Role:            role,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(priv)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

// offeringServiceToken mints a service credential. requireUserAuth must refuse
// it: an offering's seller has to name a user wallet.
func offeringServiceToken(t *testing.T, priv ed25519.PrivateKey) string {
	t.Helper()

	signed, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{
		"service": "indexer",
		"iat":     time.Now().Unix(),
		"exp":     time.Now().Add(time.Hour).Unix(),
	}).SignedString(priv)
	if err != nil {
		t.Fatalf("sign service token: %v", err)
	}
	return signed
}

func seedOfferingFixtures(t *testing.T, a *API) (offeringFixtures, func()) {
	t.Helper()
	ctx := context.Background()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}

	var fx offeringFixtures
	voucher := func(owner *string, name string, active bool) (uint64, string) {
		var (
			id      uint64
			address string
		)
		if err := tx.QueryRow(ctx, `
			INSERT INTO vouchers (chain_id, address, name, symbol, decimals, active,
			    geo_lat, geo_lng, location_name, owner_address)
			VALUES (42220, '0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'),
			        $1, 'ZQO', 6, $2, -1.29, 36.82, $3, $4)
			RETURNING id, address`, name, active, offeringMarker, owner).Scan(&id, &address); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("seed voucher %q: %v", name, err)
		}
		return id, address
	}

	// The owner is stored lower-case while the session claim is checksummed, so
	// every ownership assertion below also proves the comparison is case-folded.
	lowerOwner := strings.ToLower(offeringOwnerAddress)
	ownedID, ownedAddress := voucher(&lowerOwner, offeringMarker+" Owned", true)
	fx.ownedID, fx.ownedAddress = ownedID, ownedAddress

	_, fx.inactiveAddress = voucher(&lowerOwner, offeringMarker+" Retired", false)
	_, fx.ownerlessAddress = voucher(nil, offeringMarker+" Imported", true)

	otherOwner := strings.ToLower(offeringStrangerAddr)
	otherID, otherAddress := voucher(&otherOwner, offeringMarker+" Other", true)
	fx.otherVoucherAddr = otherAddress

	// An imported row: free-text unit, a seller who is not the current owner, and
	// a quantity the importer meant. None of it may block a write, and none of it
	// may be lost by one.
	//
	// The unit is why the constraint is dropped and re-added around the seed. A
	// NOT VALID foreign key grandfathers rows that already existed and constrains
	// every insert after it, so "50kg sack" cannot be inserted through it — which
	// is the behaviour under test. Recreating the pre-023 state is the only honest
	// way to produce the row migration 023 was written to preserve, and
	// re-adding NOT VALID is free because it skips the table scan.
	if _, err := tx.Exec(ctx, `ALTER TABLE offerings DROP CONSTRAINT offerings_unit_fk`); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("drop unit constraint for the legacy fixture: %v", err)
	}

	offering := func(voucherID uint64, seller, name, unit string) uint64 {
		var id uint64
		if err := tx.QueryRow(ctx, `
			INSERT INTO offerings (voucher_id, seller_address, commodity_name,
			    commodity_type, description, quantity, unit, price, available, location_name)
			VALUES ($1, $2, $3, 'GOOD', 'Imported row.', 12, $4, '7.25', TRUE, $5)
			RETURNING id`, voucherID, seller, name, unit, offeringMarker).Scan(&id); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("seed offering %q: %v", name, err)
		}
		return id
	}
	fx.legacyOfferingID = offering(ownedID, strings.ToLower(offeringStrangerAddr), offeringMarker+" Legacy", legacyOfferingUnit)
	fx.otherOfferingID = offering(otherID, otherOwner, offeringMarker+" Foreign", "bag")

	if _, err := tx.Exec(ctx, `
		ALTER TABLE offerings
		    ADD CONSTRAINT offerings_unit_fk
		        FOREIGN KEY (unit) REFERENCES offering_unit(value)
		        NOT VALID`); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("restore unit constraint: %v", err)
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	return fx, func() {
		cleanup, err := a.store.Pool().Begin(ctx)
		if err != nil {
			t.Errorf("begin cleanup: %v", err)
			return
		}
		defer cleanup.Rollback(ctx)

		for _, statement := range []string{
			`DELETE FROM offering_tags WHERE offering_id IN
			     (SELECT id FROM offerings WHERE location_name = $1
			      OR voucher_id IN (SELECT id FROM vouchers WHERE location_name = $1))`,
			`DELETE FROM offerings WHERE location_name = $1
			     OR voucher_id IN (SELECT id FROM vouchers WHERE location_name = $1)`,
			`DELETE FROM vouchers WHERE location_name = $1`,
		} {
			if _, err := cleanup.Exec(ctx, statement, offeringMarker); err != nil {
				t.Errorf("cleanup: %v", err)
				return
			}
		}
		if err := cleanup.Commit(ctx); err != nil {
			t.Errorf("commit cleanup: %v", err)
		}
	}
}

func offeringRequest(
	t *testing.T,
	a *API,
	method, target, token string,
	body map[string]any,
) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()

	var reader *strings.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode body: %v", err)
		}
		reader = strings.NewReader(string(encoded))
	} else {
		reader = strings.NewReader("")
	}

	req := httptest.NewRequest(method, target, reader)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "198.51.100.77:5000"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, req)

	var envelope struct {
		Ok     bool           `json:"ok"`
		Result map[string]any `json:"result"`
	}
	if res.Code == http.StatusOK || res.Code == http.StatusCreated {
		if err := json.Unmarshal(res.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode %s %s: %v", method, target, err)
		}
	}
	return res, envelope.Result
}

func offeringBody(overrides map[string]any) map[string]any {
	body := map[string]any{
		"commodityName": "Fresh maize flour",
		"commodityType": "GOOD",
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
	return body
}

func offeringFrom(t *testing.T, result map[string]any) map[string]any {
	t.Helper()
	offering, ok := result["offering"].(map[string]any)
	if !ok {
		t.Fatalf("result has no offering object: %#v", result)
	}
	return offering
}

// The whole create contract in one round trip: the voucher link, the
// server-selected seller, the compatibility defaults, and the decimal returned
// exactly as it was sent.
func TestOfferingCreateLinksTheVoucherAndSelectsTheSeller(t *testing.T) {
	a, priv := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()

	token := offeringToken(t, priv, offeringOwnerAddress, "USER")
	res, result := offeringRequest(t, a, http.MethodPost,
		"/api/v2/vouchers/"+fx.ownedAddress+"/offerings", token,
		offeringBody(map[string]any{
			"imageUrl": "https://content.example/offering/a_800_b3f91c2e.webp",
			// A decimal wider than float64's mantissa. If anything on the path
			// parsed it as a number this assertion fails.
			"price": "12345678901234567890.12345",
		}))
	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 body=%s", res.Code, res.Body.String())
	}

	offering := offeringFrom(t, result)
	if got := offering["voucherAddress"]; !strings.EqualFold(got.(string), fx.ownedAddress) {
		t.Errorf("voucherAddress = %v, want %q", got, fx.ownedAddress)
	}
	// Server-selected from the locked voucher owner, and never from JSON.
	if got, _ := offering["sellerAddress"].(string); !strings.EqualFold(got, offeringOwnerAddress) {
		t.Errorf("sellerAddress = %q, want the voucher owner %q", got, offeringOwnerAddress)
	}
	if got := offering["price"]; got != "12345678901234567890.12345" {
		t.Errorf("price = %v, want the submitted text unchanged", got)
	}
	if got := offering["commodityType"]; got != "GOOD" {
		t.Errorf("commodityType = %v, want GOOD", got)
	}
	if got := offering["unit"]; got != "kg" {
		t.Errorf("unit = %v, want kg", got)
	}
	// Compatibility values for the legacy columns the product does not manage.
	if got := offering["quantity"]; got != "1" {
		t.Errorf("quantity = %v, want the compatibility value 1", got)
	}
	if got := offering["frequency"]; got != nil {
		t.Errorf("frequency = %v, want null", got)
	}
	if got := offering["locationName"]; got != nil {
		t.Errorf("locationName = %v, want null; the voucher is the location authority", got)
	}
	if got := offering["available"]; got != true {
		t.Errorf("available = %v, want true", got)
	}
	// A mutation response must never be cached.
	if got := res.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

// A caller cannot choose the seller or the parent voucher by sending them.
// There is no field for either, so the extra keys are simply not read.
func TestOfferingCreateIgnoresSellerAndVoucherInTheBody(t *testing.T) {
	a, priv := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()

	token := offeringToken(t, priv, offeringOwnerAddress, "USER")
	res, result := offeringRequest(t, a, http.MethodPost,
		"/api/v2/vouchers/"+fx.ownedAddress+"/offerings", token,
		offeringBody(map[string]any{
			"sellerAddress": offeringStrangerAddr,
			"voucherId":     999999,
			"available":     false,
			"quantity":      "500",
			"sourceId":      42,
		}))
	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 body=%s", res.Code, res.Body.String())
	}

	offering := offeringFrom(t, result)
	if got, _ := offering["sellerAddress"].(string); !strings.EqualFold(got, offeringOwnerAddress) {
		t.Errorf("sellerAddress = %q, want the voucher owner", got)
	}
	if got := offering["quantity"]; got != "1" {
		t.Errorf("quantity = %v, want the server's compatibility value", got)
	}
	if got := offering["available"]; got != true {
		t.Errorf("available = %v, want true", got)
	}
	if got := offering["sourceId"]; got != nil {
		t.Errorf("sourceId = %v, want null; provenance is not a request field", got)
	}
}

func TestOfferingCreateAuthorization(t *testing.T) {
	a, priv := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()

	for name, tc := range map[string]struct {
		address string
		token   string
		want    int
	}{
		"owner": {
			fx.ownedAddress, offeringToken(t, priv, offeringOwnerAddress, "USER"), http.StatusCreated,
		},
		// The claim is checksummed and the column is lower-case.
		"owner in another casing": {
			fx.ownedAddress, offeringToken(t, priv, strings.ToLower(offeringOwnerAddress), "USER"), http.StatusCreated,
		},
		"superadmin on someone else's voucher": {
			fx.otherVoucherAddr, offeringToken(t, priv, offeringOperatorAddr, "SUPERADMIN"), http.StatusCreated,
		},
		"superadmin on an ownerless imported voucher": {
			fx.ownerlessAddress, offeringToken(t, priv, offeringOperatorAddr, "SUPERADMIN"), http.StatusCreated,
		},
		"a different signed-in user": {
			fx.ownedAddress, offeringToken(t, priv, offeringStrangerAddr, "USER"), http.StatusForbidden,
		},
		// An ownerless voucher has no owner to be, so a normal user is refused
		// rather than becoming the seller by default.
		"a user on an ownerless voucher": {
			fx.ownerlessAddress, offeringToken(t, priv, offeringOwnerAddress, "USER"), http.StatusForbidden,
		},
		"no session": {
			fx.ownedAddress, "", http.StatusUnauthorized,
		},
		"a service credential": {
			fx.ownedAddress, offeringServiceToken(t, priv), http.StatusForbidden,
		},
		"a retired voucher": {
			fx.inactiveAddress, offeringToken(t, priv, offeringOwnerAddress, "USER"), http.StatusConflict,
		},
		"a voucher that does not exist": {
			"0x99f573149db22e759fB9A38Bcc461C12855c2699",
			offeringToken(t, priv, offeringOwnerAddress, "USER"), http.StatusNotFound,
		},
	} {
		t.Run(name, func(t *testing.T) {
			res, _ := offeringRequest(t, a, http.MethodPost,
				"/api/v2/vouchers/"+tc.address+"/offerings", tc.token, offeringBody(nil))
			if res.Code != tc.want {
				t.Fatalf("status = %d, want %d body=%s", res.Code, tc.want, res.Body.String())
			}
		})
	}
}

// A superadmin publishing for an imported voucher with no owner becomes the
// seller: the column is NOT NULL and format-checked, and the operator who
// published it is the only honest attribution available.
func TestOfferingCreateOnOwnerlessVoucherAttributesTheOperator(t *testing.T) {
	a, priv := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()

	token := offeringToken(t, priv, offeringOperatorAddr, "SUPERADMIN")
	res, result := offeringRequest(t, a, http.MethodPost,
		"/api/v2/vouchers/"+fx.ownerlessAddress+"/offerings", token, offeringBody(nil))
	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 body=%s", res.Code, res.Body.String())
	}
	if got, _ := offeringFrom(t, result)["sellerAddress"].(string); !strings.EqualFold(got, offeringOperatorAddr) {
		t.Errorf("sellerAddress = %q, want the authenticated superadmin %q", got, offeringOperatorAddr)
	}
}

// Update is assign-all over the product fields and nothing else. The immutable
// relationship, the provenance, and the legacy import columns all survive.
func TestOfferingUpdateReplacesProductFieldsOnly(t *testing.T) {
	a, priv := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()

	token := offeringToken(t, priv, offeringOwnerAddress, "USER")
	target := offeringItemPath(fx.ownedAddress, fx.legacyOfferingID)

	res, result := offeringRequest(t, a, http.MethodPut, target, token, offeringBody(map[string]any{
		"commodityName": "Renamed flour",
		"commodityType": "SERVICE",
		"description":   "Now milled to order.",
		"unit":          "bag",
		"price":         "9.99",
		"imageUrl":      "https://content.example/offering/c_800_d3f91c2e.webp",
	}))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", res.Code, res.Body.String())
	}

	offering := offeringFrom(t, result)
	for field, want := range map[string]any{
		"commodityName": "Renamed flour",
		"commodityType": "SERVICE",
		"description":   "Now milled to order.",
		"unit":          "bag",
		"price":         "9.99",
		"imageUrl":      "https://content.example/offering/c_800_d3f91c2e.webp",
	} {
		if got := offering[field]; got != want {
			t.Errorf("%s = %v, want %v", field, got, want)
		}
	}
	// The seller stays the historical importer's value. Authority came from the
	// voucher relationship, so an edit must not rewrite provenance.
	if got, _ := offering["sellerAddress"].(string); !strings.EqualFold(got, offeringStrangerAddr) {
		t.Errorf("sellerAddress = %q, want the original importer's %q", got, offeringStrangerAddr)
	}
	// The legacy quantity the importer meant is untouched: it is not a form field
	// and an edit is not an excuse to overwrite it.
	if got := offering["quantity"]; got != "12" {
		t.Errorf("quantity = %v, want the imported 12", got)
	}
	if got, _ := offering["voucherId"].(float64); uint64(got) != fx.ownedID {
		t.Errorf("voucherId = %v, want the immutable %d", offering["voucherId"], fx.ownedID)
	}
	if got := res.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

// Assign-all means omission clears. There is no PATCH and no preserve-on-omission
// exception, because a form showing every field it can change has no way to say
// "leave this one alone".
func TestOfferingUpdateClearsAnOmittedPhoto(t *testing.T) {
	a, priv := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()

	token := offeringToken(t, priv, offeringOwnerAddress, "USER")
	target := offeringItemPath(fx.ownedAddress, fx.legacyOfferingID)

	if res, _ := offeringRequest(t, a, http.MethodPut, target, token, offeringBody(map[string]any{
		"imageUrl": "https://content.example/offering/e_800_f3f91c2e.webp",
	})); res.Code != http.StatusOK {
		t.Fatalf("set status = %d, want 200 body=%s", res.Code, res.Body.String())
	}

	res, result := offeringRequest(t, a, http.MethodPut, target, token, offeringBody(nil))
	if res.Code != http.StatusOK {
		t.Fatalf("clear status = %d, want 200 body=%s", res.Code, res.Body.String())
	}
	if got := offeringFrom(t, result)["imageUrl"]; got != nil {
		t.Errorf("imageUrl = %v, want null after an assign-all write that omitted it", got)
	}
}

// An imported free-text unit stays readable, and a write has to replace it with a
// canonical one — including a write that re-sends the same legacy value.
//
// That last case is why the API validates the unit itself rather than leaning on
// the database. A NOT VALID foreign key's update trigger short-circuits when the
// column is unchanged, so `unit = '50kg sack'` on a row that already holds it
// would pass the constraint untouched and quietly persist an uneditable value.
func TestOfferingLegacyUnitSurvivesUntilItIsWritten(t *testing.T) {
	a, priv := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()

	res, result := offeringRequest(t, a, http.MethodGet,
		offeringItemPath(fx.ownedAddress, fx.legacyOfferingID), "", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("read status = %d, want 200 body=%s", res.Code, res.Body.String())
	}
	if got := offeringFrom(t, result)["unit"]; got != legacyOfferingUnit {
		t.Errorf("unit = %v, want the imported free text preserved", got)
	}

	token := offeringToken(t, priv, offeringOwnerAddress, "USER")
	res, _ = offeringRequest(t, a, http.MethodPut,
		offeringItemPath(fx.ownedAddress, fx.legacyOfferingID), token,
		offeringBody(map[string]any{"unit": legacyOfferingUnit}))
	if res.Code != http.StatusBadRequest {
		t.Fatalf("rewriting a legacy unit: status = %d, want 400 body=%s", res.Code, res.Body.String())
	}
}

// Unlisting keeps the row. Public reads stop returning it, market derivation stops
// counting it, and the Sarafu source_id a later import needs is still there.
func TestOfferingDeleteUnlistsWithoutDestroying(t *testing.T) {
	a, priv := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()

	token := offeringToken(t, priv, offeringOwnerAddress, "USER")
	target := offeringItemPath(fx.ownedAddress, fx.legacyOfferingID)

	before := offeringUpdatedAt(t, a, fx.legacyOfferingID)

	res, result := offeringRequest(t, a, http.MethodDelete, target, token, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", res.Code, res.Body.String())
	}
	if got, _ := result["id"].(float64); uint64(got) != fx.legacyOfferingID {
		t.Errorf("id = %v, want %d", result["id"], fx.legacyOfferingID)
	}
	if got := res.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}

	// Still in the database, now unavailable, with an advanced updated_at.
	available, after := offeringAvailability(t, a, fx.legacyOfferingID)
	if available {
		t.Error("available = true, want the row unlisted rather than removed")
	}
	if !after.After(before) {
		t.Errorf("updated_at = %v, want it advanced past %v", after, before)
	}

	// Gone from the public item read and the public list.
	if res, _ := offeringRequest(t, a, http.MethodGet, target, "", nil); res.Code != http.StatusNotFound {
		t.Errorf("public item read after delete: status = %d, want 404", res.Code)
	}
	res, listResult := offeringRequest(t, a, http.MethodGet,
		"/api/v2/vouchers/"+fx.ownedAddress+"/offerings?perPage=50", "", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", res.Code, res.Body.String())
	}
	rows, _ := listResult["offerings"].([]any)
	for _, row := range rows {
		if entry, ok := row.(map[string]any); ok {
			if got, _ := entry["id"].(float64); uint64(got) == fx.legacyOfferingID {
				t.Error("unlisted offering is still in the public list")
			}
		}
	}

	// And a second delete is a 404, not a silent success.
	if res, _ := offeringRequest(t, a, http.MethodDelete, target, token, nil); res.Code != http.StatusNotFound {
		t.Errorf("second delete: status = %d, want 404", res.Code)
	}
}

func TestOfferingItemAndMutationNotFoundCases(t *testing.T) {
	a, priv := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()

	token := offeringToken(t, priv, offeringOwnerAddress, "USER")

	// An id that exists, under a voucher that is not its parent. A 404, never a
	// redirect to its real parent and never a cross-parent write.
	mismatched := offeringItemPath(fx.ownedAddress, fx.otherOfferingID)
	for _, tc := range []struct {
		method string
		body   map[string]any
	}{
		{http.MethodGet, nil},
		{http.MethodPut, offeringBody(nil)},
		{http.MethodDelete, nil},
	} {
		t.Run("parent mismatch "+tc.method, func(t *testing.T) {
			callerToken := token
			if tc.method == http.MethodGet {
				callerToken = ""
			}
			res, _ := offeringRequest(t, a, tc.method, mismatched, callerToken, tc.body)
			if res.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 body=%s", res.Code, res.Body.String())
			}
		})
	}

	// A retired voucher hides its offerings from the public read entirely.
	t.Run("retired voucher item read", func(t *testing.T) {
		res, _ := offeringRequest(t, a, http.MethodGet,
			offeringItemPath(fx.inactiveAddress, fx.legacyOfferingID), "", nil)
		if res.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", res.Code)
		}
	})
}

// The public item read answers without a cookie and keeps the market cache
// headers, exactly like the list beside it.
func TestOfferingItemReadIsPublicAndCacheable(t *testing.T) {
	a, _ := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()

	res, result := offeringRequest(t, a, http.MethodGet,
		offeringItemPath(fx.ownedAddress, fx.legacyOfferingID), "", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", res.Code, res.Body.String())
	}
	if got := res.Header().Get("Cache-Control"); !strings.Contains(got, "max-age=60") {
		t.Errorf("Cache-Control = %q, want the market cache headers", got)
	}
	// The item projection is the list projection, so a client reconciles one shape.
	offering := offeringFrom(t, result)
	for _, field := range []string{
		"id", "voucherId", "voucherAddress", "voucherSymbol", "voucherDecimals",
		"sellerAddress", "commodityName", "commodityType", "description",
		"quantity", "unit", "price", "available", "imageUrl", "tags",
		"createdAt", "updatedAt",
	} {
		if _, ok := offering[field]; !ok {
			t.Errorf("item read is missing %q from the shared projection", field)
		}
	}
}

// Constraint violations that a caller could fix must arrive as a 400 naming the
// field, never as a 500 or a leaked SQL message.
func TestOfferingWriteRejectsInvalidValuesWithA400(t *testing.T) {
	a, priv := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()

	token := offeringToken(t, priv, offeringOwnerAddress, "USER")
	for name, body := range map[string]map[string]any{
		"unknown unit":   offeringBody(map[string]any{"unit": "furlong"}),
		"third type":     offeringBody(map[string]any{"commodityType": "UNCLASSIFIED"}),
		"data uri image": offeringBody(map[string]any{"imageUrl": "data:image/png;base64,AAAA"}),
		"zero price":     offeringBody(map[string]any{"price": "0"}),
		"exponent price": offeringBody(map[string]any{"price": "1e3"}),
		"blank name":     offeringBody(map[string]any{"commodityName": "   "}),
		"over-long name": offeringBody(map[string]any{"commodityName": strings.Repeat("a", 121)}),
	} {
		t.Run(name, func(t *testing.T) {
			res, _ := offeringRequest(t, a, http.MethodPost,
				"/api/v2/vouchers/"+fx.ownedAddress+"/offerings", token, body)
			if res.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 body=%s", res.Code, res.Body.String())
			}
			if strings.Contains(res.Body.String(), "SQLSTATE") ||
				strings.Contains(res.Body.String(), "constraint") {
				t.Fatalf("response leaks database detail: %s", res.Body.String())
			}
		})
	}
}

// Unlisting an imported row whose unit is not in the canonical lookup must still
// work. This is the whole reason the unit constraint is a NOT VALID foreign key
// and the image constraint is a validated CHECK: a foreign key's update trigger
// short-circuits when its column is unchanged, so `SET available = false` does
// not re-check the legacy unit.
func TestOfferingDeleteWorksOnALegacyUnit(t *testing.T) {
	a, priv := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()

	token := offeringToken(t, priv, offeringOwnerAddress, "USER")
	res, _ := offeringRequest(t, a, http.MethodDelete,
		offeringItemPath(fx.ownedAddress, fx.legacyOfferingID), token, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", res.Code, res.Body.String())
	}
}

// Creating an offering makes the voucher's market marker its classification, and
// unlisting the last one takes it back to the generic marker. No third commodity
// type is involved, so the existing derivation needed no change.
func TestOfferingWritesMoveTheMarketMarker(t *testing.T) {
	a, priv := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()

	token := offeringToken(t, priv, offeringOwnerAddress, "USER")
	res, result := offeringRequest(t, a, http.MethodPost,
		"/api/v2/vouchers/"+fx.ownerlessAddress+"/offerings",
		offeringToken(t, priv, offeringOperatorAddr, "SUPERADMIN"),
		offeringBody(map[string]any{"commodityType": "SERVICE", "unit": "hour"}))
	if res.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", res.Code, res.Body.String())
	}
	created := offeringFrom(t, result)

	if got := voucherMarketKind(t, a, fx.ownerlessAddress); got != "services" {
		t.Errorf("kind = %q, want services after a SERVICE offering", got)
	}

	id, _ := created["id"].(float64)
	if res, _ := offeringRequest(t, a, http.MethodDelete,
		offeringItemPath(fx.ownerlessAddress, uint64(id)),
		offeringToken(t, priv, offeringOperatorAddr, "SUPERADMIN"), nil); res.Code != http.StatusOK {
		t.Fatalf("delete status = %d", res.Code)
	}
	if got := voucherMarketKind(t, a, fx.ownerlessAddress); got != "voucher" {
		t.Errorf("kind = %q, want the generic marker once nothing is listed", got)
	}
	_ = token
}

func offeringItemPath(voucherAddress string, id uint64) string {
	return "/api/v2/vouchers/" + voucherAddress + "/offerings/" + offeringIDText(id)
}

func offeringIDText(id uint64) string {
	return jsonNumberText(id)
}

func jsonNumberText(id uint64) string {
	encoded, _ := json.Marshal(id)
	return string(encoded)
}

func offeringUpdatedAt(t *testing.T, a *API, id uint64) time.Time {
	t.Helper()
	_, updated := offeringAvailability(t, a, id)
	return updated
}

func offeringAvailability(t *testing.T, a *API, id uint64) (bool, time.Time) {
	t.Helper()
	ctx := context.Background()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin read: %v", err)
	}
	defer tx.Rollback(ctx)

	var (
		available bool
		updated   time.Time
	)
	if err := tx.QueryRow(ctx,
		`SELECT available, updated_at FROM offerings WHERE id = $1`, id,
	).Scan(&available, &updated); err != nil {
		t.Fatalf("read offering %d: %v", id, err)
	}
	return available, updated
}

func voucherMarketKind(t *testing.T, a *API, address string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v2/vouchers/"+address, nil)
	req.RemoteAddr = "198.51.100.78:5000"
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("voucher read status = %d body=%s", res.Code, res.Body.String())
	}

	var envelope struct {
		Result struct {
			Voucher map[string]any `json:"voucher"`
		} `json:"result"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode voucher: %v", err)
	}
	kind, _ := envelope.Result.Voucher["kind"].(string)
	return kind
}
