package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func seedRelationshipWant(t *testing.T, f *publicProfileBoundary, user msgUser, state string) int64 {
	t.Helper()
	var id int64
	require.NoError(t, f.a.store.Pool().QueryRow(t.Context(), `INSERT INTO wants
        (owner_user_id,name,description,location_name,geo_lat,geo_lng,starts_at,expires_at,status,contact_text,contact_permission)
        VALUES($1,'Relationship Want','Needs help','Kilifi',-3.678912,39.834567,
            CASE WHEN $2='future' THEN now()+interval '1 day' ELSE now()-interval '2 days' END,
            CASE WHEN $2='expired' THEN now()-interval '1 day' ELSE NULL END,
            CASE WHEN $2='closed' THEN 'CLOSED' ELSE 'OPEN' END,'Private instructions',3) RETURNING id`, user.ID, state).Scan(&id))
	return id
}

func relationshipWantIDs(result wantTestResult) []uint64 {
	ids := make([]uint64, 0, len(result.Wants))
	for _, row := range result.Wants {
		ids = append(ids, row.ID)
	}
	return ids
}

func TestVoucherWantsFollowCurrentOwnerAndLifecycleDB(t *testing.T) {
	f := newPublicProfileBoundary(t)
	f.a.chainID = 100
	db, ctx := f.a.store.Pool(), t.Context()
	_, err := db.Exec(ctx, `UPDATE wants SET contact_text='Private instructions',contact_permission=3 WHERE id=$1`, f.wantID)
	require.NoError(t, err)
	viewerWant := seedRelationshipWant(t, f, f.viewer, "active")
	for _, state := range []string{"future", "expired", "closed"} {
		seedRelationshipWant(t, f, f.owner, state)
	}
	// The same contract address on another chain must not contribute its owner.
	var otherChain int64
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO vouchers(chain_id,address,owner_address,name,symbol,active)
        VALUES(42220,$1,$2,'Other chain','WRONGCHAIN',true) RETURNING id`, f.voucher, f.viewer.Address).Scan(&otherChain))
	t.Cleanup(func() {
		_, err := db.Exec(context.Background(), `DELETE FROM vouchers WHERE id=$1`, otherChain)
		require.NoError(t, err)
	})
	path := "/api/v2/vouchers/" + strings.ToLower(f.voucher) + "/wants"
	response := f.get(path, "")
	result := wantResult(t, response, http.StatusOK)
	require.Equal(t, []uint64{uint64(f.wantID)}, relationshipWantIDs(result))
	require.Empty(t, result.Wants[0].Tags, "an untagged active Want must remain visible")
	requireWantPrivacyBoundary(t, response, "wants")
	requireNoWantContacts(t, response, "wants")

	// Cookie presence never grants extra public relationship data.
	require.JSONEq(t, response.Body.String(), f.get(path, f.owner.Token).Body.String())
	_, err = db.Exec(ctx, `UPDATE vouchers SET owner_address=$2 WHERE id=$1`, f.voucherID, f.viewer.Address)
	require.NoError(t, err)
	result = wantResult(t, f.get(path, ""), http.StatusOK)
	require.Equal(t, []uint64{uint64(viewerWant)}, relationshipWantIDs(result))
	_, err = db.Exec(ctx, `INSERT INTO account_retirements(user_id,closes_at,request_source)
        VALUES($1,now()+interval '6 months','SELF')`, f.viewer.ID)
	require.NoError(t, err)
	require.Empty(t, wantResult(t, f.get(path, ""), http.StatusOK).Wants)
	_, err = db.Exec(ctx, `DELETE FROM account_retirements WHERE user_id=$1`, f.viewer.ID)
	require.NoError(t, err)
	_, err = db.Exec(ctx, `UPDATE vouchers SET active=false WHERE id=$1`, f.voucherID)
	require.NoError(t, err)
	require.Empty(t, wantResult(t, f.get(path, ""), http.StatusOK).Wants)
}

func TestVoucherAndPoolWantsShareSafeMembershipAndPaginationDB(t *testing.T) {
	f := newPublicProfileBoundary(t)
	f.a.chainID = 100
	db, ctx := f.a.store.Pool(), t.Context()
	viewerWant := seedRelationshipWant(t, f, f.viewer, "active")
	newestWant := seedRelationshipWant(t, f, f.owner, "active")
	wrongChainUser := f.user("APPROVED")
	wrongWant := seedRelationshipWant(t, f, wrongChainUser, "active")
	safe := randomAddress(t)
	var secondVoucher int64
	require.NoError(t, db.QueryRow(ctx, `INSERT INTO vouchers(chain_id,address,owner_address,name,symbol,active)
        VALUES(100,$1,$2,'Second membership','SECONDREL',true) RETURNING id`, randomAddress(t), safe).Scan(&secondVoucher))
	t.Cleanup(func() {
		_, err := db.Exec(context.Background(), `DELETE FROM pool_tokens WHERE pool_id=$1`, f.poolID)
		require.NoError(t, err)
		_, err = db.Exec(context.Background(), `DELETE FROM vouchers WHERE id=$1`, secondVoucher)
		require.NoError(t, err)
		_, err = db.Exec(context.Background(), `DELETE FROM safe_state WHERE safe_address=lower($1)`, safe)
		require.NoError(t, err)
	})
	_, err := db.Exec(ctx, `UPDATE vouchers SET owner_address=$2 WHERE id=$1`, f.voucherID, safe)
	require.NoError(t, err)
	_, err = db.Exec(ctx, `INSERT INTO pool_tokens(pool_id,voucher_id,is_allowed) VALUES($1,$2,true),($1,$3,true)`, f.poolID, f.voucherID, secondVoucher)
	require.NoError(t, err)
	replace := func(chain int64, owners []string, block uint64) {
		tx, err := db.Begin(ctx)
		require.NoError(t, err)
		defer tx.Rollback(ctx)
		_, err = f.a.store.ReplaceSafeSigners(ctx, tx, chain, safe, owners, 1, block, 1)
		require.NoError(t, err)
		require.NoError(t, tx.Commit(ctx))
	}
	replace(100, []string{strings.ToLower(f.owner.Address), f.viewer.Address}, 1)
	replace(42220, []string{wrongChainUser.Address}, 1)
	paths := []string{"/api/v2/vouchers/" + f.voucher + "/wants", "/api/v2/pools/" + f.pool + "/wants"}
	wantIDs := []uint64{uint64(newestWant), uint64(viewerWant), uint64(f.wantID)}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			response := f.get(path, "")
			result := wantResult(t, response, http.StatusOK)
			require.Equal(t, wantIDs, relationshipWantIDs(result), "two accepted vouchers must not duplicate a signer's Wants")
			require.NotContains(t, relationshipWantIDs(result), uint64(wrongWant))
			requireNoWantContacts(t, response, "wants")
			first := wantResult(t, f.get(path+"?perPage=1", ""), http.StatusOK)
			second := wantResult(t, f.get(path+"?perPage=1&next=true&cursor="+strconv.FormatUint(first.Last, 10), ""), http.StatusOK)
			third := wantResult(t, f.get(path+"?perPage=1&next=true&cursor="+strconv.FormatUint(second.Last, 10), ""), http.StatusOK)
			require.Equal(t, wantIDs, []uint64{first.Wants[0].ID, second.Wants[0].ID, third.Wants[0].ID})
			previous := wantResult(t, f.get(fmt.Sprintf("%s?perPage=1&next=false&cursor=%d", path, second.First), ""), http.StatusOK)
			require.Equal(t, first.Wants[0].ID, previous.Wants[0].ID)
		})
	}
	replace(100, []string{f.viewer.Address}, 2)
	for _, path := range paths {
		require.Equal(t, []uint64{uint64(viewerWant)}, relationshipWantIDs(wantResult(t, f.get(path, ""), http.StatusOK)))
	}
	_, err = db.Exec(ctx, `UPDATE pool_tokens SET is_allowed=false WHERE pool_id=$1`, f.poolID)
	require.NoError(t, err)
	require.Empty(t, wantResult(t, f.get(paths[1], ""), http.StatusOK).Wants)
	require.Equal(t, []uint64{uint64(viewerWant)}, relationshipWantIDs(wantResult(t, f.get(paths[0], ""), http.StatusOK)))
}
