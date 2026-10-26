package store

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRetiredPoolDiscoveryAndRecovery(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()
	owner := randomAddress(t, tx)
	term := "retirement" + owner[2:]
	var activeID, retiredID uint64
	activeAddress, retiredAddress := randomAddress(t, tx), randomAddress(t, tx)
	for i, address := range []string{activeAddress, retiredAddress} {
		id := &activeID
		if i == 1 {
			id = &retiredID
		}
		require.NoError(t, tx.QueryRow(ctx, `INSERT INTO pools (chain_id,address,name,symbol,owner_address,active)
VALUES (100,$1,$2,$3,$4,$5) RETURNING id`, address, term+address[len(address)-6:], "VIS"+address[2:], owner, i == 0).Scan(id))
	}
	for _, filter := range []CatalogFilter{{Owner: owner}, {Term: term}, {Owner: owner, Term: term}} {
		pools, err := pg.ListPoolsFiltered(ctx, tx, filter, CatalogFirstPage, 0, 1)
		require.NoError(t, err)
		require.Len(t, pools, 1, "filter retirement before the first page limit")
		require.Equal(t, activeID, pools[0].ID)
		require.True(t, pools[0].Active)
	}
	// Direct internal reads and managed pages retain a retired pool for recovery.
	retired, err := pg.GetPoolByAddress(ctx, tx, retiredAddress)
	require.NoError(t, err)
	require.False(t, retired.Active)
	managed, err := pg.ListManagedPools(ctx, tx, CatalogFilter{Term: term}, "retired", owner, CatalogFirstPage, 0, 10)
	require.NoError(t, err)
	require.Len(t, managed, 1)
	require.Equal(t, retiredID, managed[0].ID)
	otherOwner, err := pg.ListManagedPools(ctx, tx, CatalogFilter{}, "all", randomAddress(t, tx), CatalogFirstPage, 0, 10)
	require.NoError(t, err)
	require.Empty(t, otherOwner)

	require.ErrorIs(t, pg.CheckPoolOperationsAllowed(ctx, tx, 100, []string{strings.ToLower(retiredAddress)}), ErrRetiredPool)
	require.NoError(t, pg.CheckPoolOperationsAllowed(ctx, tx, 100, []string{strings.ToLower(activeAddress)}))
	require.NoError(t, pg.CheckPoolOperationsAllowed(ctx, tx, 1, []string{strings.ToLower(retiredAddress)}), "same address on another chain is independent")
}

func TestRetiredPoolKeepsProtectedComposition(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()
	poolID, voucherID, _, _, _, pool, _ := seedComposablePool(t, tx)
	_, err := tx.Exec(ctx, `INSERT INTO pool_tokens (pool_id,voucher_id,is_allowed) VALUES ($1,$2,true)`, poolID, voucherID)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `UPDATE pools SET active=false WHERE id=$1`, poolID)
	require.NoError(t, err)
	public, err := pg.ListPoolVouchers(ctx, tx, pool, 10)
	require.NoError(t, err)
	require.Empty(t, public)
	retained, err := pg.ListAdminPoolVouchers(ctx, tx, pool, 10)
	require.NoError(t, err)
	require.Len(t, retained, 1)
	require.Equal(t, voucherID, retained[0].ID)
	_, err = tx.Exec(ctx, `UPDATE vouchers SET active=false WHERE id=$1`, voucherID)
	require.NoError(t, err)
	retained, err = pg.ListAdminPoolVouchers(ctx, tx, pool, 10)
	require.NoError(t, err)
	require.Len(t, retained, 1, "owner withdrawals need retired token metadata too")
}
