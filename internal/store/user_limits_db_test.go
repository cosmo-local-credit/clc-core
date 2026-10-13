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
