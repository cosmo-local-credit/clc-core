package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/go-playground/validator/v10"
)

func TestDeployRequestsAcceptTermsURL(t *testing.T) {
	var pool DeployPoolRequest
	if err := json.Unmarshal([]byte(`{"terms":"https://example.org/terms"}`), &pool); err != nil {
		t.Fatal(err)
	}
	if pool.Terms != "https://example.org/terms" {
		t.Fatalf("pool terms = %q", pool.Terms)
	}

	var token DeployTokenRequest
	if err := json.Unmarshal([]byte(`{"terms":"https://example.org/terms"}`), &token); err != nil {
		t.Fatal(err)
	}
	if token.Terms != "https://example.org/terms" {
		t.Fatalf("token terms = %q", token.Terms)
	}
}

// Voucher and pool terms may be written in place as Markdown rather than linked.
func TestVoucherTermsAcceptMarkdown(t *testing.T) {
	v := validator.New(validator.WithRequiredStructEnabled())
	markdown := "# Terms\n\nRedeemable at our stall on market days."

	if err := v.Struct(&DeployTokenRequest{
		Name: "Kilifi Harvest", Symbol: "KHV", Decimals: "6",
		Owner: "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045",
		Terms: markdown,
	}); err != nil {
		t.Fatalf("deploy token markdown terms: %v", err)
	}
	if err := v.Struct(&DeployTokenRequest{
		Name: "Kilifi Harvest", Symbol: "KHV", Decimals: "6",
		Owner: "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045",
		Terms: strings.Repeat("a", 5001),
	}); err == nil {
		t.Fatal("deploy token terms over 5000 characters should be rejected")
	}

	if err := v.Struct(&UpdateVoucherProfileRequest{Terms: &markdown}); err != nil {
		t.Fatalf("update voucher markdown terms: %v", err)
	}

	if err := v.Struct(&DeployPoolRequest{
		Name: "Kilifi Pool", Symbol: "KLP", Decimals: "6",
		Owner:            "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045",
		FeePolicyDefault: "0", UnitOfAccount: "KES",
		QuoterType: "decimal",
		Terms:      markdown,
	}); err != nil {
		t.Fatalf("deploy pool markdown terms: %v", err)
	}
	if err := v.Struct(&UpdatePoolProfileRequest{Terms: &markdown}); err != nil {
		t.Fatalf("update pool markdown terms: %v", err)
	}
	if err := v.Struct(&DeployPoolRequest{
		Name: "Kilifi Pool", Symbol: "KLP", Decimals: "6",
		Owner:            "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045",
		FeePolicyDefault: "0", UnitOfAccount: "KES",
		QuoterType: "decimal",
		Terms:      strings.Repeat("a", 5001),
	}); err == nil {
		t.Fatal("deploy pool terms over 5000 characters should be rejected")
	}
}
