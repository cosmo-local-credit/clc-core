package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/stretchr/testify/require"
)

type transferSearchFederation struct {
	store.FederationStore
	search  store.TransferHistorySearch
	address string
	cursor  *uint64
	limit   uint64
	next    bool
	calls   int
}

func (f *transferSearchFederation) SearchUserTransferHistory(_ context.Context, address string, search store.TransferHistorySearch, cursor *uint64, limit uint64, next bool) ([]store.FederationTransfer, error) {
	f.search, f.address, f.cursor, f.limit, f.next = search, address, cursor, limit, next
	f.calls++
	return []store.FederationTransfer{}, nil
}

func (f *transferSearchFederation) ListUserTransferHistory(_ context.Context, address string, limit uint64) ([]store.FederationTransfer, error) {
	f.address, f.limit = address, limit
	return []store.FederationTransfer{}, nil
}

func TestAccountTransferSearchEndpointDB(t *testing.T) {
	a, token := newReportSearchAPI(t)
	_, voucher, cleanup := seedReportVoucher(t, a, "Xhqzactivity old voucher", "ACT")
	defer cleanup()
	_, err := a.store.Pool().Exec(context.Background(), "UPDATE vouchers SET active=false WHERE address=$1", voucher)
	require.NoError(t, err)
	fed := &transferSearchFederation{}
	a.federationStore = fed
	// Same middleware as the production account route, mounted after injecting federation.
	a.router.GET("/api/v2/accounts/:address/transfers", a.accountTransfersHandler, a.flexibleAuthMiddleware())
	path := "/api/v2/accounts/0x1a3E18f7329bb8b533E5Be0D8B176eE1ed73563d/transfers"
	get := func(query string, auth bool) int {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path+query, nil)
		if auth {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		a.router.ServeHTTP(response, request)
		return response.Code
	}
	require.Equal(t, http.StatusUnauthorized, get("?q=Xhqzactivity", false))
	require.Zero(t, fed.calls)
	for _, q := range []string{"x", strings.Repeat("a", 67)} {
		require.Equal(t, http.StatusBadRequest, get("?q="+q, true))
	}
	require.Zero(t, fed.calls, "invalid queries must not read federation")
	require.Equal(t, http.StatusOK, get("?q=Xhqzactivity&perPage=2", true))
	require.Nil(t, fed.cursor)
	require.EqualValues(t, 2, fed.limit)
	require.Equal(t, "Xhqzactivity", fed.search.Filter.Term)
	require.Contains(t, fed.search.TokenAddresses, strings.ToLower(voucher))
	require.Equal(t, http.StatusOK, get("?q=Xhqzactivity&perPage=2&cursor=8&next=true", true))
	require.EqualValues(t, 8, *fed.cursor)
	require.True(t, fed.next)
	require.Equal(t, http.StatusOK, get(fmt.Sprintf("?q=Xhqzactivity&perPage=2&cursor=%d&next=false", 6), true))
	require.False(t, fed.next)
	require.Equal(t, "0x1a3E18f7329bb8b533E5Be0D8B176eE1ed73563d", fed.address)
	hash := "0x" + strings.Repeat("a", 64)
	require.Equal(t, http.StatusOK, get("?q="+hash, true))
	require.Equal(t, hash, fed.search.Filter.Term)
	calls := fed.calls
	require.Equal(t, http.StatusOK, get("?perPage=20", true))
	require.Equal(t, calls, fed.calls, "clearing search uses the original history path")
}
