package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/stretchr/testify/require"
)

func TestAdminDirectResourceRetirement(t *testing.T) {
	for _, kind := range []string{"voucher", "pool"} {
		t.Run(kind, func(t *testing.T) {
			a, priv := newOfferingAPI(t)
			fx, cleanup := seedOfferingFixtures(t, a)
			defer cleanup()
			a.retirementPreviewReader = retirementFixturePreview
			ctx := context.Background()
			owner := offeringToken(t, priv, offeringOwnerAddress, "USER")
			admin := offeringToken(t, priv, offeringOperatorAddr, "SUPERADMIN")
			address := fx.ownedAddress
			var id uint64
			if kind == "pool" {
				require.NoError(t, a.store.Pool().QueryRow(ctx, `INSERT INTO pools(chain_id,address,owner_address,name,symbol) VALUES(100,'0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),$1,'Direct pool','DIRPOOL') RETURNING id,address`, offeringOwnerAddress).Scan(&id, &address))
				defer func() {
					for _, q := range []string{"DELETE FROM retirement_events WHERE pool_id=$1", "DELETE FROM retirement_requests WHERE pool_id=$1", "DELETE FROM pools WHERE id=$1"} {
						_, err := a.store.Pool().Exec(ctx, q, id)
						require.NoError(t, err)
					}
				}()
			} else {
				require.NoError(t, a.store.Pool().QueryRow(ctx, `SELECT id FROM vouchers WHERE lower(address)=lower($1)`, address).Scan(&id))
				defer func() {
					for _, q := range []string{"DELETE FROM retirement_events WHERE voucher_id=$1", "DELETE FROM retirement_requests WHERE voucher_id=$1"} {
						_, err := a.store.Pool().Exec(ctx, q, id)
						require.NoError(t, err)
					}
				}()
			}
			column := kind + "_id"
			table := kind + "s"
			retire := "/api/v2/admin/" + kind + "s/" + address + "/retire"
			active := func() bool {
				var v bool
				require.NoError(t, a.store.Pool().QueryRow(ctx, "SELECT active FROM "+table+" WHERE id=$1", id).Scan(&v))
				return v
			}
			events := func() int { return countRows(t, a, "SELECT count(*) FROM retirement_events WHERE "+column+"=$1", id) }

			response, _ := offeringRequest(t, a, http.MethodPut, retire, owner, map[string]any{})
			require.Equal(t, http.StatusForbidden, response.Code)

			response, _ = offeringRequest(t, a, http.MethodPut, retire, admin, map[string]any{})
			require.Equal(t, http.StatusConflict, response.Code, "balances need an acknowledgement")
			require.True(t, active())

			cacheKey := "admin-direct-retirement-" + kind
			a.marketMapCache.set(cacheKey, store.MarketMapResult{})
			response, _ = offeringRequest(t, a, http.MethodPut, retire, admin, map[string]any{"acknowledgeBalances": true})
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.False(t, active())
			_, cached := a.marketMapCache.get(cacheKey)
			require.False(t, cached, "a direct retirement must invalidate public map results")
			require.Equal(t, 1, events())
			require.Equal(t, 1, countRows(t, a, "SELECT count(*) FROM retirement_requests WHERE "+column+"=$1 AND source='ADMIN' AND status='approved'", id))

			a.marketMapCache.set(cacheKey, store.MarketMapResult{})
			response, result := offeringRequest(t, a, http.MethodPut, retire, admin, map[string]any{"acknowledgeBalances": true})
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.Equal(t, false, result["changed"])
			_, cached = a.marketMapCache.get(cacheKey)
			require.True(t, cached, "a no-op retirement must preserve public map results")
			require.Equal(t, 1, events(), "a repeated retirement writes no event")

			restore := "/api/v2/admin/" + kind + "s/" + address + "/restore"
			a.marketMapCache.set(cacheKey, store.MarketMapResult{})
			restoreBody := map[string]any{"reason": "Back in use"}
			if kind == "voucher" {
				restoreBody["displaySymbol"] = "ZQRDIR"
			}
			response, _ = offeringRequest(t, a, http.MethodPut, restore, admin, restoreBody)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.True(t, active())
			_, cached = a.marketMapCache.get(cacheKey)
			require.False(t, cached, "a restoration must invalidate public map results")

			path := "/api/v2/" + kind + "s/" + address + "/retirement-requests"
			response, _ = offeringRequest(t, a, http.MethodPost, path, owner, map[string]any{"reason": "End of use"})
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			before := countRows(t, a, "SELECT count(*) FROM retirement_requests WHERE "+column+"=$1", id)
			response, _ = offeringRequest(t, a, http.MethodPut, retire, admin, map[string]any{"acknowledgeBalances": true})
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.False(t, active())
			require.Equal(t, before, countRows(t, a, "SELECT count(*) FROM retirement_requests WHERE "+column+"=$1", id), "the pending owner request is approved, not duplicated")
			require.Equal(t, 1, countRows(t, a, "SELECT count(*) FROM retirement_requests WHERE "+column+"=$1 AND source='OWNER' AND status='approved'", id))
		})
	}
}
