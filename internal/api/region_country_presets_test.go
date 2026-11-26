package api

import (
	"encoding/json"
	"testing"
)

func TestRegionCountryPresets(t *testing.T) {
	countries, err := readRegionCountries()
	if err != nil {
		t.Fatal(err)
	}
	if len(countries) != 248 {
		t.Fatalf("expected 248 ISO presets, got %d", len(countries))
	}
	seen := make(map[string]bool, len(countries))
	for _, country := range countries {
		if len(country.Code) != 3 || seen[country.Code] || country.Name == "" {
			t.Fatalf("invalid country metadata: %+v", country.regionCountryName)
		}
		seen[country.Code] = true
		var boundary struct {
			Type        string           `json:"type"`
			Coordinates [][][][2]float64 `json:"coordinates"`
		}
		if err := json.Unmarshal(country.Boundary, &boundary); err != nil || boundary.Type != "MultiPolygon" || len(boundary.Coordinates) == 0 {
			t.Fatalf("invalid preset for %s: %v", country.Code, err)
		}
	}
	bhutan, found, err := lookupRegionCountry("BTN")
	if err != nil || !found || bhutan.Name != "Bhutan" {
		t.Fatalf("expected Bhutan's preset, got %v, %v, %v", bhutan.Name, found, err)
	}
	if _, found, err := lookupRegionCountry("ZZZ"); err != nil || found {
		t.Fatalf("unknown codes must not return a fallback boundary: %v, %v", found, err)
	}
}
