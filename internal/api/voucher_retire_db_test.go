package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRetiredVoucherPoolCompositionVisibility(t *testing.T) {
	a, priv := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()
	ctx := context.Background()
	operator := offeringToken(t, priv, offeringOperatorAddr, "SUPERADMIN")
	owner := offeringToken(t, priv, offeringOwnerAddress, "USER")
	var poolID uint64
	var address string
	require.NoError(t, a.store.Pool().QueryRow(ctx, `INSERT INTO pools(chain_id,address)
        VALUES(42220,'0x'||lpad(to_hex((random()*1e18)::bigint),40,'0')) RETURNING id,address`).Scan(&poolID, &address))
	defer func() {
		_, err := a.store.Pool().Exec(ctx, `DELETE FROM pool_tokens WHERE pool_id=$1`, poolID)
		require.NoError(t, err)
		_, err = a.store.Pool().Exec(ctx, `DELETE FROM pools WHERE id=$1`, poolID)
		require.NoError(t, err)
	}()
	_, err := a.store.Pool().Exec(ctx, `INSERT INTO pool_tokens(pool_id,voucher_id,is_allowed)
        SELECT $1,id,true FROM vouchers WHERE lower(address) IN (lower($2),lower($3))`, poolID, fx.ownedAddress, fx.inactiveAddress)
	require.NoError(t, err)
	for _, prefix := range []string{"/api/v2/pools/", "/api/v2/admin/pools/"} {
		admin := strings.Contains(prefix, "/admin/")
		token := ""
		if admin {
			token = operator
		}
		res, result := offeringRequest(t, a, http.MethodGet, prefix+address+"/vouchers", token, nil)
		require.Equal(t, http.StatusOK, res.Code, res.Body.String())
		vouchers, ok := result["vouchers"].([]any)
		require.True(t, ok)
		if admin {
			require.Len(t, vouchers, 2)
			require.Equal(t, float64(2), result["total"])
			require.Equal(t, "no-store", res.Header().Get("Cache-Control"))
		} else {
			require.Len(t, vouchers, 1)
			voucher, ok := vouchers[0].(map[string]any)
			require.True(t, ok)
			require.Equal(t, true, voucher["active"])
		}
	}
	res, result := offeringRequest(t, a, http.MethodGet, "/api/v2/admin/pools/"+address+"/vouchers?perPage=1", operator, nil)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	require.Equal(t, float64(2), result["total"])
	rows, ok := result["vouchers"].([]any)
	require.True(t, ok)
	require.Len(t, rows, 1)
	for _, token := range []string{"", owner} {
		res, _ := offeringRequest(t, a, http.MethodGet, "/api/v2/admin/pools/"+address+"/vouchers", token, nil)
		require.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, res.Code)
	}
	tx, err := a.store.Pool().Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	pool, err := a.store.GetPoolByAddress(ctx, tx, address)
	require.NoError(t, err)
	require.EqualValues(t, 1, pool.VoucherCount)
	// Filtering belongs before LIMIT in both paging directions.
	next, err := a.store.ListPoolVouchersNext(ctx, tx, address, 2147483647, 1)
	require.NoError(t, err)
	require.Len(t, next, 1)
	require.True(t, next[0].Active)
	previous, err := a.store.ListPoolVouchersPrevious(ctx, tx, address, 0, 1)
	require.NoError(t, err)
	require.Len(t, previous, 1)
	require.True(t, previous[0].Active)
}

func TestVoucherRestoreRejectsOfferQuotaAndRollsBack(t *testing.T) {
	a, priv := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()
	ctx := context.Background()
	operator := offeringToken(t, priv, offeringOperatorAddr, "SUPERADMIN")
	owner := offeringToken(t, priv, offeringOwnerAddress, "USER")
	_, err := a.store.Pool().Exec(ctx, `UPDATE users SET max_offers=0 WHERE lower(blockchain_address)=lower($1)`, offeringOwnerAddress)
	require.NoError(t, err)
	defer func() {
		_, err := a.store.Pool().Exec(ctx, `UPDATE users SET max_offers=100 WHERE lower(blockchain_address)=lower($1)`, offeringOwnerAddress)
		require.NoError(t, err)
	}()
	_, err = a.store.Pool().Exec(ctx, `INSERT INTO offerings(voucher_id,seller_address,commodity_name,commodity_type,description,quantity,available)
        VALUES($1,$2,'Restore quota','GOOD','',1,true)`, fx.ownedID, offeringOwnerAddress)
	require.NoError(t, err)
	res, _ := offeringRequest(t, a, http.MethodDelete, "/api/v2/vouchers/"+fx.ownedAddress, owner, nil)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	res, _ = offeringRequest(t, a, http.MethodPut, "/api/v2/admin/vouchers/"+fx.ownedAddress+"/restore", operator, nil)
	require.Equal(t, http.StatusTooManyRequests, res.Code, res.Body.String())
	var active bool
	require.NoError(t, a.store.Pool().QueryRow(ctx, `SELECT active FROM vouchers WHERE id=$1`, fx.ownedID).Scan(&active))
	require.False(t, active)
}

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
