package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The voucher profile write, end to end: real route table, real auth middleware,
// real store, real database. Skipped unless CORE_TEST_DSN is set:
//
//	CORE_TEST_DSN=postgres://postgres:postgres@127.0.0.1:5432/clc_core go test ./internal/api/
//
// Fixtures are committed, because the requests under test open their own
// connections and cannot see an uncommitted transaction, and every one carries a
// marker unique to this file so cleanup cannot touch another suite's rows.
const voucherProfileMarker = "Zqvoucherprofile"

// seedEditableVoucher creates one active, geolocated voucher with two tags, and
// returns its address. Two tags rather than one so a replace that keeps a subset
// is distinguishable from a replace that keeps everything.
func seedEditableVoucherFixture(t *testing.T, a *API) (address string, tags []string, cleanup func()) {
	t.Helper()
	ctx := context.Background()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}

	var voucherID uint64
	if err := tx.QueryRow(ctx, `
		INSERT INTO vouchers (chain_id, address, name, symbol, decimals, owner_address,
			description, voucher_uoa, voucher_value, email, phone, location_name,
			geo_lat, geo_lng, active)
		VALUES (42220, '0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'),
			$1, 'ZQV', 6, '0x11a573149db22e759fB9A38Bcc461C12855c2600',
			'Before', 'KES', 5, 'before@example.org', '+254700', $2,
			-1.29, 36.82, TRUE)
		RETURNING id, address`, voucherProfileMarker+" Voucher", voucherProfileMarker).
		Scan(&voucherID, &address); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("seed voucher: %v", err)
	}

	rows, err := tx.Query(ctx, `SELECT value FROM token_tag ORDER BY value LIMIT 2`)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("read tag vocabulary: %v", err)
	}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			rows.Close()
			_ = tx.Rollback(ctx)
			t.Fatalf("scan tag: %v", err)
		}
		tags = append(tags, value)
	}
	rows.Close()
	if len(tags) < 2 {
		_ = tx.Rollback(ctx)
		t.Skip("token_tag holds fewer than two values; nothing to replace")
	}

	for _, tag := range tags {
		if _, err := tx.Exec(ctx,
			`INSERT INTO voucher_tags (voucher_id, tag_value) VALUES ($1, $2)`,
			voucherID, tag); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("seed tag %q: %v", tag, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	// Cleanup keys off the row's id, captured here. It cannot key off
	// location_name the way the read-only suites do: this endpoint is assign-all,
	// so a request that omits locationName clears it, and a marker-matching
	// DELETE then finds nothing and leaks the fixture into the shared database.
	return address, tags, func() {
		remove, err := a.store.Pool().Begin(ctx)
		if err != nil {
			t.Errorf("begin cleanup: %v", err)
			return
		}
		defer remove.Rollback(ctx)

		for _, statement := range []string{
			`DELETE FROM voucher_tags WHERE voucher_id = $1`,
			`DELETE FROM vouchers WHERE id = $1`,
		} {
			if _, err := remove.Exec(ctx, statement, voucherID); err != nil {
				t.Errorf("cleanup: %v", err)
				return
			}
		}
		if err := remove.Commit(ctx); err != nil {
			t.Errorf("commit cleanup: %v", err)
		}
	}
}

func putVoucherProfile(
	t *testing.T,
	a *API,
	token, address string,
	body map[string]any,
) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode body: %v", err)
	}

	target := "/api/v2/admin/vouchers/" + address + "/profile"
	req := httptest.NewRequest(http.MethodPut, target, strings.NewReader(string(encoded)))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = fmt.Sprintf("198.51.100.%d:5100", 100+len(address)%50)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res := httptest.NewRecorder()
	a.router.ServeHTTP(res, req)

	var envelope struct {
		Ok     bool           `json:"ok"`
		Result map[string]any `json:"result"`
	}
	if res.Code == http.StatusOK {
		if err := json.Unmarshal(res.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode %s: %v", target, err)
		}
	}
	return res, envelope.Result
}

func voucherFromResult(t *testing.T, result map[string]any) map[string]any {
	t.Helper()
	voucher, ok := result["voucher"].(map[string]any)
	if !ok {
		t.Fatalf("result.voucher is not an object: %#v", result["voucher"])
	}
	return voucher
}

// The response is what the console writes into its cache, so a save that returns
// the wrong row is a save the operator sees revert on the next read.
func TestAdminUpdateVoucherProfileReturnsTheSavedRecord(t *testing.T) {
	a, token := newAdminAPI(t)
	address, tags, cleanup := seedEditableVoucherFixture(t, a)
	defer cleanup()

	res, result := putVoucherProfile(t, a, token, address, map[string]any{
		"description":  "After",
		"voucherUoa":   "HOUR",
		"voucherValue": 2.5,
		"email":        "after@example.org",
		"website":      "https://after.example",
		"terms":        "https://after.example/terms",
		"locationName": voucherProfileMarker,
		"geoLat":       -3.5,
		"geoLng":       39.9,
		"tags":         []string{tags[1]},
	})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}

	voucher := voucherFromResult(t, result)
	if got := voucher["description"]; got != "After" {
		t.Errorf("description = %v, want After", got)
	}
	if got := voucher["voucherUoa"]; got != "HOUR" {
		t.Errorf("voucherUoa = %v, want HOUR", got)
	}
	// NUMERIC is serialized as text so no float64 rounds the value on the way out.
	if got := voucher["voucherValue"]; got != "2.5" {
		t.Errorf("voucherValue = %#v, want the string 2.5", got)
	}
	if got := voucher["terms"]; got != "https://after.example/terms" {
		t.Errorf("terms = %v, want the saved URL", got)
	}
	// Assign-all: phone was seeded and not sent, so it is cleared.
	if got := voucher["phone"]; got != nil {
		t.Errorf("phone = %v, want nil — an omitted field must clear", got)
	}

	got, ok := voucher["tags"].([]any)
	if !ok {
		t.Fatalf("tags is not a list: %#v", voucher["tags"])
	}
	if len(got) != 1 || got[0] != tags[1] {
		t.Errorf("tags = %#v, want exactly [%s]", got, tags[1])
	}
}

// Images are the one field pair that preserves on omission, so a console that
// edits prose cannot blank what a deploy carried in.
func TestAdminUpdateVoucherProfilePreservesImagesWhenOmitted(t *testing.T) {
	a, token := newAdminAPI(t)
	address, _, cleanup := seedEditableVoucherFixture(t, a)
	defer cleanup()

	icon := "https://content.example/voucher_400_a3f91c2e.webp"
	_, result := putVoucherProfile(t, a, token, address, map[string]any{
		"description": "With an icon",
		"iconUrl":     icon,
	})
	if got := voucherFromResult(t, result)["iconUrl"]; got != icon {
		t.Fatalf("iconUrl = %v, want %s", got, icon)
	}

	_, result = putVoucherProfile(t, a, token, address, map[string]any{
		"description": "Edited without mentioning images",
	})
	if got := voucherFromResult(t, result)["iconUrl"]; got != icon {
		t.Errorf("iconUrl = %v, want the icon to survive an edit that says nothing about it", got)
	}

	// Clearing needs a value, not an absence.
	_, result = putVoucherProfile(t, a, token, address, map[string]any{"iconUrl": ""})
	if got := voucherFromResult(t, result)["iconUrl"]; got != nil {
		t.Errorf("iconUrl = %v, want nil after an empty string", got)
	}
}

// Half a pin describes a place nobody can point at. The 400 names the field so
// the operator is not left reading a constraint name.
func TestAdminUpdateVoucherProfileRejectsHalfAPin(t *testing.T) {
	a, token := newAdminAPI(t)
	address, _, cleanup := seedEditableVoucherFixture(t, a)
	defer cleanup()

	res, _ := putVoucherProfile(t, a, token, address, map[string]any{"geoLat": -3.5})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "geoLat and geoLng") {
		t.Errorf("body = %s, want it to name both coordinate fields", res.Body.String())
	}
}

// A tag outside the vocabulary is rejected whole, so the profile edit beside it
// does not land half-applied.
func TestAdminUpdateVoucherProfileRejectsAnUnknownTag(t *testing.T) {
	a, token := newAdminAPI(t)
	address, _, cleanup := seedEditableVoucherFixture(t, a)
	defer cleanup()

	res, _ := putVoucherProfile(t, a, token, address, map[string]any{
		"description": "Should not be saved",
		"tags":        []string{"NotARealTagValue"},
	})
	if res.Code == http.StatusOK {
		t.Fatalf("an unknown tag was accepted: %s", res.Body.String())
	}

	var description *string
	if err := a.store.Pool().QueryRow(context.Background(),
		`SELECT description FROM vouchers WHERE lower(address) = lower($1)`, address).
		Scan(&description); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if description == nil || *description != "Before" {
		t.Errorf("description = %v, want the seeded value — a rejected tag must roll the edit back", description)
	}
}

func TestAdminUpdateVoucherProfileReportsAnUnknownVoucher(t *testing.T) {
	a, token := newAdminAPI(t)

	res, _ := putVoucherProfile(t, a, token,
		"0x000000000000000000000000000000000000dEaD", map[string]any{"description": "Nobody"})
	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", res.Code, res.Body.String())
	}
}

// The address is the identity and arrives in whatever casing the caller used.
func TestAdminUpdateVoucherProfileIgnoresAddressCasing(t *testing.T) {
	a, token := newAdminAPI(t)
	address, _, cleanup := seedEditableVoucherFixture(t, a)
	defer cleanup()

	res, result := putVoucherProfile(t, a, token, strings.ToLower(address),
		map[string]any{"description": "Lowercased request"})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if got := voucherFromResult(t, result)["address"]; got != address {
		t.Errorf("address = %v, want the stored casing %s", got, address)
	}
}

// clc-core is the gate, not the console: requireSuperAdmin covers this route the
// same as every other under /admin.
func TestAdminUpdateVoucherProfileRefusesANonSuperadmin(t *testing.T) {
	a, _ := newAdminAPI(t)
	address, _, cleanup := seedEditableVoucherFixture(t, a)
	defer cleanup()

	res, _ := putVoucherProfile(t, a, "", address, map[string]any{"description": "Anonymous"})
	if res.Code != http.StatusUnauthorized && res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 401 or 403, body = %s", res.Code, res.Body.String())
	}
}
