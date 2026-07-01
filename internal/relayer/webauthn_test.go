package relayer

import (
	"testing"
)

func TestOriginAllowed(t *testing.T) {
	allowed := []string{
		"https://app.cosmolocal.credit",
		"https://staging.cosmolocal.credit",
		"https://*.clc-app.pages.dev",
	}

	cases := []struct {
		name string
		origin string
		want  bool
	}{
		// Exact matches
		{"exact app", "https://app.cosmolocal.credit", true},
		{"exact staging", "https://staging.cosmolocal.credit", true},

		// Wildcard subdomain matches
		{"branch build", "https://feat-wallet.clc-app.pages.dev", true},
		{"short branch", "https://a.clc-app.pages.dev", true},
		{"qa preview", "https://backend-integration.clc-app.pages.dev", true},

		// Negative: wrong scheme
		{"http not https", "http://app.cosmolocal.credit", false},

		// Negative: path on exact origin
		{"path suffix", "https://app.cosmolocal.credit.evil.com", false},

		// Security: wildcard must not match empty label or dots in label
		{"empty label", "https://.clc-app.pages.dev", false},
		{"double label", "https://a.b.clc-app.pages.dev", false},

		// Security: wildcard suffix anchored — sibling domain cannot bypass
		{"sibling bypass", "https://evilclc-app.pages.dev", false},
		{"prefix injection", "https://evil.clc-app.pages.dev.evil.com", false},

		// Negative: unrelated origin
		{"unrelated", "https://evil.com", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := originAllowed(tc.origin, allowed)
			if got != tc.want {
				t.Errorf("originAllowed(%q) = %v, want %v", tc.origin, got, tc.want)
			}
		})
	}
}
