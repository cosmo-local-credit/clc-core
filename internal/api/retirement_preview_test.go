package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/ethereum/go-ethereum/common"
	"github.com/grassrootseconomics/ethutils"
	"github.com/lmittmann/w3"
	"github.com/stretchr/testify/require"
)

type (
	retirementRPCRequest struct {
		ID     json.RawMessage   `json:"id"`
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
)

// Exercise the real w3 ABI and fixed-block calls, not the API test preview seam.
func TestRetirementVoucherPreviewRPC(t *testing.T) {
	for _, tc := range []struct {
		name            string
		supply, balance string
		owner           string
		chain           string
		wantErr         bool
	}{
		{"large precise amount", "1267650600228229401496703205377", "1", offeringOwnerAddress, "0x64", false},
		{"zero outside owner", "123", "123", offeringOwnerAddress, "0x64", false},
		{"owner changed", "123", "0", offeringStrangerAddr, "0x64", true},
		{"wrong chain", "123", "0", offeringOwnerAddress, "0x1", true},
		{"invalid supply", "10", "11", offeringOwnerAddress, "0x64", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var blocks []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var raw json.RawMessage
				require.NoError(t, json.NewDecoder(r.Body).Decode(&raw))
				var requests []retirementRPCRequest
				batch := len(raw) > 0 && raw[0] == '['
				if batch {
					require.NoError(t, json.Unmarshal(raw, &requests))
				} else {
					var request retirementRPCRequest
					require.NoError(t, json.Unmarshal(raw, &request))
					requests = append(requests, request)
				}
				replies := make([]map[string]any, 0, len(requests))
				for _, request := range requests {
					result := ""
					switch request.Method {
					case "eth_chainId":
						result = tc.chain
					case "eth_blockNumber":
						result = "0x2a"
					case "eth_call":
						var call map[string]string
						require.NoError(t, json.Unmarshal(request.Params[0], &call))
						var block string
						require.NoError(t, json.Unmarshal(request.Params[1], &block))
						mu.Lock()
						blocks = append(blocks, block)
						mu.Unlock()
						input := call["data"]
						if input == "" {
							input = call["input"]
						}
						require.GreaterOrEqual(t, len(input), 10)
						switch input[:10] {
						case "0x8da5cb5b":
							result = "0x" + hex.EncodeToString(common.LeftPadBytes(common.HexToAddress(tc.owner).Bytes(), 32))
						case "0x18160ddd":
							n, ok := new(big.Int).SetString(tc.supply, 10)
							require.True(t, ok)
							result = "0x" + hex.EncodeToString(common.LeftPadBytes(n.Bytes(), 32))
						case "0x70a08231":
							n, ok := new(big.Int).SetString(tc.balance, 10)
							require.True(t, ok)
							result = "0x" + hex.EncodeToString(common.LeftPadBytes(n.Bytes(), 32))
						case "0x313ce567":
							result = "0x" + strings.Repeat("0", 62) + "06"
						default:
							t.Errorf("unexpected selector %s", input[:10])
						}
					default:
						t.Errorf("unexpected method %s", request.Method)
					}
					replies = append(replies, map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
				}
				w.Header().Set("Content-Type", "application/json")
				if batch {
					require.NoError(t, json.NewEncoder(w).Encode(replies))
				} else {
					require.NoError(t, json.NewEncoder(w).Encode(replies[0]))
				}
			}))
			defer server.Close()
			client, err := w3.Dial(server.URL)
			require.NoError(t, err)
			defer func() { require.NoError(t, client.Close()) }()
			a := &API{chainProvider: &ethutils.Provider{Client: client}, chainID: 100}
			preview, err := a.retirementPreview(context.Background(), nil, store.RetirementResource{ResourceType: "voucher", Address: "0x1000000000000000000000000000000000000001", ChainID: 100, OwnerAddress: offeringOwnerAddress, Active: true, Symbol: "TEST"})
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			supply, _ := new(big.Int).SetString(tc.supply, 10)
			balance, _ := new(big.Int).SetString(tc.balance, 10)
			require.Equal(t, new(big.Int).Sub(supply, balance).String(), *preview.AmountOutsideOwnerRaw)
			require.Equal(t, 6, preview.Decimals)
			require.Equal(t, "42", preview.BlockNumber)
			require.Equal(t, "total-supply", preview.Coverage)
			require.Equal(t, supply.Cmp(balance) != 0, preview.HasPositiveBalances)
			mu.Lock()
			defer mu.Unlock()
			require.Len(t, blocks, 4)
			for _, block := range blocks {
				require.Equal(t, "0x2a", block)
			}
		})
	}
}

func retirementRPCClient(t *testing.T, reply func(retirementRPCRequest) (string, error)) *w3.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var requests []retirementRPCRequest
		batch := len(raw) > 0 && raw[0] == '['
		if batch {
			if err := json.Unmarshal(raw, &requests); err != nil {
				t.Error(err)
				return
			}
		} else {
			var request retirementRPCRequest
			if err := json.Unmarshal(raw, &request); err != nil {
				t.Error(err)
				return
			}
			requests = append(requests, request)
		}
		replies := make([]map[string]any, 0, len(requests))
		for _, request := range requests {
			result, err := reply(request)
			response := map[string]any{"jsonrpc": "2.0", "id": request.ID}
			if err != nil {
				response["error"] = map[string]any{"code": -32000, "message": err.Error()}
			} else {
				response["result"] = result
			}
			replies = append(replies, response)
		}
		w.Header().Set("Content-Type", "application/json")
		var err error
		if batch {
			err = json.NewEncoder(w).Encode(replies)
		} else {
			err = json.NewEncoder(w).Encode(replies[0])
		}
		if err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	client, err := w3.Dial(server.URL)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return client
}
