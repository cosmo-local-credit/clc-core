package api

import (
	"testing"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
)

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

func TestProfileUpdateFromRequestPreservesLocationWhenOmitted(t *testing.T) {
	update, err := profileUpdateFromRequest("0xabc", apiresp.UpdateProfileRequest{Name: "Alice"})
	if err != nil {
		t.Fatalf("profileUpdateFromRequest() error = %v", err)
	}
	if update.ClearLocation || update.LocationName != nil || update.GeoLat != nil || update.GeoLng != nil {
		t.Fatalf("expected omitted location to preserve the stored pin, got %#v", update)
	}
}

func TestProfileUpdateFromRequestReplacesLocation(t *testing.T) {
	locationName := "  Sarajevo  "
	lat := 43.8563
	lng := 18.4131
	update, err := profileUpdateFromRequest("0xabc", apiresp.UpdateProfileRequest{
		LocationName: &locationName,
		GeoLat:       &lat,
		GeoLng:       &lng,
	})
	if err != nil {
		t.Fatalf("profileUpdateFromRequest() error = %v", err)
	}
	if update.LocationName == nil || *update.LocationName != "Sarajevo" {
		t.Fatalf("unexpected update: %#v", update)
	}
	if update.GeoLat == nil || *update.GeoLat != 43.8563 || update.GeoLng == nil || *update.GeoLng != 18.4131 {
		t.Fatalf("unexpected coordinates: %#v", update)
	}
}

func TestProfileUpdateFromRequestClearsLocation(t *testing.T) {
	update, err := profileUpdateFromRequest("0xabc", apiresp.UpdateProfileRequest{ClearLocation: true})
	if err != nil {
		t.Fatalf("profileUpdateFromRequest() error = %v", err)
	}
	if !update.ClearLocation || update.LocationName != nil || update.GeoLat != nil || update.GeoLng != nil {
		t.Fatalf("unexpected clear update: %#v", update)
	}
}

func TestProfileUpdateFromRequestTrimsProfilePreferences(t *testing.T) {
	currency := " kes "
	bio := "  Neighbourhood organiser  "
	website := "  https://alice.example  "
	update, err := profileUpdateFromRequest("0xabc", apiresp.UpdateProfileRequest{
		PreferredCurrency: &currency,
		Bio:               &bio,
		Website:           &website,
	})
	if err != nil {
		t.Fatalf("profileUpdateFromRequest() error = %v", err)
	}
	if update.PreferredCurrency == nil || *update.PreferredCurrency != "KES" {
		t.Fatalf("preferred currency = %v, want KES", update.PreferredCurrency)
	}
	if update.Bio == nil || *update.Bio != "Neighbourhood organiser" {
		t.Fatalf("bio = %v, want trimmed value", update.Bio)
	}
	if update.Website == nil || *update.Website != "https://alice.example" {
		t.Fatalf("website = %v, want trimmed value", update.Website)
	}
}

func TestProfileUpdateFromRequestRejectsConflictingLocationCommands(t *testing.T) {
	locationName := "Sarajevo"
	_, err := profileUpdateFromRequest("0xabc", apiresp.UpdateProfileRequest{
		LocationName:  &locationName,
		ClearLocation: true,
	})
	if err == nil {
		t.Fatal("expected conflicting location update to fail")
	}
}

func TestProfileUpdateFromRequestRejectsPartialCoordinates(t *testing.T) {
	lat := 43.8563
	_, err := profileUpdateFromRequest("0xabc", apiresp.UpdateProfileRequest{GeoLat: &lat})
	if err == nil {
		t.Fatal("expected a partial coordinate pair to fail")
	}
}
