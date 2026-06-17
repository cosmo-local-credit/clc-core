package api

import "testing"

func TestNewPendingSIWEUserIsInactive(t *testing.T) {
	user := newPendingSIWEUser("0xAbC123")

	if user.Status != "PENDING" {
		t.Fatalf("expected new SIWE user status PENDING, got %q", user.Status)
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
