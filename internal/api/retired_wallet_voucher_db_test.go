package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

func TestWalletBalanceCatalogRetirement(t *testing.T) {
	a, _ := newAdminAPI(t)
	address, _, cleanup := seedEditableVoucherFixture(t, a)
	defer cleanup()
	ctx := context.Background()
	balances := []store.FederationTokenBalance{
		{TokenAddress: address, Balance: "123000000"},
		{TokenAddress: "0x0000000000000000000000000000000000000000", Balance: "9"},
	}
	require.NoError(t, a.enrichTokenBalances(ctx, balances))
	require.NotNil(t, balances[0].CatalogActive)
	require.True(t, *balances[0].CatalogActive)
	require.Nil(t, balances[1].CatalogActive)

	tx, err := a.store.Pool().Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	var id uint64
	require.NoError(t, tx.QueryRow(ctx, "SELECT id FROM vouchers WHERE address = $1", address).Scan(&id))
	require.NoError(t, a.store.SetVoucherActive(ctx, tx, id, false))
	require.NoError(t, tx.Commit(ctx))

	require.NoError(t, a.enrichTokenBalances(ctx, balances))
	require.NotNil(t, balances[0].CatalogActive)
	require.False(t, *balances[0].CatalogActive)
	require.NotEmpty(t, balances[0].TokenName, "history can still describe retired vouchers")
	require.Equal(t, "123000000", balances[0].Balance, "retiring does not burn chain balances")
	require.Nil(t, balances[1].CatalogActive, "an uncatalogued token is not retired")

	_, err = a.store.Pool().Exec(ctx, "UPDATE vouchers SET active = true WHERE id = $1", id)
	require.NoError(t, err)
	require.NoError(t, a.enrichTokenBalances(ctx, balances))
	require.True(t, *balances[0].CatalogActive)
}

type walletSummaryFederation struct {
	store.FederationStore
	pages   [][]store.FederationTokenBalance
	cursors []uint64
}

func (s *walletSummaryFederation) ListUserTokenHoldings(_ context.Context, _ string, _ uint64) ([]store.FederationTokenBalance, error) {
	return s.pages[0], nil
}

func (s *walletSummaryFederation) ListUserTokenHoldingsNext(_ context.Context, _ string, cursor, _ uint64) ([]store.FederationTokenBalance, error) {
	s.cursors = append(s.cursors, cursor)
	return s.pages[len(s.cursors)], nil
}

func TestWalletSummarySkipsRetiredPages(t *testing.T) {
	a, token := newAdminAPI(t)
	// newAdminAPI has no federation at construction, so these routes are not registered.
	a.router.GET("/api/v2/accounts/:address/tokens/latest", a.accountLatestTokensHandler)
	ctx := context.Background()
	var retired []store.FederationTokenBalance
	for i := 0; i < 5; i++ {
		var address string
		require.NoError(t, a.store.Pool().QueryRow(ctx, `INSERT INTO vouchers(chain_id,address,active)
    VALUES(100,'0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),false) RETURNING address`).Scan(&address))
		t.Cleanup(func() {
			_, err := a.store.Pool().Exec(ctx, "DELETE FROM vouchers WHERE address=$1", address)
			require.NoError(t, err)
		})
		retired = append(retired, store.FederationTokenBalance{ID: uint64(20 - i), TokenAddress: address, Balance: "1"})
	}
	t.Run("active token in the same transaction as five retired tokens", func(t *testing.T) {
		tied := append([]store.FederationTokenBalance{}, retired...)
		for i := range tied {
			tied[i].ID = 100
		}
		tied = append(tied, store.FederationTokenBalance{ID: 100, TokenAddress: "0x0000000000000000000000000000000000000000", Balance: "7"})
		fed := &walletSummaryFederation{pages: [][]store.FederationTokenBalance{tied, nil}}
		a.federationStore = fed
		req := httptest.NewRequest(http.MethodGet, "/api/v2/accounts/"+common.HexToAddress(accountAddress).Hex()+"/tokens/latest", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res := httptest.NewRecorder()
		a.router.ServeHTTP(res, req)
		require.Equal(t, http.StatusOK, res.Code, res.Body.String())
		var body struct {
			Result struct {
				Tokens []store.FederationTokenBalance `json:"tokens"`
			} `json:"result"`
		}
		require.NoError(t, json.Unmarshal(res.Body.Bytes(), &body))
		require.Len(t, body.Result.Tokens, 1)
		require.Equal(t, "7", body.Result.Tokens[0].Balance)
	})
	for _, tc := range []struct {
		name  string
		older []store.FederationTokenBalance
	}{
		{"older uncatalogued holding", []store.FederationTokenBalance{{ID: 10, TokenAddress: "0x0000000000000000000000000000000000000000", Balance: "7"}}},
		{"all retired", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fed := &walletSummaryFederation{pages: [][]store.FederationTokenBalance{retired, tc.older}}
			a.federationStore = fed
			req := httptest.NewRequest(http.MethodGet, "/api/v2/accounts/"+common.HexToAddress(accountAddress).Hex()+"/tokens/latest", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			res := httptest.NewRecorder()
			a.router.ServeHTTP(res, req)
			require.Equal(t, http.StatusOK, res.Code, res.Body.String())
			var body struct {
				Result struct {
					Tokens []store.FederationTokenBalance `json:"tokens"`
				} `json:"result"`
			}
			require.NoError(t, json.Unmarshal(res.Body.Bytes(), &body))
			require.Len(t, body.Result.Tokens, len(tc.older))
			if len(tc.older) > 0 {
				require.Equal(t, tc.older[0].TokenAddress, body.Result.Tokens[0].TokenAddress)
			}
			require.Equal(t, []uint64{16}, fed.cursors)
		})
	}
}
