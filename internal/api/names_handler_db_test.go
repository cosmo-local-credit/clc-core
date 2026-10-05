package api

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/google/uuid"
)

// Distinctive enough that the fixtures cannot collide with catalog rows a
// developer already has locally, since name occupancy is global.
const (
	nameLookupVoucher = "Zqnamelookup Voucher"
	nameLookupPool    = "Zqnamelookup Pool"
)

type (
	nameLookupFixtures struct {
		voucherAddress string
		poolAddress    string
	}
)

func seedNameLookupFixtures(t *testing.T, a *API) (nameLookupFixtures, func()) {
	t.Helper()
	ctx := context.Background()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin name fixtures: %v", err)
	}
	defer tx.Rollback(ctx)

	var keyID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO keystore (public_key, private_key)
		VALUES ('0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'), 'name-test-key')
		RETURNING id`).Scan(&keyID); err != nil {
		t.Fatalf("insert name fixture key: %v", err)
	}

	platformID := func(contractType string) int64 {
		t.Helper()
		var id int64
		if err := tx.QueryRow(ctx,
			`SELECT id FROM platform_contracts WHERE chain_id = 42220 AND contract_type = $1::contract_type`,
			contractType).Scan(&id); err != nil {
			t.Fatalf("select %s platform contract: %v", contractType, err)
		}
		return id
	}

	var otxIDs []int64
	insertOTX := func(otxType string) int64 {
		t.Helper()
		var id int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO otx (tracking_id, otx_type, signer_account, raw_tx, tx_hash, nonce)
			VALUES ($1, $2, $3, '0x00', $4, 0)
			RETURNING id`, uuid.NewString(), otxType, keyID, "0x"+uuid.NewString()).Scan(&id); err != nil {
			t.Fatalf("insert %s fixture otx: %v", otxType, err)
		}
		otxIDs = append(otxIDs, id)
		return id
	}

	var fixtures nameLookupFixtures
	admin := "0x33c573149db22e759fB9A38Bcc461C12855c2633"

	// The catalog voucher carries its label as a display_name override, so the
	// lookup has to resolve COALESCE(display_name, name) rather than name alone.
	var voucherIDs []int64
	var voucherID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO vouchers (chain_id, address, name, display_name, symbol, decimals, active, location_name)
		VALUES (42220, '0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'),
		        'Zqnamelookup Contract Name', $1, 'ZQNM', 6, true, 'Zqnamelookup')
		RETURNING id, address`, nameLookupVoucher).Scan(&voucherID, &fixtures.voucherAddress); err != nil {
		t.Fatalf("insert name fixture voucher: %v", err)
	}
	voucherIDs = append(voucherIDs, voucherID)

	tokenPlatformID := platformID("ERC20")
	insertTokenDeploy := func(name, status string) {
		t.Helper()
		otxID := insertOTX("TOKEN_PROXY_DEPLOY")
		if _, err := tx.Exec(ctx, `
			INSERT INTO deployed_tokens
			    (otx_id, platform_contract_id, status, symbol, name, decimals, admin)
			VALUES ($1, $2, $3, 'ZQNM' || gen_random_uuid()::TEXT, $4, 6, $5)`,
			otxID, tokenPlatformID, status, name, admin); err != nil {
			t.Fatalf("insert %s token name %q: %v", status, name, err)
		}
	}
	insertTokenDeploy("Zqnamelookup Pending", "PENDING")
	insertTokenDeploy("Zqnamelookup Failed", "FAILED")

	var poolIDs []int64
	var poolID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO pools (chain_id, address, name, symbol, decimals, location_name)
		VALUES (42220, '0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'),
		        $1, 'ZQNP', 6, 'Zqnamelookup')
		RETURNING id, address`, nameLookupPool).Scan(&poolID, &fixtures.poolAddress); err != nil {
		t.Fatalf("insert name fixture pool: %v", err)
	}
	poolIDs = append(poolIDs, poolID)

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit name fixtures: %v", err)
	}

	return fixtures, func() {
		cleanup, err := a.store.Pool().Begin(ctx)
		if err != nil {
			t.Errorf("begin name cleanup: %v", err)
			return
		}
		defer cleanup.Rollback(ctx)
		statements := []struct {
			query string
			arg   any
		}{
			{`DELETE FROM deployed_tokens WHERE otx_id = ANY($1::BIGINT[])`, otxIDs},
			{`DELETE FROM vouchers WHERE id = ANY($1::BIGINT[])`, voucherIDs},
			{`DELETE FROM pools WHERE id = ANY($1::BIGINT[])`, poolIDs},
			{`DELETE FROM otx WHERE id = ANY($1::BIGINT[])`, otxIDs},
			{`DELETE FROM noncestore WHERE keystore_id = $1`, keyID},
			{`DELETE FROM keystore WHERE id = $1`, keyID},
		}
		for _, statement := range statements {
			if _, err := cleanup.Exec(ctx, statement.query, statement.arg); err != nil {
				t.Errorf("name cleanup %q: %v", statement.query, err)
				return
			}
		}
		if err := cleanup.Commit(ctx); err != nil {
			t.Errorf("commit name cleanup: %v", err)
		}
	}
}

func nameLookupTarget(kind, name, exclude string) string {
	q := url.Values{}
	q.Set("kind", kind)
	q.Set("name", name)
	if exclude != "" {
		q.Set("exclude", exclude)
	}
	return "/api/v2/names?" + q.Encode()
}

func TestNamesLookupReadsCatalogAndOpenDeployments(t *testing.T) {
	a, signingKey := newOfferingAPI(t)
	fixtures, cleanup := seedNameLookupFixtures(t, a)
	defer cleanup()
	token := offeringToken(t, signingKey, offeringOwnerAddress, "USER")

	tests := []struct {
		label     string
		kind      string
		name      string
		exclude   string
		available bool
	}{
		{label: "catalog display name", kind: "voucher", name: nameLookupVoucher, available: false},
		// Case and padding fold away, so the wizard's placeholder cannot be
		// re-submitted with cosmetic edits.
		{label: "case insensitive", kind: "voucher", name: "zqnamelookup voucher", available: false},
		{label: "whitespace collapsed", kind: "voucher", name: "  Zqnamelookup   Voucher  ", available: false},
		// The override shadows the contract name; only the label users see is taken.
		{label: "shadowed contract name", kind: "voucher", name: "Zqnamelookup Contract Name", available: true},
		{label: "open deploy", kind: "voucher", name: "Zqnamelookup Pending", available: false},
		{label: "failed deploy is reusable", kind: "voucher", name: "Zqnamelookup Failed", available: true},
		{label: "free name", kind: "voucher", name: "Zqnamelookup Unused", available: true},
		// Voucher labels are separate from pool recipient names.
		{label: "pool name from voucher kind", kind: "voucher", name: nameLookupPool, available: true},
		{label: "pool name", kind: "pool", name: nameLookupPool, available: false},
		{label: "voucher name from pool kind", kind: "pool", name: nameLookupVoucher, available: true},
		// A rename form checking the value already in its field.
		{label: "excluded self", kind: "voucher", name: nameLookupVoucher,
			exclude: fixtures.voucherAddress, available: true},
		{label: "excluded self pool", kind: "pool", name: nameLookupPool,
			exclude: fixtures.poolAddress, available: true},
	}
	for _, tc := range tests {
		t.Run(tc.label, func(t *testing.T) {
			res, result := offeringRequest(t, a, http.MethodGet,
				nameLookupTarget(tc.kind, tc.name, tc.exclude), token, nil)
			if res.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 body=%s", res.Code, res.Body.String())
			}
			if got := result["kind"]; got != tc.kind {
				t.Errorf("kind = %#v, want %q", got, tc.kind)
			}
			if got := result["available"]; got != tc.available {
				t.Errorf("available = %#v, want %v", got, tc.available)
			}
		})
	}
}

func TestNamesLookupEchoesTheNormalizedName(t *testing.T) {
	a, signingKey := newOfferingAPI(t)
	_, cleanup := seedNameLookupFixtures(t, a)
	defer cleanup()
	token := offeringToken(t, signingKey, offeringOwnerAddress, "USER")

	res, result := offeringRequest(t, a, http.MethodGet,
		nameLookupTarget("voucher", "  Zqnamelookup   Unused  ", ""), token, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", res.Code, res.Body.String())
	}
	if got := result["name"]; got != "Zqnamelookup Unused" {
		t.Errorf("name = %#v, want the trimmed and collapsed form", got)
	}
}

func TestNamesLookupValidatesAndRequiresAuthentication(t *testing.T) {
	a, signingKey := newOfferingAPI(t)
	token := offeringToken(t, signingKey, offeringOwnerAddress, "USER")

	longName := make([]byte, 121)
	for i := range longName {
		longName[i] = 'a'
	}

	for _, target := range []string{
		"/api/v2/names",
		nameLookupTarget("token", "Something", ""),
		nameLookupTarget("voucher", "", ""),
		nameLookupTarget("voucher", "   ", ""),
		// Padding must not buy extra characters against the cap.
		nameLookupTarget("voucher", "  "+string(longName)+"  ", ""),
		// 81 characters is fine for a voucher and over the cap for a pool.
		nameLookupTarget("pool", string(longName[:81]), ""),
		nameLookupTarget("voucher", "Something", "not-an-address"),
	} {
		res, _ := offeringRequest(t, a, http.MethodGet, target, token, nil)
		if res.Code != http.StatusBadRequest {
			t.Errorf("%s status = %d, want 400 body=%s", target, res.Code, res.Body.String())
		}
	}

	res, _ := offeringRequest(t, a, http.MethodGet,
		nameLookupTarget("voucher", "Something", ""), "", nil)
	if res.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated status = %d, want 401 body=%s", res.Code, res.Body.String())
	}
}
