package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// An account sees an asset owned by a Safe it signs for, everywhere it sees
// its own: the wallet's owned list, the account counts, pool membership and
// the market owner filter. Removing the signer takes the asset away again.
func TestSafeSignerSeesSafeOwnedAssetsDB(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	var userID uint64
	var account string
	require.NoError(t, tx.QueryRow(ctx, `INSERT INTO users(blockchain_address,name,email)
        VALUES('0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),'Safe signer',gen_random_uuid()::text||'@safe.test')
        RETURNING id, blockchain_address`).Scan(&userID, &account))
	safeAddr, coSigner := randomAddress(t, tx), randomAddress(t, tx)
	_, voucherID, _, _, _, _, token := seedComposablePool(t, tx)
	_, err := tx.Exec(ctx, `UPDATE vouchers SET owner_address=$2, active=true, geo_lat=0, geo_lng=0 WHERE id=$1`, voucherID, safeAddr)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `UPDATE pool_tokens SET is_allowed=true WHERE voucher_id=$1`, voucherID)
	require.NoError(t, err)

	ownedIDs := func() []uint64 {
		t.Helper()
		rows, err := pg.ListOwnedVouchers(ctx, tx, userID, 50)
		require.NoError(t, err)
		ids := make([]uint64, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
		return ids
	}
	marketOwned := func() int {
		t.Helper()
		rows, err := pg.ListMarketVouchersFiltered(ctx, tx, nil, CatalogFilter{Owner: account}, CatalogFirstPage, 0, 50)
		require.NoError(t, err)
		n := 0
		for _, r := range rows {
			if r.ID == voucherID {
				n++
			}
		}
		return n
	}
	counts := func() AccountRelationshipCounts {
		t.Helper()
		c, err := pg.AccountRelationshipCounts(ctx, tx, account)
		require.NoError(t, err)
		return c
	}
	accountPools := func() int {
		t.Helper()
		pools, err := pg.ListAccountPools(ctx, tx, account, CatalogFirstPage, 0, 50)
		require.NoError(t, err)
		return len(pools)
	}

	require.NotContains(t, ownedIDs(), voucherID, "not a signer yet")
	require.Zero(t, marketOwned())
	require.Zero(t, counts().Vouchers)
	require.Zero(t, accountPools())

	_, err = pg.ReplaceSafeSigners(ctx, tx, 42220, safeAddr, []string{account, coSigner}, 2, 10, 0)
	require.NoError(t, err)
	require.Contains(t, ownedIDs(), voucherID)
	require.Equal(t, 1, marketOwned())
	require.EqualValues(t, 1, counts().Vouchers)
	require.EqualValues(t, 1, counts().PoolMemberships)
	require.Equal(t, 1, accountPools())

	// The co-signer sees it too; a stranger does not.
	rows, err := pg.ListMarketVouchersFiltered(ctx, tx, nil, CatalogFilter{Owner: coSigner}, CatalogFirstPage, 0, 50)
	require.NoError(t, err)
	require.NotEmpty(t, rows)
	stranger := randomAddress(t, tx)
	rows, err = pg.ListMarketVouchersFiltered(ctx, tx, nil, CatalogFilter{Owner: stranger}, CatalogFirstPage, 0, 50)
	require.NoError(t, err)
	for _, r := range rows {
		require.NotEqual(t, voucherID, r.ID)
	}

	// RemovedOwner: the account loses the asset everywhere.
	_, err = pg.ReplaceSafeSigners(ctx, tx, 42220, safeAddr, []string{coSigner}, 1, 11, 0)
	require.NoError(t, err)
	require.NotContains(t, ownedIDs(), voucherID)
	require.Zero(t, marketOwned())
	require.Zero(t, counts().Vouchers)
	require.Zero(t, accountPools())
	_ = token
}
