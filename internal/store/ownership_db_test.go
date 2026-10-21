package store

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

func TestCatalogOwnershipOrderingAndMetadata(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()
	poolID, voucherID, registry, _, _, pool, token := seedComposablePool(t, tx)
	oldOwner, first, second := randomAddress(t, tx), randomAddress(t, tx), randomAddress(t, tx)
	_, err := tx.Exec(ctx, `UPDATE pools SET owner_address=$2, description='preserved pool', fee_address=$2 WHERE id=$1`, poolID, oldOwner)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `UPDATE vouchers SET owner_address=$2, description='preserved voucher' WHERE id=$1`, voucherID, oldOwner)
	require.NoError(t, err)
	for _, address := range []string{pool, token} {
		// Other chains and supporting contracts cannot change this catalog.
		n, err := pg.ProjectCatalogOwnership(ctx, tx, 100, address, first, 10, 0)
		require.NoError(t, err)
		require.Zero(t, n)
		n, err = pg.ProjectCatalogOwnership(ctx, tx, 42220, registry, first, 10, 0)
		require.NoError(t, err)
		require.Zero(t, n)
		n, err = pg.ProjectCatalogOwnership(ctx, tx, 42220, address, first, 10, 2)
		require.NoError(t, err)
		require.EqualValues(t, 1, n)
		for _, pos := range [][2]uint64{{10, 2}, {10, 1}, {9, 99}} {
			n, err = pg.ProjectCatalogOwnership(ctx, tx, 42220, address, oldOwner, pos[0], pos[1])
			require.NoError(t, err)
			require.Zero(t, n)
		}
		n, err = pg.ProjectCatalogOwnership(ctx, tx, 42220, address, second, 10, 3)
		require.NoError(t, err)
		require.EqualValues(t, 1, n)
		// Reconciliation is an end-of-block fence.
		n, err = pg.ProjectCatalogOwnership(ctx, tx, 42220, address, first, 11, math.MaxInt64)
		require.NoError(t, err)
		require.EqualValues(t, 1, n)
		n, err = pg.ProjectCatalogOwnership(ctx, tx, 42220, address, second, 11, 100)
		require.NoError(t, err)
		require.Zero(t, n)
		n, err = pg.ProjectCatalogOwnership(ctx, tx, 42220, address, second, 12, 0)
		require.NoError(t, err)
		require.EqualValues(t, 1, n)
	}
	var owner, description, recipient string
	require.NoError(t, tx.QueryRow(ctx, `SELECT owner_address, description, fee_address FROM pools WHERE id=$1`, poolID).Scan(&owner, &description, &recipient))
	require.Equal(t, common.HexToAddress(second).Hex(), owner, "stored checksummed")
	require.Equal(t, "preserved pool", description)
	require.Equal(t, oldOwner, recipient)
	require.NoError(t, tx.QueryRow(ctx, `SELECT owner_address, description FROM vouchers WHERE id=$1`, voucherID).Scan(&owner, &description))
	require.Equal(t, common.HexToAddress(second).Hex(), owner)
	require.Equal(t, "preserved voucher", description)
}

func TestCatalogOwnershipPositionRollsBackWithAuthority(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()
	poolID, _, _, _, _, pool, _ := seedComposablePool(t, tx)
	owner := randomAddress(t, tx)
	savepoint, err := tx.Begin(ctx)
	require.NoError(t, err)
	_, err = pg.ProjectCatalogOwnership(ctx, savepoint, 42220, pool, owner, 10, 1)
	require.NoError(t, err)
	require.NoError(t, savepoint.Rollback(ctx))
	var count int
	var actual *string
	require.NoError(t, tx.QueryRow(ctx, `SELECT count(*) FROM catalog_ownership WHERE address=$1`, pool).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, tx.QueryRow(ctx, `SELECT owner_address FROM pools WHERE id=$1`, poolID).Scan(&actual))
	require.Nil(t, actual)
}

func TestDeploymentPromotionKeepsEstablishedOwnership(t *testing.T) {
	for _, kind := range []string{"voucher", "pool"} {
		for _, earlyEvent := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/early-event=%v", kind, earlyEvent), func(t *testing.T) {
				pg, tx, cleanup := newMarketTestPg(t)
				defer cleanup()
				ctx := context.Background()
				address, original, recipient := randomAddress(t, tx), randomAddress(t, tx), randomAddress(t, tx)
				contractType, otxType, table := "ERC20", TOKEN_PROXY_DEPLOY, "vouchers"
				if kind == "pool" {
					contractType, otxType, table = "SWAPPOOL", POOL_PROXY_DEPLOY, "pools"
				}
				platformID := platformContractID(t, tx, contractType)
				var chain int64
				require.NoError(t, tx.QueryRow(ctx, `SELECT chain_id FROM platform_contracts WHERE id=$1`, platformID).Scan(&chain))
				if earlyEvent {
					n, err := pg.ProjectCatalogOwnership(ctx, tx, chain, address, recipient, 42, 1)
					require.NoError(t, err)
					require.Zero(t, n)
				} else {
					_, err := tx.Exec(ctx, `INSERT INTO `+table+` (chain_id,address,name,symbol,decimals,owner_address) VALUES ($1,$2,'Established','EST',6,$3)`, chain, address, recipient)
					require.NoError(t, err)
				}
				otxID := seedDeployOTX(t, tx, otxType)
				if kind == "voucher" {
					id, err := pg.InsertDeployedToken(ctx, tx, DeployedToken{OTXID: otxID, PlatformContractID: platformID, Name: "Historical", Symbol: "HST", Decimals: 6, Admin: original})
					require.NoError(t, err)
					_, err = pg.ConfirmDeployedToken(ctx, tx, otxID, address)
					require.NoError(t, err)
					_, err = pg.UpsertVoucherFromDeploy(ctx, tx, id)
					require.NoError(t, err)
					var admin string
					require.NoError(t, tx.QueryRow(ctx, `SELECT admin FROM deployed_tokens WHERE id=$1`, id).Scan(&admin))
					require.Equal(t, original, admin)
				} else {
					id, err := pg.InsertDeployedPool(ctx, tx, DeployedPool{OTXID: otxID, PlatformContractID: platformID, Name: "Historical", Symbol: "HST", Decimals: 6, Admin: original, TokenRegistryAddress: randomAddress(t, tx), LimiterAddress: randomAddress(t, tx), FeePolicyAddress: randomAddress(t, tx), QuoterAddress: randomAddress(t, tx), ProtocolFeeControllerAddress: randomAddress(t, tx)})
					require.NoError(t, err)
					_, err = pg.ConfirmDeployedPool(ctx, tx, otxID, address)
					require.NoError(t, err)
					_, err = pg.UpsertPoolFromDeploy(ctx, tx, id)
					require.NoError(t, err)
					var admin string
					require.NoError(t, tx.QueryRow(ctx, `SELECT admin FROM deployed_pools WHERE id=$1`, id).Scan(&admin))
					require.Equal(t, original, admin)
				}
				var owner string
				require.NoError(t, tx.QueryRow(ctx, `SELECT owner_address FROM `+table+` WHERE chain_id=$1 AND address=$2`, chain, address).Scan(&owner))
				require.Equal(t, common.HexToAddress(recipient), common.HexToAddress(owner))
			})
		}
	}
}
