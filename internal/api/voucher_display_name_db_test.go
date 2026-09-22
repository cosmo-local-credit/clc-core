package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// The owner-facing label mutation shares the offering fixture because both
// authorize from the voucher's current owner. These are real route, auth and
// database tests; newOfferingAPI skips them unless CORE_TEST_DSN is set.
func TestVoucherOwnerCanUpdateTheDisplayNameWithoutChangingTheContractName(t *testing.T) {
	a, signingKey := newOfferingAPI(t)
	fixtures, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()

	owner := offeringToken(t, signingKey, offeringOwnerAddress, "USER")
	res, result := offeringRequest(t, a, http.MethodPut,
		"/api/v2/vouchers/"+fixtures.ownedAddress+"/display-name", owner,
		map[string]any{"displayName": "  Updated community label  "})
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
	if got := voucher["name"]; got != "Updated community label" {
		t.Errorf("response name = %v, want the trimmed display label", got)
	}

	var contractName string
	var displayName *string
	if err := a.store.Pool().QueryRow(context.Background(), `
		SELECT name, display_name FROM vouchers WHERE id = $1`, fixtures.ownedID).
		Scan(&contractName, &displayName); err != nil {
		t.Fatalf("read updated voucher: %v", err)
	}
	if contractName != offeringMarker+" Owned" {
		t.Errorf("contract name = %q, want the deployed value", contractName)
	}
	if displayName == nil || *displayName != "Updated community label" {
		t.Errorf("display name = %v, want the trimmed display label", displayName)
	}
}

func TestVoucherDisplayNameMutationRejectsNonOwnersAndRetiredVouchers(t *testing.T) {
	a, signingKey := newOfferingAPI(t)
	fixtures, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()

	stranger := offeringToken(t, signingKey, offeringStrangerAddr, "USER")
	res, _ := offeringRequest(t, a, http.MethodPut,
		"/api/v2/vouchers/"+fixtures.ownedAddress+"/display-name", stranger,
		map[string]any{"displayName": "Not allowed"})
	if res.Code != http.StatusForbidden {
		t.Errorf("stranger status = %d, want 403 body=%s", res.Code, res.Body.String())
	}

	owner := offeringToken(t, signingKey, offeringOwnerAddress, "USER")
	res, _ = offeringRequest(t, a, http.MethodPut,
		"/api/v2/vouchers/"+fixtures.inactiveAddress+"/display-name", owner,
		map[string]any{"displayName": "Retired voucher"})
	if res.Code != http.StatusConflict {
		t.Errorf("retired status = %d, want 409 body=%s", res.Code, res.Body.String())
	}

	// A superadmin may correct an imported or abandoned record even when their
	// wallet is not the owner. That is the same steward escape hatch as the
	// existing catalog-profile route, not an implied ownership transfer.
	superadmin := offeringToken(t, signingKey, offeringOperatorAddr, "SUPERADMIN")
	res, _ = offeringRequest(t, a, http.MethodPut,
		"/api/v2/vouchers/"+fixtures.otherVoucherAddr+"/display-name", superadmin,
		map[string]any{"displayName": "Steward correction"})
	if res.Code != http.StatusOK {
		t.Errorf("superadmin status = %d, want 200 body=%s", res.Code, res.Body.String())
	}
}

func TestVoucherDisplayNameMutationRejectsBlankLabels(t *testing.T) {
	a, signingKey := newOfferingAPI(t)
	fixtures, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()

	owner := offeringToken(t, signingKey, offeringOwnerAddress, "USER")
	res, _ := offeringRequest(t, a, http.MethodPut,
		"/api/v2/vouchers/"+fixtures.ownedAddress+"/display-name", owner,
		map[string]any{"displayName": "   "})
	if res.Code != http.StatusBadRequest {
		t.Errorf("blank status = %d, want 400 body=%s", res.Code, res.Body.String())
	}
}

// The label is what catalog reads show, so two vouchers carrying the same one is
// exactly the ambiguity the deploy-time check exists to prevent; a rename must
// not be a way around it.
func TestVoucherDisplayNameMutationRejectsALabelAnotherVoucherHolds(t *testing.T) {
	a, signingKey := newOfferingAPI(t)
	fixtures, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()
	owner := offeringToken(t, signingKey, offeringOwnerAddress, "USER")

	// Case and padding differ; normalization has to see through both.
	for _, label := range []string{
		offeringMarker + " Other",
		"  " + strings.ToLower(offeringMarker) + "   other  ",
	} {
		res, _ := offeringRequest(t, a, http.MethodPut,
			"/api/v2/vouchers/"+fixtures.ownedAddress+"/display-name", owner,
			map[string]any{"displayName": label})
		if res.Code != http.StatusConflict {
			t.Errorf("%q status = %d, want 409 body=%s", label, res.Code, res.Body.String())
		}
	}

	var displayName *string
	if err := a.store.Pool().QueryRow(context.Background(), `
		SELECT display_name FROM vouchers WHERE id = $1`, fixtures.ownedID).Scan(&displayName); err != nil {
		t.Fatalf("read voucher after rejected rename: %v", err)
	}
	if displayName != nil {
		t.Errorf("display name = %v, want the rejected rename to have left it unset", *displayName)
	}
}

// Re-saving the label a voucher already holds is not a conflict with itself,
// and neither is resetting it to the contract name its own deploy row carries.
func TestVoucherDisplayNameMutationAcceptsTheVouchersOwnLabel(t *testing.T) {
	a, signingKey := newOfferingAPI(t)
	fixtures, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()
	owner := offeringToken(t, signingKey, offeringOwnerAddress, "USER")

	target := "/api/v2/vouchers/" + fixtures.ownedAddress + "/display-name"
	for _, label := range []string{
		"Zqdisplayname Reused Label",
		"Zqdisplayname Reused Label",
		offeringMarker + " Owned", // the contract name on this voucher's own row
	} {
		res, _ := offeringRequest(t, a, http.MethodPut, target, owner,
			map[string]any{"displayName": label})
		if res.Code != http.StatusOK {
			t.Fatalf("%q status = %d, want 200 body=%s", label, res.Code, res.Body.String())
		}
	}
}
