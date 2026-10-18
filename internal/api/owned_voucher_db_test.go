package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/stretchr/testify/require"
)

func ownedVoucherResult(t *testing.T, a *API, path, token string, status int) []store.OwnedVoucher {
	t.Helper()
	res := curatedRequest(a, http.MethodGet, path, token, "")
	require.Equal(t, status, res.Code, res.Body.String())
	if status != http.StatusOK {
		return nil
	}
	var body struct {
		Result struct {
			Vouchers []store.OwnedVoucher `json:"vouchers"`
			First    uint64               `json:"first"`
			Last     uint64               `json:"last"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &body))
	return body.Result.Vouchers
}

func TestOwnedVouchersCurrentOwnershipAndVisibilityDB(t *testing.T) {
	a, key := newOfferingAPI(t)
	t.Cleanup(func() { a.store.Pool().Close() })
	ownerID, token := seedCuratedAPIUser(t, a, key)
	otherID, otherToken := seedCuratedAPIUser(t, a, key)
	ctx := context.Background()

	var owner, other string
	require.NoError(t, a.store.Pool().QueryRow(ctx, `SELECT blockchain_address FROM users WHERE id=$1`, ownerID).Scan(&owner))
	require.NoError(t, a.store.Pool().QueryRow(ctx, `SELECT blockchain_address FROM users WHERE id=$1`, otherID).Scan(&other))
	ownerMixedCase := "0x" + strings.ToUpper(owner[2:])
	marker := "Owned wallet " + strings.ReplaceAll(t.Name(), "/", "-")
	t.Cleanup(func() {
		_, err := a.store.Pool().Exec(ctx, `DELETE FROM vouchers WHERE name LIKE $1`, marker+"%")
		require.NoError(t, err)
	})

	insert := func(suffix, voucherOwner string, active bool, located bool) (uint64, string) {
		t.Helper()
		var id uint64
		var address string
		lat, lng := "NULL", "NULL"
		if located {
			lat, lng = "0", "0"
		}
		query := fmt.Sprintf(`INSERT INTO vouchers(chain_id,address,name,symbol,decimals,owner_address,active,geo_lat,geo_lng)
			VALUES(100,'0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),$1,$2,6,$3,$4,%s,%s)
			RETURNING id,address`, lat, lng)
		require.NoError(t, a.store.Pool().QueryRow(ctx, query, marker+suffix, "OWN"+suffix, voucherOwner, active).Scan(&id, &address))
		return id, address
	}

	activeID, activeAddress := insert(" active", ownerMixedCase, true, true)
	unlocatedID, _ := insert(" unlocated", owner, true, false)
	retiredID, _ := insert(" retired", owner, false, true)
	_, _ = insert(" transferred away", other, true, true)
	transferredInID, transferredInAddress := insert(" transferred in", other, true, true)
	_, err := a.store.Pool().Exec(ctx, `UPDATE vouchers SET owner_address=$2 WHERE address=$1`, transferredInAddress, owner)
	require.NoError(t, err)
	var incompleteID uint64
	var incompleteAddress string
	require.NoError(t, a.store.Pool().QueryRow(ctx, `
		INSERT INTO vouchers(chain_id,address,owner_address,active)
		VALUES(100,'0x'||lpad(to_hex((random()*1e18)::bigint),40,'0'),$1,true)
		RETURNING id,address`, owner).Scan(&incompleteID, &incompleteAddress))
	t.Cleanup(func() {
		_, cleanupErr := a.store.Pool().Exec(ctx, `DELETE FROM vouchers WHERE id=$1`, incompleteID)
		require.NoError(t, cleanupErr)
	})

	ownedVoucherResult(t, a, "/api/v2/users/me/vouchers", "", http.StatusUnauthorized)
	cacheResponse := curatedRequest(a, http.MethodGet, "/api/v2/users/me/vouchers?perPage=1", token, "")
	require.Equal(t, http.StatusOK, cacheResponse.Code, cacheResponse.Body.String())
	require.Equal(t, "no-store", cacheResponse.Header().Get("Cache-Control"))
	first := ownedVoucherResult(t, a, "/api/v2/users/me/vouchers?perPage=2", token, http.StatusOK)
	require.Len(t, first, 2)
	require.Greater(t, first[0].ID, first[1].ID)
	next := ownedVoucherResult(t, a, fmt.Sprintf("/api/v2/users/me/vouchers?perPage=2&cursor=%d&next=true", first[1].ID), token, http.StatusOK)
	require.Len(t, next, 2)
	last := ownedVoucherResult(t, a, fmt.Sprintf("/api/v2/users/me/vouchers?perPage=2&cursor=%d&next=true", next[1].ID), token, http.StatusOK)
	all := append(append(first, next...), last...)
	require.Len(t, all, 5)
	byID := make(map[uint64]store.OwnedVoucher, len(all))
	for _, voucher := range all {
		byID[voucher.ID] = voucher
	}
	require.ElementsMatch(t, []uint64{activeID, unlocatedID, retiredID, transferredInID, incompleteID}, []uint64{all[0].ID, all[1].ID, all[2].ID, all[3].ID, all[4].ID})
	require.Equal(t, activeAddress, byID[activeID].Address)
	require.Equal(t, marker+" active", byID[activeID].Name)
	require.Equal(t, "OWN active", byID[activeID].Symbol)
	require.Equal(t, 6, *byID[activeID].Decimals)
	require.True(t, strings.EqualFold(owner, byID[activeID].OwnerAddress))
	require.True(t, byID[activeID].Active)
	require.True(t, byID[unlocatedID].Active)
	require.False(t, byID[retiredID].Active)
	require.Equal(t, incompleteAddress, byID[incompleteID].Name)
	require.Equal(t, incompleteAddress[:6]+"…"+incompleteAddress[len(incompleteAddress)-4:], byID[incompleteID].Symbol)
	previous := ownedVoucherResult(t, a, fmt.Sprintf("/api/v2/users/me/vouchers?perPage=2&cursor=%d&next=false", next[0].ID), token, http.StatusOK)
	require.Equal(t, []uint64{first[0].ID, first[1].ID}, []uint64{previous[0].ID, previous[1].ID})

	otherRows := ownedVoucherResult(t, a, "/api/v2/users/me/vouchers", otherToken, http.StatusOK)
	require.Len(t, otherRows, 1)
	require.Contains(t, otherRows[0].Name, "transferred away")

	public := curatedRequest(a, http.MethodGet, "/api/v2/vouchers?perPage=100&owner="+owner, "", "")
	require.Equal(t, http.StatusOK, public.Code, public.Body.String())
	require.Contains(t, public.Body.String(), activeAddress)
	require.NotContains(t, public.Body.String(), marker+" unlocated")
	require.NotContains(t, public.Body.String(), marker+" retired")

	ownedVoucherResult(t, a, "/api/v2/users/me/vouchers?cursor=bad&next=true", token, http.StatusBadRequest)
}
