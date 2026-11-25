package safe

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/grassrootseconomics/ethutils"
	"github.com/lmittmann/w3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadNonceUsesLatestChainState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     json.RawMessage   `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) ||
			!assert.Len(t, request.Params, 2) {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		assert.Equal(t, "eth_call", request.Method)
		assert.JSONEq(t, `"latest"`, string(request.Params[1]))
		var call struct {
			To   common.Address `json:"to"`
			Data hexutil.Bytes  `json:"data"`
		}
		assert.NoError(t, json.Unmarshal(request.Params[0], &call))
		assert.Equal(t, safeAddr, call.To)
		assert.Equal(t, hexutil.Bytes(nonceFunc.Selector[:]), call.Data)
		w.Header().Set("Content-Type", "application/json")
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      request.ID,
			"result":  hexutil.Encode(common.LeftPadBytes([]byte{14}, 32)),
		}))
	}))
	defer server.Close()
	client, err := w3.Dial(server.URL)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	got, err := NewReader(&ethutils.Provider{Client: client}).ReadNonce(t.Context(), safeAddr)
	require.NoError(t, err)
	require.Equal(t, "14", got)
}

func TestCompatibleMultisigWithoutVersionOrThreshold(t *testing.T) {
	encoded, err := abi.Arguments{{Type: mustType(t, "address[]")}}.Pack([]common.Address{holder, assetAddr})
	require.NoError(t, err)
	caller := func(_ context.Context, _ common.Address, data []byte) ([]byte, error) {
		if [4]byte(data) == getOwnersFunc.Selector {
			return encoded, nil
		}
		return nil, errors.New("execution reverted")
	}
	state, err := ReadSafeVia(context.Background(), safeAddr, caller)
	require.NoError(t, err)
	require.ElementsMatch(t, []common.Address{holder, assetAddr}, state.Owners)
	require.Zero(t, state.Threshold, "unknown threshold must not prevent single-signer metadata edits")
}

func TestMultisigReadPreservesTransportErrors(t *testing.T) {
	outage := errors.New("connection reset by peer")
	_, err := ReadSafeVia(context.Background(), safeAddr, func(context.Context, common.Address, []byte) ([]byte, error) { return nil, outage })
	require.ErrorIs(t, err, outage)
}

func TestOptionalThresholdOutageDoesNotEraseVerifiedSigners(t *testing.T) {
	encoded, err := abi.Arguments{{Type: mustType(t, "address[]")}}.Pack([]common.Address{holder})
	require.NoError(t, err)
	state, err := ReadSafeVia(context.Background(), safeAddr, func(_ context.Context, _ common.Address, data []byte) ([]byte, error) {
		if [4]byte(data) == getOwnersFunc.Selector {
			return encoded, nil
		}
		return nil, errors.New("connection reset by peer")
	})
	require.NoError(t, err)
	require.Equal(t, []common.Address{holder}, state.Owners)
	require.Zero(t, state.Threshold)
}

func TestMultisigRejectsInvalidSignerSets(t *testing.T) {
	for _, owners := range [][]common.Address{{}, {holder, holder}, {{}}} {
		encoded, err := abi.Arguments{{Type: mustType(t, "address[]")}}.Pack(owners)
		require.NoError(t, err)
		threshold, err := abi.Arguments{{Type: mustType(t, "uint256")}}.Pack(big.NewInt(1))
		require.NoError(t, err)
		_, err = ReadSafeVia(context.Background(), safeAddr, func(_ context.Context, _ common.Address, data []byte) ([]byte, error) {
			if [4]byte(data) == getOwnersFunc.Selector {
				return encoded, nil
			}
			return threshold, nil
		})
		require.ErrorIs(t, err, ErrNotSafe)
	}
}
