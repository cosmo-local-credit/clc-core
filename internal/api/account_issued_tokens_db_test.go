package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

type (
	issuedTokensFederation struct {
		store.FederationStore

		balances []store.FederationTokenBalance
		asked    [][]string
	}
)

func (s *issuedTokensFederation) ListUserTokenBalances(_ context.Context, _ string, tokens []string) ([]store.FederationTokenBalance, error) {
	s.asked = append(s.asked, tokens)
	return s.balances, nil
}

// The vouchers are real catalog rows with no coordinates, which the market
// catalog would never return; the indexer is faked.
func TestAccountIssuedTokensKeepZeroBalances(t *testing.T) {
	a, token := newAdminAPI(t)
	// newAdminAPI has no federation at construction, so this route is not registered.
	a.router.GET("/api/v2/accounts/:address/tokens/issued", a.accountIssuedTokensHandler)
	ctx := context.Background()
	owner := common.HexToAddress("0x5ee1b0bd6df9af1b1e6f8b54b6fbe4ca7b2a9e01").Hex()
	insert := func(ownerAddress string, active bool) string {
		var address string
		require.NoError(t, a.store.Pool().QueryRow(ctx, `INSERT INTO vouchers(chain_id,address,owner_address,active)
    VALUES(100,'0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),$1,$2) RETURNING address`,
			ownerAddress, active).Scan(&address))
		t.Cleanup(func() {
			_, err := a.store.Pool().Exec(ctx, "DELETE FROM vouchers WHERE address=$1", address)
			require.NoError(t, err)
		})
		return common.HexToAddress(address).Hex()
	}
	// Inserted oldest first; the list answers newest first.
	minted := insert(strings.ToLower(owner), true)
	unminted := insert(owner, true)
	insert(owner, false)
	insert("0x9999999999999999999999999999999999999999", true)
	lastMoved := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	fed := &issuedTokensFederation{balances: []store.FederationTokenBalance{
		{TokenAddress: minted, Balance: "5000000", LastDateBlock: lastMoved},
	}}
	a.federationStore = fed

	get := func(query string) (tokens []store.FederationTokenBalance, last uint64) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v2/accounts/"+owner+"/tokens/issued"+query, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res := httptest.NewRecorder()
		a.router.ServeHTTP(res, req)
		require.Equal(t, http.StatusOK, res.Code, res.Body.String())
		var body struct {
			Result struct {
				Tokens []store.FederationTokenBalance `json:"tokens"`
				Last   uint64                         `json:"last"`
			} `json:"result"`
		}
		require.NoError(t, json.Unmarshal(res.Body.Bytes(), &body))
		return body.Result.Tokens, body.Result.Last
	}

	first, cursor := get("?perPage=1")
	require.Len(t, first, 1)
	require.Equal(t, unminted, first[0].TokenAddress)
	require.Equal(t, "0", first[0].Balance)
	require.NotNil(t, first[0].CatalogActive)
	require.True(t, *first[0].CatalogActive)

	next, cursor := get(fmt.Sprintf("?perPage=1&cursor=%d&next=true", cursor))
	require.Len(t, next, 1)
	require.Equal(t, minted, next[0].TokenAddress, "the owner compare folds case")
	require.Equal(t, "5000000", next[0].Balance)
	require.True(t, next[0].LastDateBlock.Equal(lastMoved))

	end, _ := get(fmt.Sprintf("?perPage=1&cursor=%d&next=true", cursor))
	require.Empty(t, end, "retired and someone else's vouchers are not listed")
	require.Equal(t, [][]string{{unminted}, {minted}}, fed.asked, "the indexer is asked in checksummed form")
}
