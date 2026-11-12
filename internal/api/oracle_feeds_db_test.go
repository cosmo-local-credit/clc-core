package api

import (
	"net/http"
	"testing"
)

// The Gnosis registry is what the steward feed picker lists, and it needs the
// metadata as well as the address: the pair to show, the decimals to scale a
// live answer, and the freshness bound to send with setOracle.
func TestOracleFeedsListsGnosisRegistryWithMetadata(t *testing.T) {
	a, signingKey := newOfferingAPI(t)
	token := offeringToken(t, signingKey, "0x00000000000000000000000000000000000F0077", "USER")

	res, result := offeringRequest(t, a, http.MethodGet, "/api/v2/oracle-feeds?chainId=100", token, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", res.Code, res.Body.String())
	}

	feeds, ok := result["feeds"].([]any)
	if !ok {
		t.Fatalf("result.feeds = %#v, want array", result["feeds"])
	}
	var eur map[string]any
	for _, raw := range feeds {
		feed, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("feed = %#v, want object", raw)
		}
		if feed["chainId"] != float64(100) {
			t.Errorf("chainId = %#v, want only chain 100 feeds", feed["chainId"])
		}
		if feed["name"] == "EUR" {
			eur = feed
		}
	}
	if eur == nil {
		t.Fatalf("feeds = %#v, want the seeded EUR feed", feeds)
	}

	for key, want := range map[string]any{
		"address":             "0xab70BCB260073d036d1660201e9d5405F5829b7a",
		"description":         "EUR / USD",
		"decimals":            float64(8),
		"maxStalenessSeconds": float64(93600),
	} {
		if got := eur[key]; got != want {
			t.Errorf("%s = %#v, want %#v", key, got, want)
		}
	}
	for _, key := range []string{"id", "createdAt", "updatedAt"} {
		if _, ok := eur[key]; !ok {
			t.Errorf("feed is missing %s", key)
		}
	}
}
