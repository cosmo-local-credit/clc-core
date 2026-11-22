package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/safe"
	"github.com/ethereum/go-ethereum/common"
	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/require"
)

type (
	resourceTargetsSafeReader struct {
		owners []common.Address
		err    error
		calls  int
	}
)

const (
	resourceTargetsPath = "/api/v2/messaging/resource-targets"
)

func (r *resourceTargetsSafeReader) ReadSafeLatest(context.Context, common.Address) (*safe.State, uint64, error) {
	r.calls++
	if r.err != nil {
		return nil, 0, r.err
	}
	return &safe.State{Owners: r.owners, Threshold: 1}, 1, nil
}

func TestMessagingResourceTargetsRejectMalformedQueries(t *testing.T) {
	var a API
	for _, query := range []string{
		"", "?kind=want&resourceId=0", "?kind=catalog&resourceId=1e4",
		"?kind=voucher&resourceId=12", "?kind=post&resourceId=12",
		"?kind=want&kind=catalog&resourceId=12", "?kind=want&resourceId=1&resourceId=2",
	} {
		t.Run(query, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, resourceTargetsPath+query, nil)
			res := httptest.NewRecorder()
			require.NoError(t, a.messagingResourceTargetsHandler(echo.New().NewContext(req, res)))
			require.Equal(t, http.StatusBadRequest, res.Code, res.Body.String())
			require.Equal(t, "private, no-store", res.Header().Get("Cache-Control"))
		})
	}
}

func TestMessagingResourceTargetsDB(t *testing.T) {
	f := newMessagingFixture(t)
	viewer, owner, signer := f.approved(), f.approved(), f.approved()
	unapproved := f.user("USER", "PENDING")
	ctx := t.Context()
	pg := f.a.store.Pool()
	poolAddress, voucherAddress, safe := randomAddress(t), randomAddress(t), randomAddress(t)
	_, err := pg.Exec(ctx, `INSERT INTO pools(chain_id,address,name,active,owner_address) VALUES($1,$2,$3,true,$4)`,
		f.a.chainID, poolAddress, messagingTestMarker, owner.Address)
	require.NoError(t, err)
	_, err = pg.Exec(ctx, `INSERT INTO vouchers(chain_id,address,name,symbol,active,owner_address) VALUES($1,$2,$3,'MSGTG',true,$4)`,
		f.a.chainID, voucherAddress, messagingTestMarker, safe)
	require.NoError(t, err)
	tx, err := pg.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	changed, err := f.a.store.ReplaceSafeSigners(ctx, tx, f.a.chainID, safe, []string{viewer.Address, signer.Address, unapproved.Address}, 2, 1, 1)
	require.NoError(t, err)
	require.True(t, changed)
	require.NoError(t, tx.Commit(ctx))
	t.Cleanup(func() {
		_, err := pg.Exec(context.Background(), `DELETE FROM safe_state WHERE chain_id=$1 AND safe_address=lower($2)`, f.a.chainID, safe)
		require.NoError(t, err)
	})

	var wantID, catalogID int64
	require.NoError(t, pg.QueryRow(ctx, `
		INSERT INTO wants(owner_user_id,name,description,location_name,geo_lat,geo_lng,status)
		VALUES($1,$2,'Target fixture','Target fixture',-3.68,39.83,'OPEN') RETURNING id`, owner.ID, messagingTestMarker).Scan(&wantID))
	require.NoError(t, pg.QueryRow(ctx, `
		INSERT INTO catalogs(owner_user_id,name,description,location_name,geo_lat,geo_lng)
		VALUES($1,$2,'Target fixture','Target fixture',-3.68,39.83) RETURNING id`, owner.ID, messagingTestMarker).Scan(&catalogID))

	poolQuery := "?kind=pool&resourceId=" + strings.ToLower(poolAddress)
	require.Equal(t, http.StatusUnauthorized, resourceGet(t, f.a, resourceTargetsPath+poolQuery, "").Code)
	for _, tc := range []struct {
		query    string
		user     msgUser
		want     []messagingPeer
		selfOnly bool
	}{
		{poolQuery, viewer, []messagingPeer{{Address: owner.Address}}, false},
		{poolQuery, owner, []messagingPeer{}, true},
		{"?kind=voucher&resourceId=" + voucherAddress, viewer, []messagingPeer{{Address: signer.Address}}, false},
		{"?kind=want&resourceId=" + strconv.FormatInt(wantID, 10), viewer, []messagingPeer{{Address: owner.Address}}, false},
		{"?kind=catalog&resourceId=" + strconv.FormatInt(catalogID, 10), viewer, []messagingPeer{{Address: owner.Address}}, false},
		{"?kind=want&resourceId=" + strconv.FormatInt(wantID, 10), owner, []messagingPeer{}, true},
		{"?kind=catalog&resourceId=" + strconv.FormatInt(catalogID, 10), owner, []messagingPeer{}, true},
		{"?kind=pool&resourceId=" + randomAddress(t), viewer, []messagingPeer{}, false},
	} {
		response := resourceGet(t, f.a, resourceTargetsPath+tc.query, tc.user.Token)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"))
		var wire struct {
			Result struct {
				Targets  []messagingPeer `json:"targets"`
				SelfOnly bool            `json:"selfOnly"`
			} `json:"result"`
		}
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &wire))
		require.Equal(t, tc.want, wire.Result.Targets, tc.query)
		require.Equal(t, tc.selfOnly, wire.Result.SelfOnly, tc.query)
	}

	_, err = pg.Exec(ctx, `UPDATE pools SET active=false WHERE chain_id=$1 AND address=$2`, f.a.chainID, poolAddress)
	require.NoError(t, err)
	response := resourceGet(t, f.a, resourceTargetsPath+poolQuery, viewer.Token)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.JSONEq(t, `{"ok":true,"result":{"targets":[],"selfOnly":false}}`, response.Body.String())

	tx, err = pg.Begin(ctx)
	require.NoError(t, err)
	_, err = f.a.store.ReplaceSafeSigners(ctx, tx, f.a.chainID, safe, []string{viewer.Address, unapproved.Address}, 1, 2, 1)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
	response = resourceGet(t, f.a, resourceTargetsPath+"?kind=voucher&resourceId="+voucherAddress, viewer.Token)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.JSONEq(t, `{"ok":true,"result":{"targets":[],"selfOnly":true}}`, response.Body.String(),
		"a Safe with the caller as its only eligible signer has a self-only recipient")

	legacySafe := randomAddress(t)
	_, err = pg.Exec(ctx, `UPDATE pools SET active=true, owner_address=$3 WHERE chain_id=$1 AND address=$2`, f.a.chainID, poolAddress, legacySafe)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pg.Exec(context.Background(), `DELETE FROM safe_state WHERE chain_id=$1 AND safe_address=lower($2)`, f.a.chainID, legacySafe)
		require.NoError(t, err)
	})
	reader := &resourceTargetsSafeReader{owners: []common.Address{common.HexToAddress(signer.Address)}, err: context.DeadlineExceeded}
	f.a.safeReader = reader
	require.GreaterOrEqual(t, resourceGet(t, f.a, resourceTargetsPath+poolQuery, viewer.Token).Code, 500)
	reader.err = nil
	for range 2 {
		response := resourceGet(t, f.a, resourceTargetsPath+poolQuery, viewer.Token)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), signer.Address)
	}
	require.Equal(t, 2, reader.calls, "a failed inspection retries, then uses the durable projection")
}
