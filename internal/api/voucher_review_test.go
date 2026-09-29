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
	id := uint64(reviews[0].(map[string]any)["id"].(float64))
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
	_, err = fed.Exec(ctx, `UPDATE tx SET date_block=CURRENT_TIMESTAMP AT TIME ZONE 'UTC' WHERE id=10`)
	require.NoError(t, err)
	request(http.MethodPut, root+"/me", userToken, write, http.StatusOK)
	page = request(http.MethodGet, root, "", nil, http.StatusOK)
	require.Equal(t, float64(id), page["reviews"].([]any)[0].(map[string]any)["id"])

	// Reports notify the admin inbox once; flagging alone never hides a review.
	request(http.MethodPost, reviewPath+"/flag", strangerToken, map[string]any{"reason": "No"}, http.StatusForbidden)
	for range 2 {
		request(http.MethodPost, reviewPath+"/flag", ownerToken, map[string]any{"reason": "Private information"}, http.StatusOK)
	}
	page = request(http.MethodGet, root, "", nil, http.StatusOK)
	require.Equal(t, float64(1), page["total"])
	request(http.MethodGet, "/api/v2/admin/review-notifications", ownerToken, nil, http.StatusForbidden)
	inbox := request(http.MethodGet, "/api/v2/admin/review-notifications", adminToken, nil, http.StatusOK)
	notifications := inbox["notifications"].([]any)
	require.Len(t, notifications, 1)
	notificationID := uint64(notifications[0].(map[string]any)["id"].(float64))
	request(http.MethodPut, fmt.Sprintf("/api/v2/admin/review-notifications/%d", notificationID), adminToken, map[string]any{"action": "hide"}, http.StatusOK)
	page = request(http.MethodGet, root, "", nil, http.StatusOK)
	require.Equal(t, float64(0), page["total"])
	require.Empty(t, page["reviews"])
	request(http.MethodPut, root+"/me", userToken, write, http.StatusForbidden)
	require.Empty(t, request(http.MethodGet, "/api/v2/admin/review-notifications", adminToken, nil, http.StatusOK)["notifications"])
}
