package api

import (
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/store"
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
	if update.UpdateLocation {
		t.Fatal("expected omitted location to preserve the stored pin")
	}
}

func TestProfileUpdateFromRequestReplacesLocation(t *testing.T) {
	update, err := profileUpdateFromRequest("0xabc", apiresp.UpdateProfileRequest{
		Location: &apiresp.ProfileLocation{
			LocationName: "  Sarajevo  ",
			Lat:          43.8563,
			Lng:          18.4131,
		},
	})
	if err != nil {
		t.Fatalf("profileUpdateFromRequest() error = %v", err)
	}
	if !update.UpdateLocation || update.LocationName != "Sarajevo" {
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
	if !update.UpdateLocation || update.GeoLat != nil || update.GeoLng != nil {
		t.Fatalf("unexpected clear update: %#v", update)
	}
}

func TestProfileUpdateFromRequestRejectsConflictingLocationCommands(t *testing.T) {
	_, err := profileUpdateFromRequest("0xabc", apiresp.UpdateProfileRequest{
		Location:      &apiresp.ProfileLocation{LocationName: "Sarajevo"},
		ClearLocation: true,
	})
	if err == nil {
		t.Fatal("expected conflicting location update to fail")
	}
}

func TestUserLocationResultRequiresCompletePin(t *testing.T) {
	lat := 43.8563
	lng := 18.4131
	if got := userLocationResult(store.User{LocationName: "Sarajevo", GeoLat: &lat, GeoLng: &lng}); got == nil {
		t.Fatal("expected complete pin to be returned")
	}
	if got := userLocationResult(store.User{LocationName: "Sarajevo", GeoLat: &lat}); got != nil {
		t.Fatalf("expected incomplete pin to be hidden, got %#v", got)
	}
}
