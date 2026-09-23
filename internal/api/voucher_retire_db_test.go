package api

import (
	"net/http"
	"strings"
	"testing"
)

// End-to-end coverage of the retire/restore lifecycle through the real route
// table, auth middleware, store and database. Skipped unless CORE_TEST_DSN is
// set, like the offering CRUD suite whose fixtures it borrows:
//
//	CORE_TEST_DSN=postgres://postgres:postgres@127.0.0.1:5432/clc_core go test ./internal/api/

// TestVoucherRetireAuthorization pins who may take a voucher out of the catalog.
//
// Fixtures are re-seeded per case because retiring is not idempotent: a second
// request against a voucher an earlier case retired would answer 409, and the
// table would then be asserting the order it happens to run in.
func TestVoucherRetireAuthorization(t *testing.T) {
	a, priv := newOfferingAPI(t)

	for name, tc := range map[string]struct {
		address func(offeringFixtures) string
		token   string
		want    int
	}{
		"owner": {
			func(fx offeringFixtures) string { return fx.ownedAddress },
			offeringToken(t, priv, offeringOwnerAddress, "USER"),
			http.StatusOK,
		},
		"owner in another casing": {
			func(fx offeringFixtures) string { return fx.ownedAddress },
			offeringToken(t, priv, strings.ToLower(offeringOwnerAddress), "USER"),
			http.StatusOK,
		},
		"superadmin on someone else's voucher": {
			func(fx offeringFixtures) string { return fx.otherVoucherAddr },
			offeringToken(t, priv, offeringOperatorAddr, "SUPERADMIN"),
			http.StatusOK,
		},
		"superadmin on an ownerless imported voucher": {
			func(fx offeringFixtures) string { return fx.ownerlessAddress },
			offeringToken(t, priv, offeringOperatorAddr, "SUPERADMIN"),
			http.StatusOK,
		},
		"a different signed-in user": {
			func(fx offeringFixtures) string { return fx.ownedAddress },
			offeringToken(t, priv, offeringStrangerAddr, "USER"),
			http.StatusForbidden,
		},
		"a user on an ownerless voucher": {
			func(fx offeringFixtures) string { return fx.ownerlessAddress },
			offeringToken(t, priv, offeringOwnerAddress, "USER"),
			http.StatusForbidden,
		},
		"no session": {
			func(fx offeringFixtures) string { return fx.ownedAddress },
			"",
			http.StatusUnauthorized,
		},
		"a service credential": {
			func(fx offeringFixtures) string { return fx.ownedAddress },
			offeringServiceToken(t, priv),
			http.StatusForbidden,
		},
		"a voucher that is already retired": {
			func(fx offeringFixtures) string { return fx.inactiveAddress },
			offeringToken(t, priv, offeringOwnerAddress, "USER"),
			http.StatusConflict,
		},
		"a voucher that does not exist": {
			func(offeringFixtures) string { return "0x99f573149db22e759fB9A38Bcc461C12855c2699" },
			offeringToken(t, priv, offeringOwnerAddress, "USER"),
			http.StatusNotFound,
		},
	} {
		t.Run(name, func(t *testing.T) {
			fx, cleanup := seedOfferingFixtures(t, a)
			defer cleanup()

			res, _ := offeringRequest(t, a, http.MethodDelete, "/api/v2/vouchers/"+tc.address(fx), tc.token, nil)
			if res.Code != tc.want {
				t.Fatalf("status = %d, want %d body=%s", res.Code, tc.want, res.Body.String())
			}
		})
	}
}

// TestRetiredVoucherLeavesThePublicCatalogAndComesBack is the reason the console
// reads exist. A retired voucher must be gone from every public surface and still
// reachable by an operator, or the retire is a one-way door out of the product.
func TestRetiredVoucherLeavesThePublicCatalogAndComesBack(t *testing.T) {
	a, priv := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()

	owner := offeringToken(t, priv, offeringOwnerAddress, "USER")
	operator := offeringToken(t, priv, offeringOperatorAddr, "SUPERADMIN")

	// Visible to the public before the retire.
	if res, _ := offeringRequest(t, a, http.MethodGet, "/api/v2/vouchers/"+fx.ownedAddress, "", nil); res.Code != http.StatusOK {
		t.Fatalf("public detail before retire = %d, want 200 body=%s", res.Code, res.Body.String())
	}

	if res, _ := offeringRequest(t, a, http.MethodDelete, "/api/v2/vouchers/"+fx.ownedAddress, owner, nil); res.Code != http.StatusOK {
		t.Fatalf("retire = %d, want 200 body=%s", res.Code, res.Body.String())
	}

	// Gone from the public detail, and indistinguishable from an address the
	// catalog never held.
	res, _ := offeringRequest(t, a, http.MethodGet, "/api/v2/vouchers/"+fx.ownedAddress, "", nil)
	if res.Code != http.StatusNotFound {
		t.Fatalf("public detail after retire = %d, want 404 body=%s", res.Code, res.Body.String())
	}
	unknown, _ := offeringRequest(t, a, http.MethodGet, "/api/v2/vouchers/0x99f573149db22e759fB9A38Bcc461C12855c2699", "", nil)
	if res.Body.String() != unknown.Body.String() {
		t.Fatalf("retired 404 body %q must match the unknown-address body %q", res.Body.String(), unknown.Body.String())
	}

	// Gone from the public list too.
	if listed := publicCatalogHolds(t, a, fx.ownedAddress); listed {
		t.Fatal("a retired voucher must not appear in the public market list")
	}

	// Still reachable by an operator, and reported as retired.
	detail, result := offeringRequest(t, a, http.MethodGet, "/api/v2/admin/vouchers/"+fx.ownedAddress, operator, nil)
	if detail.Code != http.StatusOK {
		t.Fatalf("admin detail after retire = %d, want 200 body=%s", detail.Code, detail.Body.String())
	}
	voucher, _ := result["voucher"].(map[string]any)
	if active, _ := voucher["active"].(bool); active {
		t.Fatal("admin detail must report a retired voucher as inactive")
	}

	// And listed under the retired filter, which is the restore queue.
	if !adminCatalogHolds(t, a, operator, "status=retired", fx.ownedAddress) {
		t.Fatal("a retired voucher must appear under status=retired")
	}
	if adminCatalogHolds(t, a, operator, "status=active", fx.ownedAddress) {
		t.Fatal("a retired voucher must not appear under status=active")
	}

	// Restore puts it back everywhere.
	restored, restoredResult := offeringRequest(t, a,
		http.MethodPut, "/api/v2/admin/vouchers/"+fx.ownedAddress+"/restore", operator, nil)
	if restored.Code != http.StatusOK {
		t.Fatalf("restore = %d, want 200 body=%s", restored.Code, restored.Body.String())
	}
	restoredVoucher, _ := restoredResult["voucher"].(map[string]any)
	if active, _ := restoredVoucher["active"].(bool); !active {
		t.Fatal("restore must answer with the voucher it reactivated")
	}
	if res, _ := offeringRequest(t, a, http.MethodGet, "/api/v2/vouchers/"+fx.ownedAddress, "", nil); res.Code != http.StatusOK {
		t.Fatalf("public detail after restore = %d, want 200 body=%s", res.Code, res.Body.String())
	}
}

// TestConsoleVoucherReadsRequireSuperAdmin keeps the wider view behind the role
// that justifies it. These routes answer for rows the public contract hides, so
// a plain session reaching them would undo the retire rather than scope it.
func TestConsoleVoucherReadsRequireSuperAdmin(t *testing.T) {
	a, priv := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()

	user := offeringToken(t, priv, offeringOwnerAddress, "USER")

	for name, target := range map[string]string{
		"list":    "/api/v2/admin/vouchers",
		"detail":  "/api/v2/admin/vouchers/" + fx.inactiveAddress,
		"restore": "/api/v2/admin/vouchers/" + fx.inactiveAddress + "/restore",
	} {
		t.Run(name, func(t *testing.T) {
			method := http.MethodGet
			if name == "restore" {
				method = http.MethodPut
			}
			if res, _ := offeringRequest(t, a, method, target, user, nil); res.Code != http.StatusForbidden {
				t.Fatalf("%s as a plain user = %d, want 403 body=%s", name, res.Code, res.Body.String())
			}
			if res, _ := offeringRequest(t, a, method, target, "", nil); res.Code != http.StatusUnauthorized {
				t.Fatalf("%s with no session = %d, want 401 body=%s", name, res.Code, res.Body.String())
			}
		})
	}
}

// TestAdminVoucherStatusRejectsUnknownValues keeps the filter a closed set: an
// unrecognised value must not quietly widen the page to every voucher.
func TestAdminVoucherStatusRejectsUnknownValues(t *testing.T) {
	a, priv := newOfferingAPI(t)
	operator := offeringToken(t, priv, offeringOperatorAddr, "SUPERADMIN")

	res, _ := offeringRequest(t, a, http.MethodGet, "/api/v2/admin/vouchers?status=hidden", operator, nil)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=hidden = %d, want 400 body=%s", res.Code, res.Body.String())
	}
}

// publicCatalogHolds reports whether the public market list carries an address.
func publicCatalogHolds(t *testing.T, a *API, address string) bool {
	t.Helper()
	res, result := offeringRequest(t, a, http.MethodGet, "/api/v2/vouchers?perPage=500", "", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("public list = %d, want 200 body=%s", res.Code, res.Body.String())
	}
	return catalogPageHolds(result, address)
}

// adminCatalogHolds reports whether a console page carries an address.
func adminCatalogHolds(t *testing.T, a *API, token, query, address string) bool {
	t.Helper()
	res, result := offeringRequest(t, a, http.MethodGet, "/api/v2/admin/vouchers?perPage=100&"+query, token, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("admin list (%s) = %d, want 200 body=%s", query, res.Code, res.Body.String())
	}
	return catalogPageHolds(result, address)
}

func catalogPageHolds(result map[string]any, address string) bool {
	rows, _ := result["vouchers"].([]any)
	for _, row := range rows {
		voucher, _ := row.(map[string]any)
		if listed, _ := voucher["address"].(string); strings.EqualFold(listed, address) {
			return true
		}
	}
	return false
}
