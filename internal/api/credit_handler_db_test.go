package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/router"
	"github.com/cosmo-local-credit/clc-core/internal/router/routertest"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type (
	creditHarness struct {
		api   *API
		priv  ed25519.PrivateKey
		net   *routertest.Network
		svc   *router.Service
		token string
	}
)

func newCreditHarness(t *testing.T, withRouter bool) *creditHarness {
	t.Helper()
	dsn := os.Getenv("CORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CORE_TEST_DSN not set; skipping credit router API tests")
	}
	logg := slog.New(slog.DiscardHandler)
	pgStore, err := store.NewPgStore(store.PgOpts{
		Logg: logg, DSN: dsn, MigrationsFolderPath: "../../migrations", QueriesFolderPath: "../../core_queries.sql",
	})
	require.NoError(t, err)
	t.Cleanup(pgStore.Pool().Close)

	ctx := context.Background()
	migrator, err := rivermigrate.New(riverpgxv5.New(pgStore.Pool()), nil)
	require.NoError(t, err)
	_, err = migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	require.NoError(t, err)
	queue, err := river.NewClient[pgx.Tx](riverpgxv5.New(pgStore.Pool()), &river.Config{})
	require.NoError(t, err)

	n := routertest.SeedNetwork(t, pgStore.Pool())
	h := &creditHarness{net: n}
	var svc *router.Service
	if withRouter {
		svc = router.New(router.Opts{
			Config: router.Config{ChainID: n.ChainID, MaxHops: 2},
			Store:  pgStore, Chain: n.Chain, Relay: n.Relay(), Logg: logg,
		})
		require.NoError(t, svc.Load(ctx))
		require.NoError(t, svc.ReconcileOnce(ctx))
		h.svc = svc
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	h.priv = priv
	h.api = New(APIOpts{
		Logg: logg, CORS: []string{"https://app.example"}, Store: pgStore,
		SigningKey: priv, VerifyingKey: pub, QueueClient: queue, ChainID: n.ChainID, Router: svc,
	})
	h.token = offeringToken(t, priv, n.Wallet.Hex(), "USER")
	return h
}

func (h *creditHarness) do(t *testing.T, method, path string, body any) (int, http.Header, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if h.token != "" {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
	rec := httptest.NewRecorder()
	h.api.router.ServeHTTP(rec, req)
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	return rec.Code, rec.Header(), out
}

func result(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	return asMap(t, body["result"])
}

func TestCreditReachThroughTheAPI(t *testing.T) {
	h := newCreditHarness(t, true)
	n := h.net

	status, header, body := h.do(t, http.MethodGet, "/api/v2/users/me/credit?depth=1", nil)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "private, no-store", header.Get("Cache-Control"))
	r := result(t, body)
	assert.EqualValues(t, 1, r["depth"])
	assert.Len(t, r["vouchers"], 2)
	assert.Len(t, r["pools"], 2)
	inputs := asList(t, r["inputs"])
	require.Len(t, inputs, 1)
	assert.Equal(t, n.A.Hex(), asMap(t, inputs[0])["voucher"])
	assert.Equal(t, "5000000", asMap(t, inputs[0])["balance"])

	status, _, body = h.do(t, http.MethodGet, "/api/v2/users/me/credit?depth=2", nil)
	require.Equal(t, http.StatusOK, status)
	r = result(t, body)
	assert.Len(t, r["vouchers"], 3)
	assert.Len(t, r["pools"], 4)
	assert.NotEmpty(t, r["digest"])

	status, _, body = h.do(t, http.MethodGet, "/api/v2/users/me/credit?depth=3", nil)
	assert.Equal(t, http.StatusBadRequest, status, body)

	n.Chain.Lock()
	n.Chain.Down = true
	n.Chain.Unlock()
	h.svc.Forget(n.Wallet)
	status, _, body = h.do(t, http.MethodGet, "/api/v2/users/me/credit?depth=2", nil)
	assert.Equal(t, http.StatusServiceUnavailable, status, "unavailable is never an empty result")
	assert.Equal(t, "E27", body["code"])
}

func TestCreditRoutesThroughTheAPI(t *testing.T) {
	h := newCreditHarness(t, true)
	n := h.net

	status, _, body := h.do(t, http.MethodGet, "/api/v2/users/me/credit/routes?to="+n.C.Hex()+"&amount=1000000&mode=exactInput", nil)
	require.Equal(t, http.StatusOK, status, body)
	r := result(t, body)
	assert.Equal(t, true, r["complete"])
	assert.Nil(t, r["cursor"])
	target := asMap(t, r["target"])
	assert.Equal(t, n.C.Hex(), target["address"])
	inputs := asList(t, r["inputs"])
	require.Len(t, inputs, 1)
	routes := asList(t, asMap(t, inputs[0])["routes"])
	require.Len(t, routes, 2)
	best := asMap(t, routes[0])
	assert.Len(t, best["hops"], 2)
	assert.Equal(t, true, best["carriesAmount"])
	assert.Equal(t, "999601", best["amountOut"])
	assert.Equal(t, "5000000", best["maxAmountIn"])
	assert.Equal(t, true, best["maxIsExact"])
	assert.Equal(t, "wallet", best["limitedBy"])
	hop := asMap(t, asList(t, best["hops"])[0])
	assert.Equal(t, "default", hop["feeSource"])
	assert.Equal(t, "20000", hop["protocolFeePpm"])
	assert.Len(t, r["fallbackPools"], 3)

	status, _, _ = h.do(t, http.MethodGet, "/api/v2/users/me/credit/routes", nil)
	assert.Equal(t, http.StatusBadRequest, status)
	status, _, _ = h.do(t, http.MethodGet, "/api/v2/users/me/credit/routes?to=0x0000000000000000000000000000000000000001", nil)
	assert.Equal(t, http.StatusNotFound, status)
}

func quoteBody(n *routertest.Network, amount string) map[string]any {
	return map[string]any{
		"route": []map[string]any{
			{"pool": n.P1.Hex(), "tokenIn": n.A.Hex(), "tokenOut": n.B.Hex()},
			{"pool": n.P2.Hex(), "tokenIn": n.B.Hex(), "tokenOut": n.C.Hex()},
		},
		"mode": "exactInput", "amount": amount,
	}
}

func TestCreditQuoteStates(t *testing.T) {
	h := newCreditHarness(t, true)
	n := h.net

	status, _, body := h.do(t, http.MethodPost, "/api/v2/users/me/credit/quote", quoteBody(n, "1000000"))
	require.Equal(t, http.StatusOK, status, body)
	r := result(t, body)
	assert.Equal(t, "999601", r["amountOut"])
	assert.Equal(t, "999601", r["minAmountOut"])
	batch := asMap(t, r["batch"])
	assert.Equal(t, r["quoteId"], batch["digest"])
	calls := asList(t, asMap(t, batch["batchedCall"])["calls"])
	assert.Len(t, calls, 6)
	assert.Equal(t, n.Relayer.Hex(), batch["executor"])
	assert.Equal(t, true, r["delegated"])
	assert.Len(t, r["hops"], 2)

	status, _, body = h.do(t, http.MethodPost, "/api/v2/users/me/credit/quote", quoteBody(n, "9000000"))
	require.Equal(t, http.StatusConflict, status, body)
	assert.Equal(t, "E24", body["code"])
	assert.Equal(t, "wallet_balance", asMap(t, body["result"])["reason"])

	n.Chain.Lock()
	n.Chain.SimRevert = crypto.Keccak256([]byte("Expired()"))[:4]
	n.Chain.Unlock()
	status, _, body = h.do(t, http.MethodPost, "/api/v2/users/me/credit/quote", quoteBody(n, "1000"))
	require.Equal(t, http.StatusConflict, status, body)
	assert.Equal(t, "E26", body["code"])
	assert.Equal(t, "Expired", asMap(t, asMap(t, body["result"])["revert"])["reason"])

	n.Chain.Lock()
	n.Chain.SimRevert = nil
	n.Chain.Down = true
	n.Chain.Unlock()
	status, _, body = h.do(t, http.MethodPost, "/api/v2/users/me/credit/quote", quoteBody(n, "1000"))
	assert.Equal(t, http.StatusServiceUnavailable, status, body)

	bad := quoteBody(n, "1000")
	bad["mode"] = "sideways"
	status, _, _ = h.do(t, http.MethodPost, "/api/v2/users/me/credit/quote", bad)
	assert.Equal(t, http.StatusBadRequest, status)
}

func TestCreditEndpointsWithoutRouterAreUnavailable(t *testing.T) {
	h := newCreditHarness(t, false)
	status, _, body := h.do(t, http.MethodGet, "/api/v2/users/me/credit", nil)
	assert.Equal(t, http.StatusServiceUnavailable, status)
	assert.Equal(t, "E27", body["code"])
}

func relayBodyFromQuote(t *testing.T, wallet common.Address, quote map[string]any) map[string]any {
	batch := asMap(t, quote["batch"])
	return map[string]any{
		"userAddress": wallet.Hex(),
		"signedBatch": map[string]any{
			"batchedCall": batch["batchedCall"],
			"nonce":       batch["nonce"],
			"keyHash":     batch["keyHash"],
			"executor":    batch["executor"],
			"deadline":    batch["deadline"],
		},
		"signature":     "0xabcdef",
		"signatureType": "secp256k1",
	}
}

// The relay records a quoted route for its grouped receipt, and refuses a
// route-shaped batch whose values were tampered with.
func TestRelayRecordsAndValidatesRoutes(t *testing.T) {
	h := newCreditHarness(t, true)
	n := h.net
	jobs := newRelayJobTracker(t, h.api)

	status, _, body := h.do(t, http.MethodPost, "/api/v2/users/me/credit/quote", quoteBody(n, "1000000"))
	require.Equal(t, http.StatusOK, status, body)
	relay := relayBodyFromQuote(t, n.Wallet, result(t, body))

	status, _, body = h.do(t, http.MethodPost, "/api/v2/relayer/relay", relay)
	require.Equal(t, http.StatusOK, status, body)
	trackingID := asString(t, result(t, body)["trackingId"])
	jobs.track(trackingID)
	t.Cleanup(func() {
		_, _ = h.api.store.Pool().Exec(context.Background(), `DELETE FROM routed_swaps WHERE tracking_id = $1::uuid`, trackingID)
	})

	status, _, body = h.do(t, http.MethodGet, "/api/v2/users/me/routed-swaps/"+trackingID, nil)
	require.Equal(t, http.StatusOK, status, body)
	swap := asMap(t, result(t, body)["swap"])
	assert.Equal(t, n.A.Hex(), swap["tokenIn"])
	assert.Equal(t, n.C.Hex(), swap["tokenOut"])
	assert.Equal(t, "999601", swap["minAmountOut"])
	assert.Equal(t, true, swap["bounded"])
	assert.Len(t, swap["hops"], 2)
	assert.Equal(t, false, result(t, body)["settled"])

	status, _, body = h.do(t, http.MethodGet, "/api/v2/users/me/routed-swaps", nil)
	require.Equal(t, http.StatusOK, status)
	assert.Len(t, result(t, body)["items"], 1)

	// Raise the approval above the withdrawn amount: still route-shaped,
	// no longer a valid route.
	tampered := relayBodyFromQuote(t, n.Wallet, result(t, func() map[string]any {
		_, _, b := h.do(t, http.MethodPost, "/api/v2/users/me/credit/quote", quoteBody(n, "1000000"))
		return b
	}()))
	calls := asList(t, asMap(t, asMap(t, tampered["signedBatch"])["batchedCall"])["calls"])
	approve := asMap(t, calls[1])
	approve["data"] = approvalCalldata(n.P1, bigInt("2000000"))
	status, _, body = h.do(t, http.MethodPost, "/api/v2/relayer/relay", tampered)
	assert.Equal(t, http.StatusBadRequest, status, body)
	assert.Contains(t, body["description"], "approval does not equal the withdrawn amount")
}

func bigInt(s string) *big.Int {
	v, _ := new(big.Int).SetString(s, 10)
	return v
}

// creditDepth resolves on the server, so the map and the list filter by the
// same reachable set before paging, counts and clustering.
func TestCreditDepthFiltersMapAndListIdentically(t *testing.T) {
	h := newCreditHarness(t, true)
	n := h.net
	ctx := context.Background()
	db := h.api.store.Pool()
	// The map only places resources with coordinates.
	for _, id := range n.PoolIDs {
		_, err := db.Exec(ctx, `UPDATE pools SET geo_lat = -1.29, geo_lng = 36.82 WHERE id = $1`, id)
		require.NoError(t, err)
	}
	for _, id := range n.VoucherIDs {
		_, err := db.Exec(ctx, `UPDATE vouchers SET geo_lat = -1.29, geo_lng = 36.82 WHERE id = $1`, id)
		require.NoError(t, err)
	}

	fixture := map[string]bool{}
	for addr := range n.PoolIDs {
		fixture[strings.ToLower(addr.Hex())] = true
	}
	for addr := range n.VoucherIDs {
		fixture[strings.ToLower(addr.Hex())] = true
	}
	mapSet := func(depth string) map[string]bool {
		status, header, body := h.do(t, http.MethodGet, "/api/v2/market-map/points?kinds=pool,voucher&creditDepth="+depth, nil)
		require.Equal(t, http.StatusOK, status, body)
		assert.Equal(t, "private, no-store", header.Get("Cache-Control"))
		out := map[string]bool{}
		for _, p := range asList(t, result(t, body)["points"]) {
			point := asMap(t, p)
			id := strings.ToLower(asString(t, point["resourceId"]))
			if fixture[id] {
				out[asString(t, point["kind"])+":"+id] = true
			}
		}
		return out
	}
	listSet := func(depth string) map[string]bool {
		status, _, body := h.do(t, http.MethodGet, "/api/v2/marketplace?kinds=pool,voucher&limit=100&creditDepth="+depth, nil)
		require.Equal(t, http.StatusOK, status, body)
		out := map[string]bool{}
		for _, r := range asList(t, result(t, body)["results"]) {
			row := asMap(t, r)
			address, _ := row["address"].(string)
			if fixture[strings.ToLower(address)] {
				out[asString(t, row["kind"])+":"+strings.ToLower(address)] = true
			}
		}
		return out
	}

	one, two := mapSet("1"), mapSet("2")
	assert.Equal(t, one, listSet("1"), "map and list filter identically at depth 1")
	assert.Equal(t, two, listSet("2"), "and at depth 2")
	assert.True(t, one["pool:"+strings.ToLower(n.P1.Hex())])
	assert.True(t, one["pool:"+strings.ToLower(n.P3.Hex())])
	assert.False(t, one["pool:"+strings.ToLower(n.P4.Hex())], "P4 is two hops away")
	assert.True(t, two["pool:"+strings.ToLower(n.P4.Hex())])
	assert.Greater(t, len(two), len(one))

	// A wallet that reaches nothing sees nothing, not everything.
	h.token = offeringToken(t, h.priv, "0x00000000000000000000000000000000000000c1", "USER")
	for key := range mapSet("2") {
		assert.Fail(t, "an empty reachable set matched "+key)
	}

	// creditDepth is private: it needs a session, and replaces creditablePools.
	h.token = ""
	status, _, _ := h.do(t, http.MethodGet, "/api/v2/market-map/points?creditDepth=1", nil)
	assert.Equal(t, http.StatusUnauthorized, status)
	h.token = offeringToken(t, h.priv, n.Wallet.Hex(), "USER")
	status, _, _ = h.do(t, http.MethodGet, "/api/v2/market-map/points?creditDepth=1&creditablePools=100:"+n.P1.Hex(), nil)
	assert.Equal(t, http.StatusBadRequest, status)
	status, _, _ = h.do(t, http.MethodGet, "/api/v2/reports?creditDepth=2", nil)
	assert.Equal(t, http.StatusOK, status)
}

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	require.True(t, ok, "want an object, got %v", v)
	return m
}

func asList(t *testing.T, v any) []any {
	t.Helper()
	l, ok := v.([]any)
	require.True(t, ok, "want an array, got %v", v)
	return l
}

func asString(t *testing.T, v any) string {
	t.Helper()
	s, ok := v.(string)
	require.True(t, ok, "want a string, got %v", v)
	return s
}
