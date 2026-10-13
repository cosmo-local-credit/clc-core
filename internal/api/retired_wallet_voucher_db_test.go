package api

import (
	"context"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/store"
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
