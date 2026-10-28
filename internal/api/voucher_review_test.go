package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// The test federation uses the production SQL with indexer-shaped tables in
// its own schema; no remote chain, wallet, or shared indexer is mutated.
type (
	reviewTestFederation struct {
		store.FederationStore

		pool *pgxpool.Pool
	}
)

func TestReviewDeadline(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	require.Nil(t, reviewDeadline(nil, now))
	future := now.Add(time.Second)
	require.Nil(t, reviewDeadline(&future, now))
	latest := now.Add(-reviewWindow)
	require.Equal(t, now, *reviewDeadline(&latest, now))
	require.False(t, now.Before(*reviewDeadline(&latest, now)), "exactly seven days is locked")
	require.True(t, now.Before(*reviewDeadline(&now, now)), "new redemption reopens the window")
}

func (s reviewTestFederation) Pool() *pgxpool.Pool { return s.pool }

func (s reviewTestFederation) LatestPoolReviewSwap(ctx context.Context, poolAddress, reviewerAddress string) (*time.Time, error) {
	var latest *time.Time
	err := s.pool.QueryRow(ctx, `SELECT max(tx.date_block AT TIME ZONE 'UTC')
        FROM pool_swap ps JOIN tx ON tx.id=ps.tx_id
        WHERE lower(ps.contract_address)=lower($1) AND lower(ps.initiator_address)=lower($2)
        AND ps.in_value>0 AND ps.out_value>0 AND tx.success
        AND tx.date_block <= (CURRENT_TIMESTAMP AT TIME ZONE 'UTC')`, poolAddress, reviewerAddress).Scan(&latest)
	return latest, err
}

func TestVoucherReviewsHTTP(t *testing.T) {
	a, key := newOfferingAPI(t)
	ctx := context.Background()
	a.chainID = 42220
	db := a.store.Pool()
	schema := fmt.Sprintf("review_test_%d", time.Now().UnixNano())
	_, err := db.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize())
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := db.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		require.NoError(t, cleanupErr)
	})
	config, err := pgxpool.ParseConfig(os.Getenv("CORE_TEST_DSN"))
	require.NoError(t, err)
	config.ConnConfig.RuntimeParams["search_path"] = schema
	fed, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err)
	t.Cleanup(fed.Close)
	_, err = fed.Exec(ctx, `CREATE TABLE tx (id bigint PRIMARY KEY, date_block timestamp NOT NULL, success boolean NOT NULL);
        CREATE TABLE token_transfer (id bigint PRIMARY KEY, tx_id bigint NOT NULL, contract_address text NOT NULL,
        sender_address text NOT NULL, recipient_address text NOT NULL, transfer_value numeric NOT NULL)`)
	require.NoError(t, err)
	a.federationStore = reviewTestFederation{pool: fed}

	owner := strings.ToLower(offeringOwnerAddress)
	reviewer := reportAuthorAddress
	other := reportOtherAddress
	var voucherID uint64
	var address string
	err = db.QueryRow(ctx, `INSERT INTO vouchers (chain_id, address, name, owner_address)
        VALUES (42220, '0x'||substr(md5(random()::text)||md5(random()::text),1,40), 'Review test', $1) RETURNING id, address`, owner).Scan(&voucherID, &address)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := db.Exec(ctx, `DELETE FROM voucher_review_flags WHERE review_id IN (SELECT id FROM voucher_reviews WHERE voucher_id=$1)`, voucherID)
		require.NoError(t, cleanupErr)
		_, cleanupErr = db.Exec(ctx, `DELETE FROM voucher_reviews WHERE voucher_id=$1`, voucherID)
		require.NoError(t, cleanupErr)
		_, cleanupErr = db.Exec(ctx, `DELETE FROM vouchers WHERE id=$1`, voucherID)
		require.NoError(t, cleanupErr)
	})
	root := "/api/v2/vouchers/" + address + "/reviews"
	userToken := offeringToken(t, key, reviewer, "USER")
	ownerToken := offeringToken(t, key, offeringOwnerAddress, "USER")
	strangerToken := offeringToken(t, key, other, "USER")
	adminToken := offeringToken(t, key, reportAdminAddress, "SUPERADMIN")
	request := func(method, path, token string, body map[string]any, want int) map[string]any {
		t.Helper()
		response, result := offeringRequest(t, a, method, path, token, body)
		require.Equal(t, want, response.Code, response.Body.String())
		return result
	}
	write := map[string]any{"stars": 5, "comment": "Excellent", "reviewerAddress": owner}
	request(http.MethodPut, root+"/me", "", write, http.StatusUnauthorized)
	request(http.MethodPut, root+"/me", offeringServiceToken(t, key), write, http.StatusForbidden)
	request(http.MethodPut, root+"/me", userToken, write, http.StatusForbidden)
	request(http.MethodPut, root+"/me", ownerToken, write, http.StatusForbidden)

	// Wrong recipient, failed, zero-value and unrelated token transfers cannot unlock a review.
	for id, row := range []struct {
		token, to string
		value     int
		success   bool
	}{
		{address, other, 1, true}, {address, owner, 1, false}, {address, owner, 0, true}, {other, owner, 1, true},
	} {
		_, err = fed.Exec(ctx, `INSERT INTO tx VALUES ($1, CURRENT_TIMESTAMP AT TIME ZONE 'UTC', $2)`, id+1, row.success)
		require.NoError(t, err)
		_, err = fed.Exec(ctx, `INSERT INTO token_transfer VALUES ($1,$1,$2,$3,$4,$5)`, id+1, row.token, reviewer, row.to, row.value)
		require.NoError(t, err)
	}
	request(http.MethodPut, root+"/me", userToken, write, http.StatusForbidden)
	_, err = fed.Exec(ctx, `INSERT INTO tx VALUES (10, (CURRENT_TIMESTAMP AT TIME ZONE 'UTC') - interval '1 hour', true)`)
	require.NoError(t, err)
	_, err = fed.Exec(ctx, `INSERT INTO token_transfer VALUES (10,10,$1,$2,$3,1)`, address, reviewer, owner)
	require.NoError(t, err)
	// Imported lowercase rows and checksum-case rows emitted by eth-tracker
	// must both unlock the same review for the authenticated wallet.
	lowercaseAccess := request(http.MethodGet, root+"/me", userToken, nil, http.StatusOK)
	require.NotNil(t, lowercaseAccess["editableUntil"])
	_, err = fed.Exec(ctx, `UPDATE token_transfer SET contract_address=$1, sender_address=$2, recipient_address=$3 WHERE id=10`,
		common.HexToAddress(address).Hex(), common.HexToAddress(reviewer).Hex(), common.HexToAddress(owner).Hex())
	require.NoError(t, err)
	checksumAccess := request(http.MethodGet, root+"/me", userToken, nil, http.StatusOK)
	require.Equal(t, lowercaseAccess["editableUntil"], checksumAccess["editableUntil"], "indexed address casing must not hide a redemption")
	request(http.MethodPut, root+"/me", userToken, map[string]any{"stars": 6}, http.StatusBadRequest)
	request(http.MethodPut, root+"/me", userToken, write, http.StatusOK)
	request(http.MethodPut, root+"/me", userToken, map[string]any{"stars": 4, "comment": "Updated"}, http.StatusOK)
	page := request(http.MethodGet, root, "", nil, http.StatusOK)
	require.Equal(t, float64(1), page["total"])
	require.Equal(t, float64(4), page["average"])
	publicJSON, err := json.Marshal(page)
	require.NoError(t, err)
	for _, private := range []string{reviewer, owner, "reviewerAddress", "editableUntil", "dateBlock"} {
		require.NotContains(t, string(publicJSON), private)
	}
	reviews, ok := page["reviews"].([]any)
	require.True(t, ok, "reviews must be an array")
	review, ok := reviews[0].(map[string]any)
	require.True(t, ok, "review must be an object")
	rawID, ok := review["id"].(float64)
	require.True(t, ok, "review id must be a number")
	id := uint64(rawID)
	reviewPath := fmt.Sprintf("%s/%d", root, id)
	require.Empty(t, request(http.MethodGet, root+"?stars=5", "", nil, http.StatusOK)["reviews"])
	require.Equal(t, float64(1), request(http.MethodGet, root+"?stars=5", "", nil, http.StatusOK)["total"])
	request(http.MethodGet, root+"?stars=9", "", nil, http.StatusBadRequest)
	access := request(http.MethodGet, root+"/me", userToken, nil, http.StatusOK)
	require.NotNil(t, access["review"])
	require.NotNil(t, access["editableUntil"])
	request(http.MethodPut, reviewPath+"/reply", strangerToken, map[string]any{"reply": "Forged"}, http.StatusForbidden)
	request(http.MethodPut, reviewPath+"/reply", adminToken, map[string]any{"reply": "Admin override"}, http.StatusForbidden)
	request(http.MethodPut, reviewPath+"/reply", ownerToken, map[string]any{"reply": "Thanks"}, http.StatusOK)
	request(http.MethodPut, reviewPath+"/reply", ownerToken, map[string]any{"reply": "Thanks again"}, http.StatusOK)

	// New ownership does not grant authorship of the previous owner's reply.
	_, err = db.Exec(ctx, `UPDATE vouchers SET owner_address=$2 WHERE id=$1`, voucherID, other)
	require.NoError(t, err)
	request(http.MethodPut, reviewPath+"/reply", strangerToken, map[string]any{"reply": "Overwrite"}, http.StatusNotFound)
	request(http.MethodPut, reviewPath+"/reply", ownerToken, map[string]any{"reply": "Old owner"}, http.StatusForbidden)
	_, err = db.Exec(ctx, `UPDATE vouchers SET owner_address=$2 WHERE id=$1`, voucherID, owner)
	require.NoError(t, err)

	// A stale redemption locks updates; a new one reuses the same review ID.
	_, err = fed.Exec(ctx, `UPDATE tx SET date_block=(CURRENT_TIMESTAMP AT TIME ZONE 'UTC')-interval '8 days' WHERE id=10`)
	require.NoError(t, err)
	request(http.MethodPut, root+"/me", userToken, write, http.StatusForbidden)
	// Keep the redemption in the past for both the database and API clocks.
	// Exact database "now" can be future-dated on the host running the API.
	_, err = fed.Exec(ctx, `UPDATE tx SET date_block=(CURRENT_TIMESTAMP AT TIME ZONE 'UTC')-interval '1 minute' WHERE id=10`)
	require.NoError(t, err)
	request(http.MethodPut, root+"/me", userToken, write, http.StatusOK)
	page = request(http.MethodGet, root, "", nil, http.StatusOK)
	reviewList, ok := page["reviews"].([]any)
	require.True(t, ok, "reviews must be an array")
	require.NotEmpty(t, reviewList)
	first, ok := reviewList[0].(map[string]any)
	require.True(t, ok, "review must be an object")
	require.Equal(t, float64(id), first["id"])

	// Reports notify the admin inbox once; flagging alone never hides a review.
	request(http.MethodPost, reviewPath+"/flag", strangerToken, map[string]any{"reason": "No"}, http.StatusForbidden)
	for range 2 {
		request(http.MethodPost, reviewPath+"/flag", ownerToken, map[string]any{"reason": "Private information"}, http.StatusOK)
	}
	page = request(http.MethodGet, root, "", nil, http.StatusOK)
	require.Equal(t, float64(1), page["total"])
	request(http.MethodGet, "/api/v2/admin/review-notifications", ownerToken, nil, http.StatusForbidden)
	inbox := request(http.MethodGet, "/api/v2/admin/review-notifications", adminToken, nil, http.StatusOK)
	notifications, ok := inbox["notifications"].([]any)
	require.True(t, ok, "notifications must be an array")
	require.Len(t, notifications, 1)
	note, ok := notifications[0].(map[string]any)
	require.True(t, ok, "notification must be an object")
	rawNoteID, ok := note["id"].(float64)
	require.True(t, ok, "notification id must be a number")
	notificationID := uint64(rawNoteID)
	request(http.MethodPut, fmt.Sprintf("/api/v2/admin/review-notifications/%d", notificationID), adminToken, map[string]any{"action": "hide"}, http.StatusOK)
	page = request(http.MethodGet, root, "", nil, http.StatusOK)
	require.Equal(t, float64(0), page["total"])
	require.Empty(t, page["reviews"])
	request(http.MethodPut, root+"/me", userToken, write, http.StatusForbidden)
	require.Empty(t, request(http.MethodGet, "/api/v2/admin/review-notifications", adminToken, nil, http.StatusOK)["notifications"])
}

func TestPoolReviewsHTTP(t *testing.T) {
	a, key := newOfferingAPI(t)
	ctx := context.Background()
	a.chainID = 42220
	db := a.store.Pool()
	schema := fmt.Sprintf("pool_review_test_%d", time.Now().UnixNano())
	_, err := db.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize())
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := db.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		require.NoError(t, cleanupErr)
	})
	config, err := pgxpool.ParseConfig(os.Getenv("CORE_TEST_DSN"))
	require.NoError(t, err)
	config.ConnConfig.RuntimeParams["search_path"] = schema
	fed, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err)
	t.Cleanup(fed.Close)
	_, err = fed.Exec(ctx, `CREATE TABLE tx (id bigint PRIMARY KEY, date_block timestamp NOT NULL, success boolean NOT NULL);
        CREATE TABLE pool_swap (tx_id bigint NOT NULL, log_index bigint NOT NULL, contract_address text NOT NULL,
        initiator_address text NOT NULL, in_value numeric NOT NULL, out_value numeric NOT NULL)`)
	require.NoError(t, err)
	a.federationStore = reviewTestFederation{pool: fed}

	owner := strings.ToLower(offeringOwnerAddress)
	reviewer := reportAuthorAddress
	other := reportOtherAddress
	var poolID, otherPoolID uint64
	var address, otherAddress string
	err = db.QueryRow(ctx, `INSERT INTO pools (chain_id,address,name,owner_address)
        VALUES (42220,'0x'||substr(md5(random()::text)||md5(random()::text),1,40),'Review pool',$1) RETURNING id,address`, owner).Scan(&poolID, &address)
	require.NoError(t, err)
	err = db.QueryRow(ctx, `INSERT INTO pools (chain_id,address,name,owner_address)
        VALUES (42220,'0x'||substr(md5(random()::text)||md5(random()::text),1,40),'Second review pool',$1) RETURNING id,address`, owner).Scan(&otherPoolID, &otherAddress)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := db.Exec(ctx, `DELETE FROM voucher_review_flags WHERE pool_review_id IN (SELECT id FROM pool_reviews WHERE pool_id=ANY($1))`, []uint64{poolID, otherPoolID})
		require.NoError(t, cleanupErr)
		_, cleanupErr = db.Exec(ctx, `DELETE FROM pool_reviews WHERE pool_id=ANY($1)`, []uint64{poolID, otherPoolID})
		require.NoError(t, cleanupErr)
		_, cleanupErr = db.Exec(ctx, `DELETE FROM pools WHERE id=ANY($1)`, []uint64{poolID, otherPoolID})
		require.NoError(t, cleanupErr)
	})

	root := "/api/v2/pools/" + address + "/reviews"
	otherRoot := "/api/v2/pools/" + otherAddress + "/reviews"
	userToken := offeringToken(t, key, reviewer, "USER")
	ownerToken := offeringToken(t, key, offeringOwnerAddress, "USER")
	newOwnerToken := offeringToken(t, key, other, "USER")
	adminToken := offeringToken(t, key, reportAdminAddress, "ADMIN")
	request := func(method, path, token string, body map[string]any, want int) map[string]any {
		t.Helper()
		response, result := offeringRequest(t, a, method, path, token, body)
		require.Equal(t, want, response.Code, response.Body.String())
		return result
	}
	write := map[string]any{"stars": 5, "comment": "Reliable swaps", "reviewerAddress": owner}
	request(http.MethodGet, root+"/me", "", nil, http.StatusUnauthorized)
	request(http.MethodGet, root+"/me", offeringServiceToken(t, key), nil, http.StatusForbidden)
	request(http.MethodPut, root+"/me", userToken, write, http.StatusForbidden)
	request(http.MethodPut, root+"/me", ownerToken, write, http.StatusForbidden)

	// Failed, zero-value, future, wrong-pool and wrong-initiator events do not qualify.
	for id, row := range []struct {
		pool, initiator string
		in, out         int
		success         bool
		when            string
	}{
		{address, reviewer, 1, 1, false, "CURRENT_TIMESTAMP AT TIME ZONE 'UTC'"},
		{address, reviewer, 0, 1, true, "CURRENT_TIMESTAMP AT TIME ZONE 'UTC'"},
		{address, reviewer, 1, 0, true, "CURRENT_TIMESTAMP AT TIME ZONE 'UTC'"},
		{address, other, 1, 1, true, "CURRENT_TIMESTAMP AT TIME ZONE 'UTC'"},
		{otherAddress, reviewer, 1, 1, true, "CURRENT_TIMESTAMP AT TIME ZONE 'UTC'"},
		{address, reviewer, 1, 1, true, "(CURRENT_TIMESTAMP AT TIME ZONE 'UTC') + interval '1 day'"},
	} {
		txID := id + 1
		_, err = fed.Exec(ctx, fmt.Sprintf(`INSERT INTO tx VALUES ($1,%s,$2)`, row.when), txID, row.success)
		require.NoError(t, err)
		_, err = fed.Exec(ctx, `INSERT INTO pool_swap VALUES ($1,1,$2,$3,$4,$5)`, txID, row.pool, row.initiator, row.in, row.out)
		require.NoError(t, err)
	}
	request(http.MethodPut, root+"/me", userToken, write, http.StatusForbidden)

	// One successful route uses both pools and grants access to each one.
	_, err = fed.Exec(ctx, `INSERT INTO tx VALUES (20,(CURRENT_TIMESTAMP AT TIME ZONE 'UTC')-interval '1 hour',true)`)
	require.NoError(t, err)
	_, err = fed.Exec(ctx, `INSERT INTO pool_swap VALUES (20,1,$1,$3,10,9),(20,2,$2,$3,9,8)`, address, otherAddress, reviewer)
	require.NoError(t, err)
	require.NotNil(t, request(http.MethodGet, root+"/me", userToken, nil, http.StatusOK)["editableUntil"])
	require.NotNil(t, request(http.MethodGet, otherRoot+"/me", userToken, nil, http.StatusOK)["editableUntil"])
	request(http.MethodPut, root+"/me", userToken, write, http.StatusOK)
	request(http.MethodPut, root+"/me", userToken, map[string]any{"stars": 4, "comment": "Updated"}, http.StatusOK)

	page := request(http.MethodGet, root, "", nil, http.StatusOK)
	require.Equal(t, float64(1), page["total"])
	require.Equal(t, float64(4), page["average"])
	publicJSON, err := json.Marshal(page)
	require.NoError(t, err)
	for _, private := range []string{reviewer, owner, "reviewerAddress", "editableUntil", "dateBlock"} {
		require.NotContains(t, string(publicJSON), private)
	}
	reviews, ok := page["reviews"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, reviews)
	review, ok := reviews[0].(map[string]any)
	require.True(t, ok)
	reviewID, ok := review["id"].(float64)
	require.True(t, ok)
	id := uint64(reviewID)
	reviewPath := fmt.Sprintf("%s/%d", root, id)
	request(http.MethodPut, reviewPath+"/reply", adminToken, map[string]any{"reply": "Admin override"}, http.StatusForbidden)
	request(http.MethodPut, reviewPath+"/reply", ownerToken, map[string]any{"reply": "Thank you"}, http.StatusOK)

	// A persisted Safe signer may act for the Safe-owned Pool, while the Admin
	// role alone grants no reply or report privilege.
	const safeOwner = "0x7153330000000000000000000000000000000067"
	var secondReviewID uint64
	err = db.QueryRow(ctx, `INSERT INTO pool_reviews (pool_id,reviewer_address,stars,comment)
		VALUES ($1,$2,3,'Second review') RETURNING id`, poolID, strings.ToLower(reportAdminAddress)).Scan(&secondReviewID)
	require.NoError(t, err)
	safeTx, err := db.Begin(ctx)
	require.NoError(t, err)
	_, err = a.store.ReplaceSafeSigners(ctx, safeTx, 42220, safeOwner, []string{other}, 1, 100, 1)
	require.NoError(t, err)
	require.NoError(t, safeTx.Commit(ctx))
	t.Cleanup(func() {
		_, _ = db.Exec(ctx, `DELETE FROM safe_state WHERE safe_address=$1`, strings.ToLower(safeOwner))
	})
	_, err = db.Exec(ctx, `UPDATE pools SET owner_address=$2 WHERE id=$1`, poolID, safeOwner)
	require.NoError(t, err)
	request(http.MethodPut, fmt.Sprintf("%s/%d/reply", root, secondReviewID), newOwnerToken, map[string]any{"reply": "Safe reply"}, http.StatusOK)

	// Existing replies stay with their author after ownership changes.
	_, err = db.Exec(ctx, `UPDATE pools SET owner_address=$2 WHERE id=$1`, poolID, other)
	require.NoError(t, err)
	request(http.MethodPut, reviewPath+"/reply", newOwnerToken, map[string]any{"reply": "Replacement"}, http.StatusNotFound)
	request(http.MethodPut, reviewPath+"/reply", ownerToken, map[string]any{"reply": "Old owner"}, http.StatusForbidden)
	_, err = db.Exec(ctx, `UPDATE pools SET owner_address=$2,active=false WHERE id=$1`, poolID, owner)
	require.NoError(t, err)
	require.Equal(t, float64(2), request(http.MethodGet, root, "", nil, http.StatusOK)["total"], "retired pool reviews stay public")

	// A stale swap locks edits and a new successful swap reopens the same review.
	_, err = fed.Exec(ctx, `UPDATE tx SET date_block=(CURRENT_TIMESTAMP AT TIME ZONE 'UTC')-interval '8 days' WHERE id=20`)
	require.NoError(t, err)
	request(http.MethodPut, root+"/me", userToken, write, http.StatusForbidden)
	_, err = fed.Exec(ctx, `UPDATE tx SET date_block=(CURRENT_TIMESTAMP AT TIME ZONE 'UTC')-interval '1 minute' WHERE id=20`)
	require.NoError(t, err)
	request(http.MethodPut, root+"/me", userToken, write, http.StatusOK)
	updatedPage := request(http.MethodGet, root, "", nil, http.StatusOK)
	updatedReviews, ok := updatedPage["reviews"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, updatedReviews)
	foundOriginal := false
	for _, rawReview := range updatedReviews {
		updatedReview, reviewOK := rawReview.(map[string]any)
		require.True(t, reviewOK)
		if updatedReview["id"] == float64(id) {
			foundOriginal = true
		}
	}
	require.True(t, foundOriginal, "editing keeps the original review instead of creating another")

	for range 2 {
		request(http.MethodPost, reviewPath+"/flag", ownerToken, map[string]any{"reason": "Abuse"}, http.StatusOK)
	}
	inbox := request(http.MethodGet, "/api/v2/admin/review-notifications", adminToken, nil, http.StatusOK)
	notifications, ok := inbox["notifications"].([]any)
	require.True(t, ok)
	require.Len(t, notifications, 1)
	note, ok := notifications[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "pool", note["resourceKind"])
	resourceAddress, ok := note["resourceAddress"].(string)
	require.True(t, ok)
	poolAddress, ok := note["poolAddress"].(string)
	require.True(t, ok)
	require.Equal(t, strings.ToLower(address), strings.ToLower(resourceAddress))
	require.Equal(t, strings.ToLower(address), strings.ToLower(poolAddress))
	require.NotContains(t, note, "voucherAddress")
	noteID, ok := note["id"].(float64)
	require.True(t, ok)
	notificationID := uint64(noteID)
	request(http.MethodPut, fmt.Sprintf("/api/v2/admin/review-notifications/%d", notificationID), adminToken, map[string]any{"action": "hide"}, http.StatusOK)
	require.Equal(t, float64(1), request(http.MethodGet, root, "", nil, http.StatusOK)["total"])
	request(http.MethodPut, root+"/me", userToken, write, http.StatusForbidden)
}
