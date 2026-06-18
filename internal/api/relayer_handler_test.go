package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/relayer"
	"github.com/ethereum/go-ethereum/common"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/labstack/echo/v5"
)

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
