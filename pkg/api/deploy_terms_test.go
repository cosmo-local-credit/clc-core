package api

import (
	"encoding/json"
	"testing"
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
