package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/stretchr/testify/require"
)

func TestVoucherDisplaySymbolAcrossDiscoveryDB(t *testing.T) {
	a := newMarketAPIWithStore(t)
	const marker = "WkpraqapiVoucherDisplaySymbol"
	native, display := "NATQXZ", "PUBQXZ"
	ids, cleanup := seedSearchFixtures(t, a, marker, []searchFixture{{name: marker, symbol: native}})
	t.Cleanup(cleanup)
	var address string
	require.NoError(t, a.store.Pool().QueryRow(t.Context(), `
		UPDATE vouchers SET display_symbol=$1 WHERE id=$2 RETURNING address`, display, ids[0]).Scan(&address))

	get := func(target string, result any) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.RemoteAddr = "198.51.100.60:5000"
		res := httptest.NewRecorder()
		a.router.ServeHTTP(res, req)
		require.Equal(t, http.StatusOK, res.Code, res.Body.String())
		envelope := struct {
			Result any `json:"result"`
		}{Result: result}
		require.NoError(t, json.Unmarshal(res.Body.Bytes(), &envelope))
	}

	for _, test := range []struct {
		term string
		want bool
	}{
		{strings.ToLower(display), true},
		{strings.ToLower(native), false},
		{address, true},
	} {
		t.Run(test.term, func(t *testing.T) {
			var vouchers struct {
				Vouchers []store.MarketVoucher `json:"vouchers"`
			}
			get(searchURL("/api/v2/vouchers", map[string][]string{"q": {test.term}}), &vouchers)

			var market struct {
				Results []store.MarketplaceResult `json:"results"`
			}
			get(searchURL("/api/v2/marketplace", map[string][]string{"q": {test.term}, "kinds": {"voucher", "offering"}}), &market)

			var offerings struct {
				Offerings []store.Offering `json:"offerings"`
			}
			get(searchURL("/api/v2/offerings", map[string][]string{"q": {test.term}}), &offerings)

			var mapPoints store.MarketMapPoints
			get(searchURL("/api/v2/market-map/points", map[string][]string{"q": {test.term}, "kinds": {"voucher"}}), &mapPoints)
			if !test.want {
				require.Empty(t, vouchers.Vouchers)
				require.Empty(t, market.Results)
				require.Empty(t, offerings.Offerings)
				require.Empty(t, mapPoints.Points)
				return
			}

			require.Len(t, vouchers.Vouchers, 1)
			require.Equal(t, ids[0], vouchers.Vouchers[0].ID)
			require.Equal(t, &native, vouchers.Vouchers[0].Symbol)
			require.Equal(t, &display, vouchers.Vouchers[0].DisplaySymbol)

			require.Len(t, market.Results, 2)
			for _, row := range market.Results {
				require.Equal(t, &native, row.Symbol)
				require.Equal(t, &display, row.DisplaySymbol)
				if row.Kind == "offering" {
					require.Equal(t, &display, row.VoucherDisplaySymbol)
				}
			}

			require.Len(t, offerings.Offerings, 1)
			require.Equal(t, &native, offerings.Offerings[0].VoucherSymbol)
			require.Equal(t, &display, offerings.Offerings[0].DisplaySymbol)
			require.Len(t, mapPoints.Points, 1)
			require.Equal(t, marker, mapPoints.Points[0].Name)
		})
	}

	items := []store.FederationTokenBalance{{TokenAddress: address, Balance: "3"}}
	require.NoError(t, a.enrichTokenBalances(t.Context(), items))
	require.Equal(t, native, items[0].TokenSymbol)
	require.Equal(t, &display, items[0].DisplaySymbol)
	require.Equal(t, "3", items[0].Balance)
}
