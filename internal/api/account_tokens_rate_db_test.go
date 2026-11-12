package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

type (
	tokenRateFederation struct {
		store.FederationStore

		holdings []store.FederationTokenBalance
	}
)

func (s *tokenRateFederation) ListUserTokenHoldings(_ context.Context, _ string, _ uint64) ([]store.FederationTokenBalance, error) {
	return append([]store.FederationTokenBalance(nil), s.holdings...), nil
}

func (s *tokenRateFederation) ListUserTokenBalances(_ context.Context, _ string, _ []string) ([]store.FederationTokenBalance, error) {
	return append([]store.FederationTokenBalance(nil), s.holdings...), nil
}

// Both balance lists carry the issuer's stated rate from the catalog, so a
// wallet can restate each holding in the voucher's unit without fetching every
// voucher. A voucher with no rate omits both keys.
func TestAccountTokensCarryVoucherRate(t *testing.T) {
	a, token := newAdminAPI(t)
	// newAdminAPI has no federation at construction, so these routes are not registered.
	a.router.GET("/api/v2/accounts/:address/tokens", a.accountTokensHandler)
	a.router.GET("/api/v2/accounts/:address/tokens/issued", a.accountIssuedTokensHandler)
	ctx := context.Background()
	owner := common.HexToAddress("0x7a7e0bd6df9af1b1e6f8b54b6fbe4ca7b2a9e0aa").Hex()
	insert := func(value, unit any) string {
		var address string
		require.NoError(t, a.store.Pool().QueryRow(ctx, `INSERT INTO vouchers(chain_id,address,owner_address,active,voucher_value,voucher_uoa)
    VALUES(100,'0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),$1,true,$2::numeric,$3) RETURNING address`,
			owner, value, unit).Scan(&address))
		t.Cleanup(func() {
			_, err := a.store.Pool().Exec(ctx, "DELETE FROM vouchers WHERE address=$1", address)
			require.NoError(t, err)
		})
		return common.HexToAddress(address).Hex()
	}
	rated := insert("10.5", "KES")
	unrated := insert(nil, nil)
	moved := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	a.federationStore = &tokenRateFederation{holdings: []store.FederationTokenBalance{
		{ID: 2, TokenAddress: rated, Balance: "15300000", LastDateBlock: moved},
		{ID: 1, TokenAddress: unrated, Balance: "4000000", LastDateBlock: moved},
	}}

	for _, route := range []string{"/tokens", "/tokens/issued"} {
		t.Run(route, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v2/accounts/"+owner+route, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			res := httptest.NewRecorder()
			a.router.ServeHTTP(res, req)
			require.Equal(t, http.StatusOK, res.Code, res.Body.String())

			var body struct {
				Result struct {
					Tokens []map[string]any `json:"tokens"`
				} `json:"result"`
			}
			require.NoError(t, json.Unmarshal(res.Body.Bytes(), &body))
			byAddress := make(map[string]map[string]any, len(body.Result.Tokens))
			for _, row := range body.Result.Tokens {
				address, ok := row["tokenAddress"].(string)
				require.True(t, ok, row)
				byAddress[address] = row
			}
			require.Contains(t, byAddress, rated)
			require.Contains(t, byAddress, unrated)

			require.Equal(t, "10.5", byAddress[rated]["voucherValue"])
			require.Equal(t, "KES", byAddress[rated]["voucherUoa"])
			require.NotContains(t, byAddress[unrated], "voucherValue")
			require.NotContains(t, byAddress[unrated], "voucherUoa")
		})
	}
}
