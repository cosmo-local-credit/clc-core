package api

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/relayer"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/labstack/echo/v5"
	"github.com/riverqueue/river/rivertype"
)

func transferCalldata(recipient common.Address, amount *big.Int) string {
	data := make([]byte, 68)
	copy(data[:4], erc20TransferSelector)
	copy(data[16:36], recipient.Bytes())
	amount.FillBytes(data[36:68])
	return hexutil.Encode(data)
}

func TestDirectVoucherTransfer(t *testing.T) {
	token := common.HexToAddress("0x1111111111111111111111111111111111111111")
	recipient := common.HexToAddress("0x2222222222222222222222222222222222222222")
	amount := new(big.Int)
	amount.SetString("123456789012345678901234567890", 10)
	valid := apiresp.CaliburSignedBatch{BatchedCall: apiresp.CaliburBatchedCall{Calls: []apiresp.CaliburCall{{
		To: token.Hex(), Value: "0", Data: transferCalldata(recipient, amount),
	}}}}

	gotToken, gotAmount, ok := directVoucherTransfer(valid)
	if !ok || gotToken != token.Hex() || gotAmount != amount.String() {
		t.Fatalf("directVoucherTransfer() = (%q, %q, %v)", gotToken, gotAmount, ok)
	}

	tests := map[string]apiresp.CaliburSignedBatch{
		"multiple calls": {BatchedCall: apiresp.CaliburBatchedCall{Calls: append(valid.BatchedCall.Calls, valid.BatchedCall.Calls[0])}},
		"native value":   {BatchedCall: apiresp.CaliburBatchedCall{Calls: []apiresp.CaliburCall{{To: token.Hex(), Value: "1", Data: transferCalldata(recipient, amount)}}}},
		"wrong selector": {BatchedCall: apiresp.CaliburBatchedCall{Calls: []apiresp.CaliburCall{{To: token.Hex(), Value: "0", Data: "0x12345678"}}}},
		"zero amount":    {BatchedCall: apiresp.CaliburBatchedCall{Calls: []apiresp.CaliburCall{{To: token.Hex(), Value: "0", Data: transferCalldata(recipient, big.NewInt(0))}}}},
		"malformed data": {BatchedCall: apiresp.CaliburBatchedCall{Calls: []apiresp.CaliburCall{{To: token.Hex(), Value: "0", Data: "0xzz"}}}},
	}
	for name, batch := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, ok := directVoucherTransfer(batch); ok {
				t.Fatal("directVoucherTransfer() accepted an unsupported batch")
			}
		})
	}
}

func TestIsPublicRelayerTrackingChain(t *testing.T) {
	tests := []struct {
		name string
		otxs []*store.OTX
		want bool
	}{
		{"empty", nil, true},
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

func TestRelayerTrackingStatus(t *testing.T) {
	tests := []struct {
		name       string
		otxs       []*store.OTX
		wantStatus int
		wantCode   string
	}{
		{
			name:       "not yet written by async worker",
			otxs:       nil,
			wantStatus: http.StatusNotFound,
			wantCode:   apiresp.ErrCodeNotFound,
		},
		{
			name:       "non-public relayer flow",
			otxs:       []*store.OTX{{OTXType: store.TOKEN_PROXY_DEPLOY}},
			wantStatus: http.StatusForbidden,
			wantCode:   apiresp.ErrCodeForbidden,
		},
		{
			name:       "public relayer flow",
			otxs:       []*store.OTX{{OTXType: store.RELAY_DELEGATE}},
			wantStatus: http.StatusOK,
			wantCode:   "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, code, _ := relayerTrackingStatus(tc.otxs)
			if status != tc.wantStatus {
				t.Fatalf("status = %d, want %d", status, tc.wantStatus)
			}
			if code != tc.wantCode {
				t.Fatalf("errCode = %q, want %q", code, tc.wantCode)
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
