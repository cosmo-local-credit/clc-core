package api

import (
	"context"
	"net/http"
	"testing"
)

// The owner-facing profile write shares the offering fixture because it
// authorizes from the voucher's current owner exactly as offerings do. These
// are real route, auth and database tests; newOfferingAPI skips them unless
// CORE_TEST_DSN is set.
func TestVoucherOwnerCanEditTheWholeProfileWithoutTouchingChainFacts(t *testing.T) {
	a, signingKey := newOfferingAPI(t)
	fixtures, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()

	var tag string
	if err := a.store.Pool().QueryRow(context.Background(),
		`SELECT value FROM token_tag ORDER BY value LIMIT 1`).Scan(&tag); err != nil {
		t.Skipf("token_tag vocabulary is empty: %v", err)
	}

	owner := offeringToken(t, signingKey, offeringOwnerAddress, "USER")
	res, result := offeringRequest(t, a, http.MethodPut,
		"/api/v2/vouchers/"+fixtures.ownedAddress+"/profile", owner,
		map[string]any{
			"displayName":  "  Malii Fresh  ",
			"description":  "Fresh produce from the Kilifi stall",
			"voucherUoa":   "KES",
			"voucherValue": 12.5,
			"email":        "owner@example.org",
			"website":      "https://malii.example",
			"phone":        "+254700000000",
			"terms":        "Redeemable on market days.",
			"locationName": "Kilifi",
			"geoLat":       -3.5,
			"geoLng":       39.9,
			"tags":         []string{tag},
		})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", res.Code, res.Body.String())
	}
	if cacheControl := res.Header().Get("Cache-Control"); cacheControl != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cacheControl)
	}

	voucher, ok := result["voucher"].(map[string]any)
	if !ok {
		t.Fatalf("result.voucher = %#v, want object", result["voucher"])
	}
	for key, want := range map[string]any{
		"name":         "Malii Fresh",
		"contractName": offeringMarker + " Owned",
		"description":  "Fresh produce from the Kilifi stall",
		"voucherUoa":   "KES",
		"voucherValue": "12.5",
		"email":        "owner@example.org",
		"website":      "https://malii.example",
		"phone":        "+254700000000",
		"terms":        "Redeemable on market days.",
		"locationName": "Kilifi",
	} {
		if got := voucher[key]; got != want {
			t.Errorf("%s = %#v, want %#v", key, got, want)
		}
	}
	tags, ok := voucher["tags"].([]any)
	if !ok || len(tags) != 1 || tags[0] != tag {
		t.Errorf("tags = %#v, want [%s]", voucher["tags"], tag)
	}

	var contractName, symbol string
	if err := a.store.Pool().QueryRow(context.Background(), `
		SELECT name, symbol FROM vouchers WHERE id = $1`, fixtures.ownedID).
		Scan(&contractName, &symbol); err != nil {
		t.Fatalf("read updated voucher: %v", err)
	}
	if contractName != offeringMarker+" Owned" {
		t.Errorf("contract name = %q, want the deployed value", contractName)
	}
}

func TestVoucherProfileMutationRejectsNonOwnersAndRetiredVouchers(t *testing.T) {
	a, signingKey := newOfferingAPI(t)
	fixtures, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()

	body := map[string]any{"description": "Not allowed", "tags": []string{}}

	stranger := offeringToken(t, signingKey, offeringStrangerAddr, "USER")
	res, _ := offeringRequest(t, a, http.MethodPut,
		"/api/v2/vouchers/"+fixtures.ownedAddress+"/profile", stranger, body)
	if res.Code != http.StatusForbidden {
		t.Errorf("stranger status = %d, want 403 body=%s", res.Code, res.Body.String())
	}

	owner := offeringToken(t, signingKey, offeringOwnerAddress, "USER")
	res, _ = offeringRequest(t, a, http.MethodPut,
		"/api/v2/vouchers/"+fixtures.inactiveAddress+"/profile", owner, body)
	if res.Code != http.StatusConflict {
		t.Errorf("retired status = %d, want 409 body=%s", res.Code, res.Body.String())
	}

	// A label without a pin is the one shape the database would reject after the
	// request had been accepted, so it is named here first.
	res, _ = offeringRequest(t, a, http.MethodPut,
		"/api/v2/vouchers/"+fixtures.ownedAddress+"/profile", owner,
		map[string]any{"locationName": "Kilifi", "geoLat": -3.5})
	if res.Code != http.StatusBadRequest {
		t.Errorf("half pin status = %d, want 400 body=%s", res.Code, res.Body.String())
	}

	superadmin := offeringToken(t, signingKey, offeringOperatorAddr, "SUPERADMIN")
	res, _ = offeringRequest(t, a, http.MethodPut,
		"/api/v2/vouchers/"+fixtures.otherVoucherAddr+"/profile", superadmin,
		map[string]any{"description": "Steward correction"})
	if res.Code != http.StatusOK {
		t.Errorf("superadmin status = %d, want 200 body=%s", res.Code, res.Body.String())
	}
}
