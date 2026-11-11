package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

const (
	retireContentMarker = "Zqretirevisibility"
	minePost            = retireContentMarker + " mine"
	importedPost        = retireContentMarker + " imported"
)

func TestRetiringAccountContentVisibility(t *testing.T) {
	a, superToken := newAdminAPI(t)
	cleanup := func() {
		ctx := context.Background()
		for _, q := range []string{
			`DELETE FROM reports WHERE description = $1`,
			`DELETE FROM vouchers WHERE location_name = $1`,
		} {
			if _, err := a.store.Pool().Exec(ctx, q, retireContentMarker); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		}
		cleanupRoleTestUsers(t, a)
	}
	cleanup()
	t.Cleanup(cleanup)

	user := seedRoleTestUser(t, a, retireUserAddress, "USER", "APPROVED")
	ctx := context.Background()

	var voucherAddress string
	if err := a.store.Pool().QueryRow(ctx, `
		INSERT INTO vouchers (chain_id, address, name, symbol, decimals, active, geo_lat, geo_lng, location_name, owner_address)
		VALUES (42220, '0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'), $1, 'ZQR' || gen_random_uuid()::TEXT, 6, TRUE, -1.29, 36.82, $2, $3)
		RETURNING address`, retireContentMarker+" Voucher", retireContentMarker, strings.ToLower(retireUserAddress)).Scan(&voucherAddress); err != nil {
		t.Fatalf("seed voucher: %v", err)
	}
	// A post by an imported author with no local account must stay visible.
	for author, title := range map[string]string{
		retireUserAddress: minePost,
		"0x0000000000000000000000000000000000dEaD01": importedPost,
	} {
		if _, err := a.store.Pool().Exec(ctx, `INSERT INTO reports (author_address, title, description, status)
			VALUES ($1, $2, $3, 'APPROVED')`, author, title, retireContentMarker); err != nil {
			t.Fatalf("seed report: %v", err)
		}
	}

	posts := func() string {
		res, _ := getJSON(t, a, "", "/api/v2/reports?q="+retireContentMarker+"&perPage=50")
		if res.Code != http.StatusOK {
			t.Fatalf("reports = %d: %s", res.Code, res.Body.String())
		}
		return res.Body.String()
	}
	voucher := func() string {
		res, _ := getJSON(t, a, "", "/api/v2/vouchers/"+voucherAddress)
		if res.Code != http.StatusOK {
			t.Fatalf("voucher = %d: %s", res.Code, res.Body.String())
		}
		return res.Body.String()
	}

	if body := posts(); !strings.Contains(body, minePost) {
		t.Fatalf("author's post should be visible before retirement: %s", body)
	}
	if !strings.Contains(voucher(), `"managerClosure":null`) {
		t.Fatalf("active owner should show no closure: %s", voucher())
	}

	target := "/api/v2/admin/users/" + user.EthereumAddress
	if res, _ := sendJSON(t, a, http.MethodPost, target+"/retirement", superToken, `{"reason":"private reason"}`); res.Code != http.StatusOK {
		t.Fatalf("retire = %d: %s", res.Code, res.Body.String())
	}

	body := posts()
	if strings.Contains(body, minePost) {
		t.Fatalf("retiring author's post is still public: %s", body)
	}
	if !strings.Contains(body, importedPost) {
		t.Fatalf("imported author's post must stay visible: %s", body)
	}
	closure := voucher()
	if !strings.Contains(closure, `"managerClosure":{"state":"SCHEDULED"`) {
		t.Fatalf("voucher should show a scheduled closure: %s", closure)
	}
	for _, leak := range []string{"private reason", user.Name, "REJECTED", "closesAt\":\"\""} {
		if strings.Contains(closure, leak) {
			t.Fatalf("closure response leaks %q: %s", leak, closure)
		}
	}

	if res, _ := sendJSON(t, a, http.MethodPost, target+"/retirement/restore", superToken, ""); res.Code != http.StatusOK {
		t.Fatalf("restore = %d", res.Code)
	}
	if body := posts(); !strings.Contains(body, minePost) {
		t.Fatalf("restored author's post should be public again: %s", body)
	}
	if !strings.Contains(voucher(), `"managerClosure":null`) {
		t.Fatalf("restored owner should show no closure: %s", voucher())
	}
}
