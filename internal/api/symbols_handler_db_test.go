package api

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
)

const (
	symbolLookupMarker = "Zqsymbollookup"
)

func seedSymbolLookupFixtures(t *testing.T, a *API) func() {
	t.Helper()
	ctx := context.Background()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin symbol fixtures: %v", err)
	}
	defer tx.Rollback(ctx)

	var keyID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO keystore (public_key, private_key)
		VALUES ('0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'), 'symbol-test-key')
		RETURNING id`).Scan(&keyID); err != nil {
		t.Fatalf("insert symbol fixture key: %v", err)
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
		trackingID := uuid.NewString()
		if err := tx.QueryRow(ctx, `
			INSERT INTO otx (tracking_id, otx_type, signer_account, raw_tx, tx_hash, nonce)
			VALUES ($1, $2, $3, '0x00', $4, 0)
			RETURNING id`, trackingID, otxType, keyID, "0x"+uuid.NewString()).Scan(&id); err != nil {
			t.Fatalf("insert %s fixture otx: %v", otxType, err)
		}
		otxIDs = append(otxIDs, id)
		return id
	}

	var voucherIDs []int64
	insertVoucher := func(symbol string, active bool) {
		t.Helper()
		var id int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO vouchers (chain_id, address, name, symbol, decimals, active, location_name)
			VALUES (42220, '0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'),
			        $1, $2, 6, $3, $1)
			RETURNING id`, symbolLookupMarker, symbol, active).Scan(&id); err != nil {
			t.Fatalf("insert voucher symbol %s: %v", symbol, err)
		}
		voucherIDs = append(voucherIDs, id)
	}
	insertVoucher("ZQSY", true)
	insertVoucher("zqsy1", false)
	insertVoucher("ZQSYA", true)

	admin := "0x33c573149db22e759fB9A38Bcc461C12855c2633"
	tokenPlatformID := platformID("ERC20")
	insertTokenDeploy := func(symbol, status string) {
		t.Helper()
		otxID := insertOTX("TOKEN_PROXY_DEPLOY")
		if _, err := tx.Exec(ctx, `
			INSERT INTO deployed_tokens
			    (otx_id, platform_contract_id, status, symbol, name, decimals, admin)
			VALUES ($1, $2, $3, $4, $5, 6, $6)`,
			otxID, tokenPlatformID, status, symbol, symbolLookupMarker, admin); err != nil {
			t.Fatalf("insert %s token symbol %s: %v", status, symbol, err)
		}
	}
	// A distinct pending deployment cannot claim an existing catalog symbol.
	insertTokenDeploy("ZQSY2", "PENDING")
	insertTokenDeploy("ZQSY3", "FAILED")

	var poolIDs []int64
	insertPool := func(symbol string) {
		t.Helper()
		var id int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO pools (chain_id, address, name, symbol, decimals, location_name)
			VALUES (42220, '0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'),
			        $1, $2, 6, $1)
			RETURNING id`, symbolLookupMarker, symbol).Scan(&id); err != nil {
			t.Fatalf("insert pool symbol %s: %v", symbol, err)
		}
		poolIDs = append(poolIDs, id)
	}
	insertPool("ZQPL")

	poolPlatformID := platformID("SWAPPOOL")
	insertPoolDeploy := func(symbol, status string) {
		t.Helper()
		otxID := insertOTX("POOL_PROXY_DEPLOY")
		if _, err := tx.Exec(ctx, `
			INSERT INTO deployed_pools (
			    otx_id, platform_contract_id, status, name, symbol, decimals,
			    token_registry_address, limiter_address, fee_policy_address, quoter_address,
			    protocol_fee_controller_address, fees_decoupled, admin
			) VALUES (
			    $1, $2, $3, $4, $5, 6,
			    $6, $6, $6, $6, $6, false, $6
		)`, otxID, poolPlatformID, status, symbolLookupMarker+" "+symbol, symbol, admin); err != nil {
			t.Fatalf("insert %s pool symbol %s: %v", status, symbol, err)
		}
	}
	insertPoolDeploy("ZQPL1", "PENDING")
	insertPoolDeploy("ZQPL2", "FAILED")

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit symbol fixtures: %v", err)
	}

	return func() {
		cleanup, err := a.store.Pool().Begin(ctx)
		if err != nil {
			t.Errorf("begin symbol cleanup: %v", err)
			return
		}
		defer cleanup.Rollback(ctx)
		statements := []struct {
			query string
			arg   any
		}{
			{`DELETE FROM deployed_tokens WHERE otx_id = ANY($1::BIGINT[])`, otxIDs},
			{`DELETE FROM deployed_pools WHERE otx_id = ANY($1::BIGINT[])`, otxIDs},
			{`DELETE FROM vouchers WHERE id = ANY($1::BIGINT[])`, voucherIDs},
			{`DELETE FROM pools WHERE id = ANY($1::BIGINT[])`, poolIDs},
			{`DELETE FROM otx WHERE id = ANY($1::BIGINT[])`, otxIDs},
			{`DELETE FROM noncestore WHERE keystore_id = $1`, keyID},
			{`DELETE FROM keystore WHERE id = $1`, keyID},
		}
		for _, statement := range statements {
			if _, err := cleanup.Exec(ctx, statement.query, statement.arg); err != nil {
				t.Errorf("symbol cleanup %q: %v", statement.query, err)
				return
			}
		}
		if err := cleanup.Commit(ctx); err != nil {
			t.Errorf("commit symbol cleanup: %v", err)
		}
	}
}

func symbolStrings(t *testing.T, value any) []string {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("taken = %#v, want array", value)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		symbol, ok := item.(string)
		if !ok {
			t.Fatalf("taken item = %#v, want string", item)
		}
		out = append(out, symbol)
	}
	return out
}

func TestSymbolsLookupReadsCatalogAndOpenDeployments(t *testing.T) {
	a, signingKey := newOfferingAPI(t)
	cleanup := seedSymbolLookupFixtures(t, a)
	defer cleanup()
	token := offeringToken(t, signingKey, offeringOwnerAddress, "USER")

	tests := []struct {
		kind   string
		prefix string
		want   []string
	}{
		{kind: "voucher", prefix: "zqsy", want: []string{"ZQSY", "ZQSY1", "ZQSY2"}},
		{kind: "pool", prefix: "ZQPL", want: []string{"ZQPL", "ZQPL1"}},
		// Both creation flows must see claims from the shared symbol namespace.
		{kind: "voucher", prefix: "ZQPL", want: []string{"ZQPL", "ZQPL1"}},
		{kind: "pool", prefix: "ZQSY", want: []string{"ZQSY", "ZQSY1", "ZQSY2"}},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("%s_%s", tc.kind, tc.prefix), func(t *testing.T) {
			res, result := offeringRequest(t, a, http.MethodGet,
				"/api/v2/symbols?kind="+tc.kind+"&prefix="+tc.prefix, token, nil)
			if res.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 body=%s", res.Code, res.Body.String())
			}
			if got := result["kind"]; got != tc.kind {
				t.Errorf("kind = %#v, want %q", got, tc.kind)
			}
			if got := result["prefix"]; got != strings.ToUpper(tc.prefix) {
				t.Errorf("prefix = %#v, want %q", got, strings.ToUpper(tc.prefix))
			}
			if got := symbolStrings(t, result["taken"]); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("taken = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSymbolsLookupValidatesAndRequiresAuthentication(t *testing.T) {
	a, signingKey := newOfferingAPI(t)
	token := offeringToken(t, signingKey, offeringOwnerAddress, "USER")

	for _, target := range []string{
		"/api/v2/symbols",
		"/api/v2/symbols?kind=token&prefix=ZQSY",
		"/api/v2/symbols?kind=voucher&prefix=ZQ-SY",
		"/api/v2/symbols?kind=voucher&prefix=TOOLONG",
	} {
		res, _ := offeringRequest(t, a, http.MethodGet, target, token, nil)
		if res.Code != http.StatusBadRequest {
			t.Errorf("%s status = %d, want 400 body=%s", target, res.Code, res.Body.String())
		}
	}

	res, _ := offeringRequest(t, a, http.MethodGet,
		"/api/v2/symbols?kind=voucher&prefix=ZQSY", "", nil)
	if res.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated status = %d, want 401 body=%s", res.Code, res.Body.String())
	}
}
