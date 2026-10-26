package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRetirementPoolQuotaRestorationDB(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()
	user, err := pg.InsertUser(ctx, tx, User{EthereumAddress: "0x1234567890123456789012345678901234567890", Email: "pool-retirement@test.example", Role: "USER", Status: "APPROVED"})
	require.NoError(t, err)
	_, err = tx.Exec(ctx, "UPDATE users SET max_pools=1 WHERE id=$1", user.ID)
	require.NoError(t, err)
	var tracking string
	require.NoError(t, tx.QueryRow(ctx, `INSERT INTO user_deployments(user_id,tracking_id,resource_type) VALUES($1,gen_random_uuid(),'POOL') RETURNING tracking_id::text`, user.ID).Scan(&tracking))
	var keyID, otxID, poolID uint64
	var address string
	require.NoError(t, tx.QueryRow(ctx, `INSERT INTO keystore(public_key,private_key) VALUES('0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),'retirement-test') RETURNING id`).Scan(&keyID))
	require.NoError(t, tx.QueryRow(ctx, `INSERT INTO otx(tracking_id,otx_type,signer_account,raw_tx,tx_hash,nonce) VALUES($1,'POOL_PROXY_DEPLOY',$2,'0x00','0x00',0) RETURNING id`, tracking, keyID).Scan(&otxID))
	require.NoError(t, tx.QueryRow(ctx, `INSERT INTO pools(chain_id,address,name,symbol,decimals,active,owner_address) VALUES(100,'0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),'Pool retirement quota','POOLRETQ',6,true,'0x5555555555555555555555555555555555555555') RETURNING id,address`).Scan(&poolID, &address))
	tag, err := tx.Exec(ctx, `INSERT INTO deployed_pools(otx_id,platform_contract_id,status,name,symbol,decimals,token_registry_address,limiter_address,fee_policy_address,quoter_address,protocol_fee_controller_address,fees_decoupled,admin,pool_id,address)
        SELECT $1,id,'CONFIRMED','Pool retirement quota','POOLRETQ',6,$2,$2,$2,$2,$2,false,$2,$3,$4 FROM platform_contracts WHERE chain_id=100 AND contract_type='SWAPPOOL'`, otxID, user.EthereumAddress, poolID, address)
	require.NoError(t, err)
	require.EqualValues(t, 1, tag.RowsAffected())
	usage, err := pg.GetUserResourceUsage(ctx, tx, user.ID)
	require.NoError(t, err)
	require.Equal(t, 1, usage.Pools.Used)
	_, err = tx.Exec(ctx, "UPDATE pools SET active=false WHERE id=$1", poolID)
	require.NoError(t, err)
	usage, err = pg.GetUserResourceUsage(ctx, tx, user.ID)
	require.NoError(t, err)
	require.Zero(t, usage.Pools.Used)
	_, err = tx.Exec(ctx, `INSERT INTO user_deployments(user_id,tracking_id,resource_type) VALUES($1,gen_random_uuid(),'POOL')`, user.ID)
	require.NoError(t, err)
	attempt, err := tx.Begin(ctx)
	require.NoError(t, err)
	changed, err := pg.RestorePoolWithinLimitsChanged(ctx, attempt, address)
	var quota *ResourceLimitError
	require.ErrorAs(t, err, &quota)
	require.False(t, changed)
	require.Equal(t, "pools", quota.Resource)
	require.Equal(t, 2, quota.Used)
	require.NoError(t, attempt.Rollback(ctx))
	var active bool
	require.NoError(t, tx.QueryRow(ctx, "SELECT active FROM pools WHERE id=$1", poolID).Scan(&active))
	require.False(t, active)
	_, err = tx.Exec(ctx, "UPDATE users SET max_pools=2 WHERE id=$1", user.ID)
	require.NoError(t, err)
	changed, err = pg.RestorePoolWithinLimitsChanged(ctx, tx, address)
	require.NoError(t, err)
	require.True(t, changed)
	changed, err = pg.RestorePoolWithinLimitsChanged(ctx, tx, address)
	require.NoError(t, err)
	require.False(t, changed)
	usage, err = pg.GetUserResourceUsage(ctx, tx, user.ID)
	require.NoError(t, err)
	require.Equal(t, 2, usage.Pools.Used)
}

func TestRetirementRestoreReportsLockedTransitionDB(t *testing.T) {
	for _, kind := range []string{"voucher", "pool"} {
		t.Run(kind, func(t *testing.T) {
			pg, pool := newCatalogTestPool(t)
			defer pool.Close()
			ctx := context.Background()
			var id uint64
			var address string
			require.NoError(t, pool.QueryRow(ctx, "INSERT INTO "+kind+"s(chain_id,address,active) VALUES(100,'0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),true) RETURNING id,address").Scan(&id, &address))
			defer func() {
				_, e := pool.Exec(ctx, "DELETE FROM retirement_events WHERE "+kind+"_id=$1", id)
				require.NoError(t, e)
				_, e = pool.Exec(ctx, "DELETE FROM "+kind+"s WHERE id=$1", id)
				require.NoError(t, e)
			}()
			retiring, err := pool.Begin(ctx)
			require.NoError(t, err)
			defer retiring.Rollback(ctx)
			_, err = retiring.Exec(ctx, "UPDATE "+kind+"s SET active=false WHERE id=$1", id)
			require.NoError(t, err)
			restoring, err := pool.Begin(ctx)
			require.NoError(t, err)
			defer restoring.Rollback(ctx)
			var pid int
			require.NoError(t, restoring.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid))
			results := make(chan struct {
				changed bool
				err     error
			}, 1)
			go func() {
				var changed bool
				var e error
				if kind == "voucher" {
					changed, e = pg.RestoreVoucherWithinLimitsChanged(ctx, restoring, address)
				} else {
					changed, e = pg.RestorePoolWithinLimitsChanged(ctx, restoring, address)
				}
				results <- struct {
					changed bool
					err     error
				}{changed, e}
			}()
			require.Eventually(t, func() bool {
				var waiting bool
				e := pool.QueryRow(ctx, "SELECT COALESCE(wait_event_type='Lock',false) FROM pg_stat_activity WHERE pid=$1", pid).Scan(&waiting)
				return e == nil && waiting
			}, 2*time.Second, 10*time.Millisecond)
			require.NoError(t, retiring.Commit(ctx))
			result := <-results
			require.NoError(t, result.err)
			require.True(t, result.changed, "the initial read saw active, but the locked state was retired")
			resource, err := GetRetirementResource(ctx, restoring, kind, address, 100, false)
			require.NoError(t, err)
			require.NoError(t, InsertRetirementEvent(ctx, restoring, resource, nil, "restored", "0x1111111111111111111111111111111111111111", "Return to use"))
			require.NoError(t, restoring.Commit(ctx))
			var events int
			require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM retirement_events WHERE "+kind+"_id=$1", id).Scan(&events))
			require.Equal(t, 1, events)
		})
	}
}
