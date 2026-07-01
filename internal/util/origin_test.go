package util

import "testing"

func TestOriginAllowed(t *testing.T) {
	allowed := []string{
		"https://staging.cosmolocal.credit",
		"https://*.clc-app.pages.dev",
	}

	tests := []struct {
		name   string
		origin string
		want   bool
	}{
		{"exact staging", "https://staging.cosmolocal.credit", true},
		{"exact other", "https://app.cosmolocal.credit", false},
		{"branch build", "https://feat-wallet.clc-app.pages.dev", true},
		{"short branch", "https://a.clc-app.pages.dev", true},
		{"qa preview", "https://backend-integration.clc-app.pages.dev", true},
		{"wrong scheme", "http://backend-integration.clc-app.pages.dev", false},
		{"wrong suffix", "https://feat-wallet.pages.dev", false},
		{"empty label", "https://.clc-app.pages.dev", false},
		{"double label", "https://a.b.clc-app.pages.dev", false},
		{"sibling bypass", "https://evilclc-app.pages.dev", false},
		{"prefix injection", "https://evil.clc-app.pages.dev.evil.com", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := OriginAllowed(tc.origin, allowed)
			if got != tc.want {
				t.Errorf("OriginAllowed(%q) = %v, want %v", tc.origin, got, tc.want)
			}
		})
	}
}
