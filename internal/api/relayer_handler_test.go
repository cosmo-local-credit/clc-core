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
	return calldata(erc20TransferSelector, addressWord(recipient), uintWord(amount))
}

func approvalCalldata(spender common.Address, amount *big.Int) string {
	return calldata(erc20ApproveSelector, addressWord(spender), uintWord(amount))
}

func withdrawCalldata(tokenOut, tokenIn common.Address, amount *big.Int, recipient *common.Address) string {
	selector := poolWithdrawSelector
	words := [][]byte{addressWord(tokenOut), addressWord(tokenIn), uintWord(amount)}
	if recipient != nil {
		selector = poolWithdrawRecipientSelector
		words = append(words, addressWord(*recipient))
	}
	return calldata(selector, words...)
}

func calldata(selector []byte, words ...[]byte) string {
	data := make([]byte, 4+32*len(words))
	copy(data[:4], selector)
	for i, word := range words {
		copy(data[4+i*32:4+(i+1)*32], word)
	}
	return hexutil.Encode(data)
}

func addressWord(address common.Address) []byte {
	word := make([]byte, 32)
	copy(word[12:], address.Bytes())
	return word
}

func uintWord(value *big.Int) []byte {
	word := make([]byte, 32)
	value.FillBytes(word)
	return word
}

func TestDirectVoucherTransfer(t *testing.T) {
	token := common.HexToAddress("0x1111111111111111111111111111111111111111")
	recipient := common.HexToAddress("0x2222222222222222222222222222222222222222")
	amount := new(big.Int)
	amount.SetString("123456789012345678901234567890", 10)
	valid := apiresp.CaliburSignedBatch{BatchedCall: apiresp.CaliburBatchedCall{Calls: []apiresp.CaliburCall{{
		To: token.Hex(), Value: "0", Data: transferCalldata(recipient, amount),
	}}}}

	gotToken, ok := directVoucherTransfer(valid)
	if !ok || gotToken != token.Hex() {
		t.Fatalf("directVoucherTransfer() = (%q, %v)", gotToken, ok)
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
			if _, ok := directVoucherTransfer(batch); ok {
				t.Fatal("directVoucherTransfer() accepted an unsupported batch")
			}
		})
	}
}

func TestValuationVoucherAddressesRecognizesPoolSwapHops(t *testing.T) {
	tokenA := common.HexToAddress("0x1111111111111111111111111111111111111111")
	tokenB := common.HexToAddress("0x2222222222222222222222222222222222222222")
	tokenC := common.HexToAddress("0x3333333333333333333333333333333333333333")
	poolA := common.HexToAddress("0x4444444444444444444444444444444444444444")
	poolB := common.HexToAddress("0x5555555555555555555555555555555555555555")
	recipient := common.HexToAddress("0x6666666666666666666666666666666666666666")
	amountA := big.NewInt(100)
	amountB := big.NewInt(90)
	batch := apiresp.CaliburSignedBatch{BatchedCall: apiresp.CaliburBatchedCall{Calls: []apiresp.CaliburCall{
		{To: tokenA.Hex(), Value: "0", Data: approvalCalldata(poolA, big.NewInt(0))},
		{To: tokenA.Hex(), Value: "0", Data: approvalCalldata(poolA, amountA)},
		{To: poolA.Hex(), Value: "0", Data: withdrawCalldata(tokenB, tokenA, amountA, nil)},
		{To: tokenB.Hex(), Value: "0", Data: approvalCalldata(poolB, big.NewInt(0))},
		{To: tokenB.Hex(), Value: "0", Data: approvalCalldata(poolB, amountB)},
		{To: poolB.Hex(), Value: "0", Data: withdrawCalldata(tokenC, tokenB, amountB, &recipient)},
	}}}

	if got := valuationVoucherAddresses(batch); len(got) != 3 ||
		got[0] != tokenA.Hex() || got[1] != tokenB.Hex() || got[2] != tokenC.Hex() {
		t.Fatalf("valuationVoucherAddresses() = %#v", got)
	}
}

func TestValuationVoucherAddressesRejectsMalformedSwapBatches(t *testing.T) {
	tokenIn := common.HexToAddress("0x1111111111111111111111111111111111111111")
	tokenOut := common.HexToAddress("0x2222222222222222222222222222222222222222")
	pool := common.HexToAddress("0x3333333333333333333333333333333333333333")
	amount := big.NewInt(100)
	validCalls := []apiresp.CaliburCall{
		{To: tokenIn.Hex(), Value: "0", Data: approvalCalldata(pool, big.NewInt(0))},
		{To: tokenIn.Hex(), Value: "0", Data: approvalCalldata(pool, amount)},
		{To: pool.Hex(), Value: "0", Data: withdrawCalldata(tokenOut, tokenIn, amount, nil)},
	}

	tests := map[string][]apiresp.CaliburCall{
		"incomplete hop": validCalls[:2],
		"nonzero reset": {
			{To: tokenIn.Hex(), Value: "0", Data: approvalCalldata(pool, big.NewInt(1))},
			validCalls[1], validCalls[2],
		},
		"mismatched amount": {
			validCalls[0], validCalls[1],
			{To: pool.Hex(), Value: "0", Data: withdrawCalldata(tokenOut, tokenIn, big.NewInt(99), nil)},
		},
		"wrong selector": {validCalls[0], validCalls[1], {To: pool.Hex(), Value: "0", Data: "0x12345678"}},
		"native value":   {validCalls[0], validCalls[1], {To: pool.Hex(), Value: "1", Data: validCalls[2].Data}},
	}
	for name, calls := range tests {
		t.Run(name, func(t *testing.T) {
			batch := apiresp.CaliburSignedBatch{BatchedCall: apiresp.CaliburBatchedCall{Calls: calls}}
			if got := valuationVoucherAddresses(batch); len(got) != 0 {
				t.Fatalf("valuationVoucherAddresses() = %#v", got)
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
