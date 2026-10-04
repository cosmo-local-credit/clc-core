package api

import (
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/go-playground/validator/v10"
)

// The validators, the exported lists and the lookup table seeds have to name
// the same values, or a request is accepted that the foreign key then rejects
// in the worker, after the deploy is signed.
func TestMarkerStylesMatchTheMigration(t *testing.T) {
	const path = "../../migrations/043_voucher_marker_style.sql"
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	const educationPath = "../../migrations/045_education_marker_icon.sql"
	education, err := os.ReadFile(educationPath)
	if err != nil {
		t.Fatalf("read %s: %v", educationPath, err)
	}
	for table, want := range map[string][]string{
		"voucher_marker_icon":  MarkerIcons,
		"voucher_marker_color": MarkerColors,
	} {
		got := markerSeed(t, string(content), table)
		if table == "voucher_marker_icon" {
			got = append(got, markerSeed(t, string(education), table)...)
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s seeds %s with %v, want %v", path, table, got, want)
		}
	}
}

func TestMarkerValidatorsListTheExportedValues(t *testing.T) {
	for _, typ := range []reflect.Type{
		reflect.TypeFor[DeployTokenRequest](),
		reflect.TypeFor[UpdateVoucherProfileRequest](),
	} {
		for field, want := range map[string][]string{
			"MarkerIcon":  MarkerIcons,
			"MarkerColor": MarkerColors,
		} {
			f, ok := typ.FieldByName(field)
			if !ok {
				t.Fatalf("%s has no %s", typ.Name(), field)
			}
			tag := f.Tag.Get("validate")
			_, list, ok := strings.Cut(tag, "oneof=")
			if !ok {
				t.Fatalf("%s.%s validate = %q, want a oneof", typ.Name(), field, tag)
			}
			if end := strings.IndexAny(list, ",|"); end != -1 {
				list = list[:end]
			}
			if got := strings.Fields(list); !slices.Equal(got, want) {
				t.Errorf("%s.%s oneof = %v, want %v", typ.Name(), field, got, want)
			}
		}
	}
}

func TestMarkerStyleValidation(t *testing.T) {
	v := validator.New(validator.WithRequiredStructEnabled())
	deploy := func(icon, color string) DeployTokenRequest {
		return DeployTokenRequest{
			Name: "Kilifi Harvest", Symbol: "KHV", Decimals: "6",
			Owner:      "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045",
			MarkerIcon: icon, MarkerColor: color,
		}
	}

	for _, req := range []DeployTokenRequest{deploy("", ""), deploy("health", "pink"), deploy("hand-coin", "")} {
		if err := v.Struct(&req); err != nil {
			t.Errorf("deploy %q/%q rejected: %v", req.MarkerIcon, req.MarkerColor, err)
		}
	}
	for _, req := range []DeployTokenRequest{deploy("rocket", ""), deploy("", "red"), deploy("Basket", "")} {
		if err := v.Struct(&req); err == nil {
			t.Errorf("deploy %q/%q accepted", req.MarkerIcon, req.MarkerColor)
		}
	}

	ptr := func(s string) *string { return &s }
	for _, req := range []UpdateVoucherProfileRequest{
		{},
		{MarkerIcon: ptr("farm"), MarkerColor: ptr("cyan")},
		// "" is how a client returns a half to automatic.
		{MarkerIcon: ptr(""), MarkerColor: ptr("")},
	} {
		if err := v.Struct(&req); err != nil {
			t.Errorf("profile %v rejected: %v", req, err)
		}
	}
	for _, req := range []UpdateVoucherProfileRequest{
		{MarkerIcon: ptr("rocket")},
		{MarkerColor: ptr("red")},
		{MarkerColor: ptr(" blue")},
	} {
		if err := v.Struct(&req); err == nil {
			t.Errorf("profile %v accepted", req)
		}
	}
}

// markerSeed extracts the values of a table's INSERT ... VALUES block.
func markerSeed(t *testing.T, sql, table string) []string {
	t.Helper()

	start := strings.Index(sql, "INSERT INTO "+table+" (value) VALUES")
	if start == -1 {
		t.Fatalf("no %s seed found", table)
	}
	end := strings.Index(sql[start:], ";")
	if end == -1 {
		t.Fatalf("unterminated %s seed", table)
	}

	var values []string
	for line := range strings.SplitSeq(sql[start:start+end], "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "('") {
			continue
		}
		values = append(values, strings.TrimSuffix(strings.TrimSuffix(line, ","), "')")[2:])
	}
	return values
}
