package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestResourceLimitsConcurrentLastSlotDB(t *testing.T) {
	pg, pool := newCatalogTestPool(t)
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var id uint64
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO users(blockchain_address,email,name)
        VALUES('0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),gen_random_uuid()::text||'@quota.test','Quota test') RETURNING id`).Scan(&id))
	defer func() {
		_, err := pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, id)
		require.NoError(t, err)
	}()
	for _, resource := range []string{"vouchers", "pools", "catalogs"} {
		t.Run(resource, func(t *testing.T) {
			_, err := pool.Exec(ctx, `UPDATE users SET max_pools=1 WHERE id=$1`, id)
			require.NoError(t, err)
			start := make(chan struct{})
			results := make(chan error, 2)
			var wg sync.WaitGroup
			for range 2 {
				wg.Go(func() {
					<-start
					tx, err := pool.Begin(ctx)
					if err != nil {
						results <- err
						return
					}
					defer tx.Rollback(ctx)
					if err = pg.CheckUserResourceLimit(ctx, tx, id, resource); err != nil {
						results <- err
						return
					}
					if resource == "catalogs" {
						zero := 0.0
						_, err = pg.CreateCuratedCatalog(ctx, tx, id, CuratedCatalogWrite{Name: "Quota", LocationName: "Test", GeoLat: &zero, GeoLng: &zero})
					} else {
						kind := "TOKEN"
						if resource == "pools" {
							kind = "POOL"
						}
						_, err = tx.Exec(ctx, `INSERT INTO user_deployments(user_id,tracking_id,resource_type) VALUES($1,gen_random_uuid(),$2)`, id, kind)
					}
					if err == nil {
						err = tx.Commit(ctx)
					}
					results <- err
				})
			}
			close(start)
			wg.Wait()
			close(results)
			successes, rejected := 0, 0
			for err := range results {
				var limit *ResourceLimitError
				switch {
				case err == nil:
					successes++
				case errors.As(err, &limit):
					rejected++
					require.Equal(t, 1, limit.Used)
					require.Equal(t, 1, limit.Limit)
				default:
					t.Fatal(err)
				}
			}
			require.Equal(t, 1, successes)
			require.Equal(t, 1, rejected)
		})
	}
}

func TestResourceLimitsDefaultsRollbackAndAdminUpdateDB(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()
	user, err := pg.InsertUser(ctx, tx, User{EthereumAddress: "0x1234567890123456789012345678901234567890", Email: "quota-defaults@test.example", Role: "USER", Status: "APPROVED"})
	require.NoError(t, err)
	require.Equal(t, 1, user.MaxTokens)
	require.Equal(t, 1, user.MaxCatalogs)
	require.Equal(t, 3, user.MaxOffers)
	require.Equal(t, 10, user.MaxPools)
	zero, two := 0, 2
	updated, err := pg.UpdateUserLimits(ctx, tx, user.EthereumAddress, UserLimitsUpdate{MaxCatalogs: &two, MaxOffers: &zero})
	require.NoError(t, err)
	require.Equal(t, 1, updated.MaxTokens)
	require.Equal(t, 10, updated.MaxPools)
	require.Equal(t, 2, updated.MaxCatalogs)
	require.Zero(t, updated.MaxOffers)
	var limit *ResourceLimitError
	require.ErrorAs(t, pg.CheckUserResourceLimit(ctx, tx, user.ID, "offers"), &limit)
	nested, err := tx.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, pg.CheckUserResourceLimit(ctx, nested, user.ID, "vouchers"))
	_, err = nested.Exec(ctx, `INSERT INTO user_deployments(user_id,tracking_id,resource_type) VALUES($1,gen_random_uuid(),'TOKEN')`, user.ID)
	require.NoError(t, err)
	require.NoError(t, nested.Rollback(ctx))
	require.NoError(t, pg.CheckUserResourceLimit(ctx, tx, user.ID, "vouchers"))
	// An invalid allowance is rejected by PostgreSQL, not just HTTP validation.
	nested, err = tx.Begin(ctx)
	require.NoError(t, err)
	_, err = nested.Exec(ctx, `UPDATE users SET max_offers=-1 WHERE id=$1`, user.ID)
	require.Error(t, err)
	require.NoError(t, nested.Rollback(ctx))
	_, err = tx.Exec(ctx, `UPDATE users SET role='SUPERADMIN' WHERE id=$1`, user.ID)
	require.NoError(t, err)
	require.NoError(t, pg.CheckUserResourceLimit(ctx, tx, user.ID, "offers"))
	require.ErrorIs(t, pg.CheckUserResourceLimit(ctx, tx, 2147483647, "offers"), pgx.ErrNoRows)
}

func TestResourceLimitsRetiredVoucherReleasesSlotDB(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()
	user, err := pg.InsertUser(ctx, tx, User{EthereumAddress: "0x1234567890123456789012345678901234567890", Email: "quota-retired@test.example", Role: "USER", Status: "APPROVED"})
	require.NoError(t, err)
	var tracking string
	require.NoError(t, tx.QueryRow(ctx, `INSERT INTO user_deployments(user_id,tracking_id,resource_type) VALUES($1,gen_random_uuid(),'TOKEN') RETURNING tracking_id::text`, user.ID).Scan(&tracking))
	var limit *ResourceLimitError
	// A queued request without a deployed voucher must still occupy its slot.
	require.ErrorAs(t, pg.CheckUserResourceLimit(ctx, tx, user.ID, "vouchers"), &limit)
	var keyID, otxID, voucherID int
	require.NoError(t, tx.QueryRow(ctx, `INSERT INTO keystore(public_key,private_key) VALUES('0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),'quota-test') RETURNING id`).Scan(&keyID))
	require.NoError(t, tx.QueryRow(ctx, `INSERT INTO otx(tracking_id,otx_type,signer_account,raw_tx,tx_hash,nonce) VALUES($1,'TOKEN_PROXY_DEPLOY',$2,'0x00','0x00',0) RETURNING id`, tracking, keyID).Scan(&otxID))
	require.NoError(t, tx.QueryRow(ctx, `INSERT INTO vouchers(chain_id,address,name,symbol,decimals,active) VALUES(42220,'0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),'Retirement quota','RETQ',6,true) RETURNING id`).Scan(&voucherID))
	_, err = tx.Exec(ctx, `INSERT INTO deployed_tokens(otx_id,platform_contract_id,status,symbol,name,decimals,admin,voucher_id,address) SELECT $1,id,'CONFIRMED','RETQ','Retirement quota',6,$2,$3,(SELECT address FROM vouchers WHERE id=$3) FROM platform_contracts WHERE chain_id=42220 AND contract_type='ERC20'`, otxID, user.EthereumAddress, voucherID)
	require.NoError(t, err)
	require.ErrorAs(t, pg.CheckUserResourceLimit(ctx, tx, user.ID, "vouchers"), &limit)
	_, err = tx.Exec(ctx, `UPDATE vouchers SET active=false WHERE id=$1`, voucherID)
	require.NoError(t, err)
	require.NoError(t, pg.CheckUserResourceLimit(ctx, tx, user.ID, "vouchers"))
	usage, err := pg.GetUserResourceUsage(ctx, tx, user.ID)
	require.NoError(t, err)
	require.Zero(t, usage.Vouchers.Used)
	// A pending replacement reserves the freed slot, so restore must fail.
	_, err = tx.Exec(ctx, `INSERT INTO user_deployments(user_id,tracking_id,resource_type) VALUES($1,gen_random_uuid(),'TOKEN')`, user.ID)
	require.NoError(t, err)
	var address string
	require.NoError(t, tx.QueryRow(ctx, `SELECT address FROM vouchers WHERE id=$1`, voucherID).Scan(&address))
	attempt, err := tx.Begin(ctx)
	require.NoError(t, err)
	require.ErrorAs(t, pg.RestoreVoucherWithinLimits(ctx, attempt, address), &limit)
	require.Equal(t, "vouchers", limit.Resource)
	require.Equal(t, 2, limit.Used)
	require.NoError(t, attempt.Rollback(ctx))
	_, err = tx.Exec(ctx, `UPDATE users SET max_tokens=2 WHERE id=$1`, user.ID)
	require.NoError(t, err)
	require.NoError(t, pg.RestoreVoucherWithinLimits(ctx, tx, address))
	require.ErrorAs(t, pg.CheckUserResourceLimit(ctx, tx, user.ID, "vouchers"), &limit)
}

func TestResourceLimitsFailedDeploymentReleasesSlotDB(t *testing.T) {
	for _, resource := range []string{"vouchers", "pools"} {
		t.Run(resource, func(t *testing.T) {
			pg, tx, cleanup := newCatalogSearchTestPg(t)
			defer cleanup()
			ctx := context.Background()
			user, err := pg.InsertUser(ctx, tx, User{EthereumAddress: "0x1234567890123456789012345678901234567890", Email: "quota-failed@test.example", Role: "USER", Status: "APPROVED"})
			require.NoError(t, err)
			_, err = tx.Exec(ctx, `UPDATE users SET max_pools=1 WHERE id=$1`, user.ID)
			require.NoError(t, err)
			kind, otxType := "TOKEN", "TOKEN_PROXY_DEPLOY"
			if resource == "pools" {
				kind, otxType = "POOL", "POOL_PROXY_DEPLOY"
			}
			var tracking string
			require.NoError(t, tx.QueryRow(ctx, `INSERT INTO user_deployments(user_id,tracking_id,resource_type) VALUES($1,gen_random_uuid(),$2) RETURNING tracking_id::text`, user.ID, kind).Scan(&tracking))
			var limit *ResourceLimitError
			require.ErrorAs(t, pg.CheckUserResourceLimit(ctx, tx, user.ID, resource), &limit)
			var keyID int
			require.NoError(t, tx.QueryRow(ctx, `INSERT INTO keystore(public_key,private_key) VALUES('0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),'quota-test') RETURNING id`).Scan(&keyID))
			insertAttempt := func() uint64 {
				var otxID uint64
				require.NoError(t, tx.QueryRow(ctx, `INSERT INTO otx(tracking_id,otx_type,signer_account,raw_tx,tx_hash,nonce) VALUES($1,$2,$3,'0x00',gen_random_uuid()::text,0) RETURNING id`, tracking, otxType, keyID).Scan(&otxID))
				if resource == "vouchers" {
					_, err = tx.Exec(ctx, `INSERT INTO deployed_tokens(otx_id,platform_contract_id,status,symbol,name,decimals,admin) SELECT $1,id,'PENDING','FAILQ','Failure quota',6,$2 FROM platform_contracts WHERE chain_id=42220 AND contract_type='ERC20'`, otxID, user.EthereumAddress)
				} else {
					_, err = tx.Exec(ctx, `INSERT INTO deployed_pools(otx_id,platform_contract_id,status,name,symbol,decimals,token_registry_address,limiter_address,fee_policy_address,quoter_address,protocol_fee_controller_address,fees_decoupled,admin) SELECT $1,id,'PENDING','Failure quota','FAILQ',6,$2,$2,$2,$2,$2,false,$2 FROM platform_contracts WHERE chain_id=42220 AND contract_type='SWAPPOOL'`, otxID, user.EthereumAddress)
				}
				require.NoError(t, err)
				return otxID
			}
			fail := func(otxID uint64) {
				if resource == "vouchers" {
					require.NoError(t, pg.FailDeployedToken(ctx, tx, otxID))
				} else {
					require.NoError(t, pg.FailDeployedPool(ctx, tx, otxID))
				}
			}
			first := insertAttempt()
			require.ErrorAs(t, pg.CheckUserResourceLimit(ctx, tx, user.ID, resource), &limit)
			fail(first)
			require.NoError(t, pg.CheckUserResourceLimit(ctx, tx, user.ID, resource))
			// A failed attempt cannot release a request with another attempt still pending.
			second := insertAttempt()
			require.ErrorAs(t, pg.CheckUserResourceLimit(ctx, tx, user.ID, resource), &limit)
			require.Equal(t, 1, limit.Used)
			fail(second)
			require.NoError(t, pg.CheckUserResourceLimit(ctx, tx, user.ID, resource))
		})
	}
}

func TestResourceLimitsOffersOnRetiredVouchersDB(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()
	user, err := pg.InsertUser(ctx, tx, User{EthereumAddress: "0x1234567890123456789012345678901234567890", Email: "retired-offers@test.example", Role: "USER", Status: "APPROVED"})
	require.NoError(t, err)
	oldID := seedMarketVoucher(t, tx, 0, 0, true, "Produce", "Produce", "Training")
	_, err = tx.Exec(ctx, `UPDATE offerings SET seller_address=$1 WHERE voucher_id=$2`, user.EthereumAddress, oldID)
	require.NoError(t, err)
	usage, err := pg.GetUserResourceUsage(ctx, tx, user.ID)
	require.NoError(t, err)
	require.Equal(t, 3, usage.Offers.Used)
	require.False(t, usage.Offers.CanCreate)

	require.NoError(t, pg.SetVoucherActive(ctx, tx, oldID, false))
	freshID := seedMarketVoucher(t, tx, 0, 0, true)
	freshOffers, err := pg.ListVoucherOfferings(ctx, tx, marketVoucherAddress(t, tx, freshID), 20)
	require.NoError(t, err)
	require.Empty(t, freshOffers)
	usage, err = pg.GetUserResourceUsage(ctx, tx, user.ID)
	require.NoError(t, err)
	require.Zero(t, usage.Offers.Used)
	require.NoError(t, pg.CheckUserResourceLimit(ctx, tx, user.ID, "offers"))
	oldOffers, err := pg.ListVoucherOfferings(ctx, tx, marketVoucherAddress(t, tx, oldID), 20)
	require.NoError(t, err)
	require.Empty(t, oldOffers)

	// Another active voucher still consumes this user's shared allowance.
	_, err = tx.Exec(ctx, `INSERT INTO offerings(voucher_id,seller_address,commodity_name,description,quantity,available) VALUES($1,$2,'Fresh','',1,true)`, freshID, user.EthereumAddress)
	require.NoError(t, err)
	usage, err = pg.GetUserResourceUsage(ctx, tx, user.ID)
	require.NoError(t, err)
	require.Equal(t, 1, usage.Offers.Used)
	// Restore must not bypass the seller's shared offer allowance.
	address := marketVoucherAddress(t, tx, oldID)
	attempt, err := tx.Begin(ctx)
	require.NoError(t, err)
	var limit *ResourceLimitError
	require.ErrorAs(t, pg.RestoreVoucherWithinLimits(ctx, attempt, address), &limit)
	require.Equal(t, "offers", limit.Resource)
	require.Equal(t, 4, limit.Used)
	require.NoError(t, attempt.Rollback(ctx))
	retired, err := pg.GetAdminVoucherByAddress(ctx, tx, address)
	require.NoError(t, err)
	require.False(t, retired.Active)
	// The admin can raise the seller's allowance, then restore at exact capacity.
	_, err = tx.Exec(ctx, `UPDATE users SET max_offers=4 WHERE id=$1`, user.ID)
	require.NoError(t, err)
	require.NoError(t, pg.RestoreVoucherWithinLimits(ctx, tx, address))
	require.NoError(t, pg.RestoreVoucherWithinLimits(ctx, tx, address))
	usage, err = pg.GetUserResourceUsage(ctx, tx, user.ID)
	require.NoError(t, err)
	require.Equal(t, 4, usage.Offers.Used)
	require.False(t, usage.Offers.CanCreate)
}

func TestResourceLimitsConcurrentVoucherRestoresDB(t *testing.T) {
	pg, pool := newCatalogTestPool(t)
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var userID uint64
	var seller string
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO users(blockchain_address,email,name,max_offers)
        VALUES('0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),gen_random_uuid()::text||'@restore.test','Restore test',1)
        RETURNING id,blockchain_address`).Scan(&userID, &seller))
	var voucherIDs []uint64
	defer func() {
		_, err := pool.Exec(context.Background(), `DELETE FROM offerings WHERE voucher_id=ANY($1::BIGINT[])`, voucherIDs)
		require.NoError(t, err)
		_, err = pool.Exec(context.Background(), `DELETE FROM vouchers WHERE id=ANY($1::BIGINT[])`, voucherIDs)
		require.NoError(t, err)
		_, err = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
		require.NoError(t, err)
	}()
	var addresses []string
	for range 2 {
		var id uint64
		var address string
		require.NoError(t, pool.QueryRow(ctx, `INSERT INTO vouchers(chain_id,address,active)
            VALUES(42220,'0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),false) RETURNING id,address`).Scan(&id, &address))
		voucherIDs = append(voucherIDs, id)
		addresses = append(addresses, address)
		_, err := pool.Exec(ctx, `INSERT INTO offerings(voucher_id,seller_address,commodity_name,description,quantity,available)
            VALUES($1,$2,'Restore','',1,true)`, id, seller)
		require.NoError(t, err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, address := range addresses {
		go func() {
			<-start
			tx, err := pool.Begin(ctx)
			if err != nil {
				results <- err
				return
			}
			defer tx.Rollback(ctx)
			err = pg.RestoreVoucherWithinLimits(ctx, tx, address)
			if err == nil {
				err = tx.Commit(ctx)
			}
			results <- err
		}()
	}
	close(start)
	succeeded, rejected := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			succeeded++
		} else {
			var limit *ResourceLimitError
			require.ErrorAs(t, err, &limit)
			rejected++
		}
	}
	require.Equal(t, 1, succeeded)
	require.Equal(t, 1, rejected)
}
