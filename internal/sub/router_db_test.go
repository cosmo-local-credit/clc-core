package sub

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/router"
	"github.com/cosmo-local-credit/clc-core/internal/router/routertest"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	trackerEvent "github.com/grassrootseconomics/eth-tracker/pkg/event"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func routerStore(t *testing.T) store.Store {
	t.Helper()
	dsn := os.Getenv("CORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CORE_TEST_DSN not set; skipping subscriber router tests")
	}
	st, err := store.NewPgStore(store.PgOpts{
		DSN: dsn, Logg: slog.New(slog.DiscardHandler),
		MigrationsFolderPath: "../../migrations", QueriesFolderPath: "../../core_queries.sql",
	})
	require.NoError(t, err)
	t.Cleanup(st.Pool().Close)
	return st
}

func limitEvent(t *testing.T, n *routertest.Network, value string, block uint64) []byte {
	t.Helper()
	raw, err := json.Marshal(trackerEvent.Event{
		Block: block, Success: true, TxType: "LIMIT_SET", TxHash: "0xrouter-wiring",
		ContractAddress: n.Chain.Pools[n.P1].Limiter.Hex(),
		Payload:         map[string]any{"token": n.A.Hex(), "holder": n.P1.Hex(), "value": value},
	})
	require.NoError(t, err)
	// The tracker sends the log index; the pinned module's struct drops it.
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	m["index"] = 3
	raw, err = json.Marshal(m)
	require.NoError(t, err)
	return raw
}

func edgeLimit(svc *router.Service, n *routertest.Network) string {
	snap := svc.Snapshot()
	pool, _ := snap.PoolByAddress(n.P1)
	token, _ := snap.TokenByAddress(n.A)
	edge, ok := snap.Edge(pool, token)
	if !ok {
		return ""
	}
	return edge.Limit.Dec()
}

// The prototype built its Handler without the graph service, so no event ever
// reached the graph. This proves the wiring: an event processed by the
// subscriber lands in the live graph after its transaction commits, with no
// reload in between.
func TestEventReachesTheLiveGraph(t *testing.T) {
	st := routerStore(t)
	n := routertest.SeedNetwork(t, st.Pool())
	svc := router.New(router.Opts{
		Config: router.Config{ChainID: n.ChainID, MaxHops: 2, ReloadInterval: time.Hour},
		Store:  st, Relay: n.Relay(),
	})
	require.NoError(t, svc.Load(context.Background()))
	ctx, cancel := context.WithCancel(context.Background())
	svc.Run(ctx)
	t.Cleanup(func() { cancel(); svc.Stop() })

	s := &Sub{store: st, router: svc, chainID: n.ChainID, logg: slog.New(slog.DiscardHandler)}
	require.Equal(t, "10000000000", edgeLimit(svc, n))
	before := svc.Snapshot().Version

	require.NoError(t, s.processEvent(context.Background(), "TRACKER.LIMIT_SET", limitEvent(t, n, "4242", 500)))
	require.Eventually(t, func() bool { return edgeLimit(svc, n) == "4242" }, 5*time.Second, 10*time.Millisecond)
	assert.Greater(t, svc.Snapshot().Version, before)

	// A redelivered older event changes neither the database nor the graph.
	require.NoError(t, s.processEvent(context.Background(), "TRACKER.LIMIT_SET", limitEvent(t, n, "1", 499)))
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, "4242", edgeLimit(svc, n))
	var limit string
	require.NoError(t, st.Pool().QueryRow(context.Background(),
		`SELECT token_limit::TEXT FROM pool_tokens WHERE pool_id = $1 AND voucher_id = $2`,
		n.PoolIDs[n.P1], n.VoucherIDs[n.A]).Scan(&limit))
	assert.Equal(t, "4242", limit)
}

type (
	// failingOTXStore lets every projection run, then fails the OTX lookup, which
	// rolls the event's transaction back.
	failingOTXStore struct{ store.Store }

	recordingSink struct {
		mu     sync.Mutex
		deltas []router.Delta
	}
)

func (failingOTXStore) GetOTXByTxHash(context.Context, pgx.Tx, string) (store.OTX, error) {
	return store.OTX{}, errors.New("lookup failed")
}

func (r *recordingSink) Apply(deltas ...router.Delta) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deltas = append(r.deltas, deltas...)
}

func TestFailedTransactionEmitsNoDelta(t *testing.T) {
	st := routerStore(t)
	n := routertest.SeedNetwork(t, st.Pool())
	sink := &recordingSink{}
	s := &Sub{store: failingOTXStore{st}, router: sink, chainID: n.ChainID, logg: slog.New(slog.DiscardHandler)}

	err := s.processEvent(context.Background(), "TRACKER.LIMIT_SET", limitEvent(t, n, "777", 600))
	require.Error(t, err)
	assert.Empty(t, sink.deltas, "the graph must not reflect a rolled-back write")
	var limit string
	require.NoError(t, st.Pool().QueryRow(context.Background(),
		`SELECT token_limit::TEXT FROM pool_tokens WHERE pool_id = $1 AND voucher_id = $2`,
		n.PoolIDs[n.P1], n.VoucherIDs[n.A]).Scan(&limit))
	assert.Equal(t, "10000000000", limit)

	committed := &Sub{store: st, router: sink, chainID: n.ChainID, logg: slog.New(slog.DiscardHandler)}
	require.NoError(t, committed.processEvent(context.Background(), "TRACKER.LIMIT_SET", limitEvent(t, n, "777", 600)))
	require.Len(t, sink.deltas, 1)
	delta, ok := sink.deltas[0].(router.EdgeLimitDelta)
	require.True(t, ok)
	assert.Equal(t, router.Position{Block: 600, LogIndex: 3}, delta.Pos)
}

// A price index event projects by quoter and emits a delta.
func TestPriceIndexEventProjects(t *testing.T) {
	st := routerStore(t)
	n := routertest.SeedNetwork(t, st.Pool())
	sink := &recordingSink{}
	s := &Sub{store: st, router: sink, chainID: n.ChainID, logg: slog.New(slog.DiscardHandler)}
	raw, err := json.Marshal(trackerEvent.Event{
		Block: 700, Success: true, TxType: "QUOTER_PRICE_INDEX_UPDATED", TxHash: "0xprice",
		ContractAddress: n.Chain.Pools[n.P3].Quoter.Hex(),
		Payload:         map[string]any{"token": n.C.Hex(), "exchangeRate": "3000000"},
	})
	require.NoError(t, err)
	require.NoError(t, s.processEvent(context.Background(), "TRACKER.QUOTER_PRICE_INDEX_UPDATED", raw))
	require.Len(t, sink.deltas, 1)
	price, ok := sink.deltas[0].(router.EdgePriceDelta)
	require.True(t, ok)
	assert.Equal(t, uint64(3_000_000), price.Index.Uint64())
	var stored string
	require.NoError(t, st.Pool().QueryRow(context.Background(),
		`SELECT price_index::TEXT FROM pool_tokens WHERE pool_id = $1 AND voucher_id = $2`,
		n.PoolIDs[n.P3], n.VoucherIDs[n.C]).Scan(&stored))
	assert.Equal(t, "3000000", stored)
}
