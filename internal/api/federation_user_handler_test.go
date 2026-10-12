package api

import (
	"testing"
)

func TestExactRawUnitValue(t *testing.T) {
	tests := []struct {
		name       string
		amountRaw  string
		decimals   int32
		claimValue string
		want       string
		valid      bool
	}{
		{name: "whole units", amountRaw: "10000000", decimals: 6, claimValue: "1", want: "10", valid: true},
		{name: "fractional claim", amountRaw: "123456789", decimals: 6, claimValue: "1.25", want: "154.32098625", valid: true},
		{name: "large exact value", amountRaw: "900719925474099325000000", decimals: 6, claimValue: "2.5", want: "2251799813685248312.5", valid: true},
		{name: "zero transfer", amountRaw: "0", decimals: 18, claimValue: "1.5", want: "0", valid: true},
		{name: "zero claim", amountRaw: "1", decimals: 0, claimValue: "0", valid: false},
		{name: "negative amount", amountRaw: "-1", decimals: 0, claimValue: "1", valid: false},
		{name: "exponent claim", amountRaw: "1", decimals: 0, claimValue: "1e3", valid: false},
		{name: "invalid decimals", amountRaw: "1", decimals: -1, claimValue: "1", valid: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, valid := exactRawUnitValue(test.amountRaw, test.decimals, test.claimValue)
			if valid != test.valid || got != test.want {
				t.Fatalf("exactRawUnitValue() = (%q, %v), want (%q, %v)", got, valid, test.want, test.valid)
			}
		})
	}
}
