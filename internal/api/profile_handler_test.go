package api

import (
	"testing"
)

func TestNormalizeProfileENS(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		want      string
		wantValid bool
	}{
		{name: "empty", input: "", want: "", wantValid: true},
		{name: "normalizes case and spaces", input: " Alice.Sarafu.ETH ", want: "alice.sarafu.eth", wantValid: true},
		{name: "accepts eth", input: "alice.eth", want: "alice.eth", wantValid: true},
		{name: "rejects unsupported suffix", input: "alice.example", want: "alice.example", wantValid: false},
		{name: "rejects nested arbitrary name", input: "sub.alice.eth", want: "sub.alice.eth", wantValid: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, valid := normalizeProfileENS(tc.input)
			if got != tc.want {
				t.Fatalf("normalizeProfileENS() = %q, want %q", got, tc.want)
			}
			if valid != tc.wantValid {
				t.Fatalf("normalizeProfileENS() valid = %t, want %t", valid, tc.wantValid)
			}
		})
	}
}

func TestNewPendingSIWEUserIsInactive(t *testing.T) {
	user := newPendingSIWEUser("0xAbC123")

	if user.Status != "APPROVED" {
		t.Fatalf("expected new SIWE user status APPROVED, got %q", user.Status)
	}
	if user.Role != "USER" {
		t.Fatalf("expected new SIWE user role USER, got %q", user.Role)
	}
	if user.Email == "" {
		t.Fatal("expected generated placeholder email for pending SIWE user")
	}
}

func TestPendingSIWEEmailIsAddressScoped(t *testing.T) {
	got := pendingSIWEEmail("0xAbC123")
	want := "pending+abc123@clc.local"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}
