package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/relayer"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/ethereum/go-ethereum/common"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/labstack/echo/v5"
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
