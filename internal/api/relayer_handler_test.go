package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/relayer"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/labstack/echo/v5"
	"github.com/riverqueue/river/rivertype"
)

func TestIsPublicRelayerTrackingChain(t *testing.T) {
	tests := []struct {
		name string
		otxs []*store.OTX
		want bool
	}{
		{"empty", nil, false},
		{"delegate only", []*store.OTX{{OTXType: store.RELAY_DELEGATE}}, true},
		{"mixed relayer chain", []*store.OTX{{OTXType: store.RELAY_DELEGATE}, {OTXType: store.RELAY_EXEC}}, true},
		{"non-relayer chain", []*store.OTX{{OTXType: store.TOKEN_PROXY_DEPLOY}}, false},
		{"mixed relayer and non-relayer", []*store.OTX{{OTXType: store.RELAY_DELEGATE}, {OTXType: store.TOKEN_PROXY_DEPLOY}}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPublicRelayerTrackingChain(tc.otxs); got != tc.want {
				t.Fatalf("isPublicRelayerTrackingChain() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRelayDelegateTrackingIDUsesExistingUniqueJobArgs(t *testing.T) {
	fallback := "new-tracking-id"
	result := &rivertype.JobInsertResult{
		Job: &rivertype.JobRow{
			EncodedArgs: []byte(`{"trackingId":"existing-tracking-id","ephemeralAddress":"0x123"}`),
		},
		UniqueSkippedAsDuplicate: true,
	}

	if got := relayDelegateTrackingID(result, fallback); got != "existing-tracking-id" {
		t.Fatalf("relayDelegateTrackingID() = %q, want existing-tracking-id", got)
	}
}

func TestRelayDelegateTrackingIDFallsBackForMalformedArgs(t *testing.T) {
	fallback := "new-tracking-id"
	result := &rivertype.JobInsertResult{
		Job: &rivertype.JobRow{EncodedArgs: []byte(`{"ephemeralAddress":"0x123"}`)},
	}

	if got := relayDelegateTrackingID(result, fallback); got != fallback {
		t.Fatalf("relayDelegateTrackingID() = %q, want fallback %q", got, fallback)
	}
}

func TestRelayerHealthHandlerIncludesRelayerAddress(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v2/relayer/health", nil)
	res := httptest.NewRecorder()
	ctx := e.NewContext(req, res)

	a := &API{
		build:   "test-build",
		chainID: 42220,
		relayer: &relayer.Relayer{},
	}

	if err := a.relayerHealthHandler(ctx); err != nil {
		t.Fatalf("relayerHealthHandler() error = %v", err)
	}
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}

	var body apiresp.OKResponse
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	want := (common.Address{}).Hex()
	if got, ok := body.Result["relayerAddress"].(string); !ok || got != want {
		t.Fatalf("relayerAddress = %#v, want %q", body.Result["relayerAddress"], want)
	}
}
