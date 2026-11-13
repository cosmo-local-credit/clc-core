package sub

import (
	"context"
	"log/slog"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/router"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	trackerEvent "github.com/grassrootseconomics/eth-tracker/pkg/event"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type (
	call struct {
		method string
		args   []any
	}

	// Only the four projection methods are implemented. The embedded interface is
	// nil, so any other call panics rather than passing silently — this test is
	// about which write a given event produces, and a wrong one must not go unseen.
	recordingStore struct {
		store.Store

		calls       []call
		positions   []router.Position
		settlements []store.RoutedSwapSettlement
	}
)

const (
	testRegistry   = "0xaDfB1e9CA7de2c0718fe7F4F2750e2e81479327d"
	testLimiter    = "0xea5Cda0799d664cdAF5572cEc16b792eA4789619"
	testQuoter     = "0x53dce1905467947Fa6b0d47f265ef989582C8133"
	testPool       = "0xF8fF590bd6Ef583e9D790eF0248ab69591f02e3E"
	testToken      = "0xE74B7fDeda8f66A01dC438A012b1eE5cc377298D"
	testAggregator = "0x0826492a24b1dBd1d8fcB4701b38C557CE685e9D"
)

var (
	recordedEdge = router.EdgeKey{PoolID: 7, VoucherID: 9}
)

func (r *recordingStore) SetPoolTokenMembership(
	_ context.Context, _ pgx.Tx, registry, token string, allowed bool, pos router.Position,
) ([]router.EdgeKey, error) {
	r.calls = append(r.calls, call{"membership", []any{registry, token, allowed}})
	r.positions = append(r.positions, pos)
	return []router.EdgeKey{recordedEdge}, nil
}

func (r *recordingStore) SetPoolTokenLimit(
	_ context.Context, _ pgx.Tx, pool, token, limit string, pos router.Position,
) ([]router.EdgeKey, error) {
	r.calls = append(r.calls, call{"limit", []any{pool, token, limit}})
	r.positions = append(r.positions, pos)
	return []router.EdgeKey{recordedEdge}, nil
}

func (r *recordingStore) SetPoolTokenOracle(
	_ context.Context, _ pgx.Tx, quoter, token, aggregator string, pos router.Position,
) ([]router.EdgeKey, error) {
	r.calls = append(r.calls, call{"oracle", []any{quoter, token, aggregator}})
	r.positions = append(r.positions, pos)
	return []router.EdgeKey{recordedEdge}, nil
}

func (r *recordingStore) ClearPoolTokenOracle(
	_ context.Context, _ pgx.Tx, quoter, token string, pos router.Position,
) ([]router.EdgeKey, error) {
	r.calls = append(r.calls, call{"clearOracle", []any{quoter, token}})
	r.positions = append(r.positions, pos)
	return []router.EdgeKey{recordedEdge}, nil
}

func (r *recordingStore) SetPoolTokenPriceIndex(
	_ context.Context, _ pgx.Tx, quoter, token, value string, pos router.Position,
) ([]router.EdgeKey, error) {
	r.calls = append(r.calls, call{"price", []any{quoter, token, value}})
	r.positions = append(r.positions, pos)
	return []router.EdgeKey{recordedEdge}, nil
}

func newTestSub() (*Sub, *recordingStore) {
	recorder := &recordingStore{}
	return &Sub{
		store: recorder,
		logg:  slog.New(slog.DiscardHandler),
	}, recorder
}

func project(t *testing.T, evt trackerEvent.Event) []call {
	t.Helper()
	sub, recorder := newTestSub()
	_, err := sub.projectPoolComposition(context.Background(), nil, evt)
	require.NoError(t, err)
	return recorder.calls
}

func TestProjectPoolCompositionRoutesEachContractsEvent(t *testing.T) {
	for _, tt := range []struct {
		name string
		evt  trackerEvent.Event
		want call
	}{
		{
			name: "registry admits a token",
			evt: trackerEvent.Event{
				Success: true, TxType: "INDEX_ADD", ContractAddress: testRegistry,
				Payload: map[string]any{"address": testToken},
			},
			want: call{"membership", []any{testRegistry, testToken, true}},
		},
		{
			name: "registry drops a token",
			evt: trackerEvent.Event{
				Success: true, TxType: "INDEX_REMOVE", ContractAddress: testRegistry,
				Payload: map[string]any{"address": testToken},
			},
			want: call{"membership", []any{testRegistry, testToken, false}},
		},
		{
			// The limiter's own address is irrelevant: the cap belongs to the
			// holder, and resolving by contract instead would key every pool
			// sharing a limiter to the same row.
			name: "limiter caps the holding pool",
			evt: trackerEvent.Event{
				Success: true, TxType: "LIMIT_SET", ContractAddress: testLimiter,
				Payload: map[string]any{"token": testToken, "holder": testPool, "value": "1000000000"},
			},
			want: call{"limit", []any{testPool, testToken, "1000000000"}},
		},
		{
			name: "quoter maps a feed",
			evt: trackerEvent.Event{
				Success: true, TxType: "ORACLE_UPDATED", ContractAddress: testQuoter,
				Payload: map[string]any{"token": testToken, "oracle": testAggregator},
			},
			want: call{"oracle", []any{testQuoter, testToken, testAggregator}},
		},
		{
			name: "quoter drops a feed",
			evt: trackerEvent.Event{
				Success: true, TxType: "ORACLE_REMOVED", ContractAddress: testQuoter,
				Payload: map[string]any{"token": testToken},
			},
			want: call{"clearOracle", []any{testQuoter, testToken}},
		},
		{
			name: "relative quoter sets a price index",
			evt: trackerEvent.Event{
				Success: true, TxType: "QUOTER_PRICE_INDEX_UPDATED", ContractAddress: testQuoter,
				Payload: map[string]any{"token": testToken, "exchangeRate": "10000000"},
			},
			want: call{"price", []any{testQuoter, testToken, "10000000"}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, []call{tt.want}, project(t, tt.evt))
		})
	}
}

func TestProjectPoolCompositionIgnoresWhatCannotHaveChangedState(t *testing.T) {
	for _, tt := range []struct {
		name string
		evt  trackerEvent.Event
	}{
		{
			// eth-tracker reports calls decoded from input data with Success
			// false. Nothing happened on chain, so nothing may happen here.
			name: "reverted call",
			evt: trackerEvent.Event{
				Success: false, TxType: "INDEX_ADD", ContractAddress: testRegistry,
				Payload: map[string]any{"address": testToken},
			},
		},
		{
			name: "unrelated event type",
			evt: trackerEvent.Event{
				Success: true, TxType: "TOKEN_TRANSFER", ContractAddress: testToken,
				Payload: map[string]any{"from": testPool, "to": testPool},
			},
		},
		{
			name: "payload missing its address",
			evt: trackerEvent.Event{
				Success: true, TxType: "INDEX_ADD", ContractAddress: testRegistry,
				Payload: map[string]any{},
			},
		},
		{
			name: "payload address is not an address",
			evt: trackerEvent.Event{
				Success: true, TxType: "ORACLE_UPDATED", ContractAddress: testQuoter,
				Payload: map[string]any{"token": testToken, "oracle": "not-an-address"},
			},
		},
		{
			// A uint256 arriving as a JSON number would already have lost
			// precision; refusing it is better than storing a rounded cap.
			name: "limit value is not a decimal string",
			evt: trackerEvent.Event{
				Success: true, TxType: "LIMIT_SET", ContractAddress: testLimiter,
				Payload: map[string]any{"token": testToken, "holder": testPool, "value": float64(1e9)},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Empty(t, project(t, tt.evt))
		})
	}
}

// The projection hands the event's position to the store and returns, as
// deltas, exactly the rows the store reports it changed.
func TestProjectPoolCompositionCarriesPositionAndDeltas(t *testing.T) {
	sub, recorder := newTestSub()
	deltas, err := sub.projectPoolComposition(context.Background(), nil, trackerEvent.Event{
		Success: true, TxType: "LIMIT_SET", ContractAddress: testLimiter, Block: 77, Index: 4,
		Payload: map[string]any{"token": testToken, "holder": testPool, "value": "123"},
	})
	require.NoError(t, err)
	require.Equal(t, []router.Position{{Block: 77, LogIndex: 4}}, recorder.positions)
	require.Len(t, deltas, 1)
	limit, ok := deltas[0].(router.EdgeLimitDelta)
	require.True(t, ok)
	assert.Equal(t, recordedEdge, limit.Key)
	assert.Equal(t, uint64(123), limit.Limit.Uint64())
	assert.Equal(t, router.Position{Block: 77, LogIndex: 4}, limit.Pos)
}

// The tracker sends the log index; the pinned module's struct drops it.
func TestDecodeEventKeepsTheLogIndex(t *testing.T) {
	evt, err := decodeEvent([]byte(`{"block":12,"index":5,"transactionType":"LIMIT_SET","success":true,"payload":{}}`))
	require.NoError(t, err)
	assert.Equal(t, uint(5), evt.Index)
	assert.Equal(t, router.Position{Block: 12, LogIndex: 5}, eventPosition(evt))
}

func TestRouterSignalsMarkPoolBalancesAndRefreshes(t *testing.T) {
	swap := routerSignals(trackerEvent.Event{
		Success: true, TxType: "POOL_SWAP", ContractAddress: testPool,
		Payload: map[string]any{"tokenIn": testToken, "tokenOut": testAggregator},
	})
	assert.Len(t, swap, 2)
	transfer := routerSignals(trackerEvent.Event{
		Success: true, TxType: "TOKEN_TRANSFER", ContractAddress: testToken,
		Payload: map[string]any{"from": testPool, "to": testLimiter},
	})
	require.Len(t, transfer, 1)
	parties, ok := transfer[0].(router.TransferDelta)
	require.True(t, ok)
	assert.Len(t, parties.Parties, 2)
	seal := routerSignals(trackerEvent.Event{Success: true, TxType: "SEAL_STATE_CHANGE", ContractAddress: testPool})
	require.Len(t, seal, 1)
	assert.IsType(t, router.RefreshPoolDelta{}, seal[0])
	assert.Empty(t, routerSignals(trackerEvent.Event{Success: false, TxType: "POOL_SWAP", ContractAddress: testPool}))
}

func (r *recordingStore) SettleRoutedSwapHop(
	_ context.Context, _ pgx.Tx, trackingID string, settlement store.RoutedSwapSettlement,
) (bool, error) {
	r.calls = append(r.calls, call{"settle", []any{trackingID}})
	r.settlements = append(r.settlements, settlement)
	return true, nil
}

// A pool's settlement event fills the tracked route's hop with the amounts
// SwapSettlement reports; anything else settles nothing.
func TestSettleRoutedSwapMapsTheSettlementEvent(t *testing.T) {
	sub, recorder := newTestSub()
	evt := trackerEvent.Event{
		Success: true, TxType: "POOL_SWAP", ContractAddress: testPool, TxHash: "0xsettle", Block: 9, Index: 4,
		Payload: map[string]any{
			"initiator": testRegistry, "tokenIn": testToken, "tokenOut": testAggregator,
			"amountIn": "1000000", "amountOut": "999800", "fee": "0",
			"quotedAmountOut": "1000000", "nominalAmountOut": "999800", "protocolFee": "200",
		},
	}
	require.NoError(t, sub.settleRoutedSwap(context.Background(), nil, "tracking-1", evt))
	require.Len(t, recorder.settlements, 1)
	got := recorder.settlements[0]
	assert.Equal(t, testPool, got.Pool)
	assert.Equal(t, testToken, got.TokenIn)
	assert.Equal(t, testAggregator, got.TokenOut)
	assert.Equal(t, "999800", got.AmountOut)
	require.NotNil(t, got.ProtocolFee)
	assert.Equal(t, "200", *got.ProtocolFee)
	assert.Equal(t, uint64(9), got.Block)
	assert.Equal(t, uint32(4), got.LogIndex)

	// Legacy Swap events carry no settlement detail; the optional fields stay nil.
	legacy := evt
	legacy.Payload = map[string]any{"tokenIn": testToken, "tokenOut": testAggregator, "amountIn": "5", "amountOut": "4", "fee": "1"}
	require.NoError(t, sub.settleRoutedSwap(context.Background(), nil, "tracking-1", legacy))
	assert.Nil(t, recorder.settlements[1].QuotedAmountOut)

	for _, skip := range []trackerEvent.Event{
		{Success: false, TxType: "POOL_SWAP", ContractAddress: testPool, Payload: evt.Payload},
		{Success: true, TxType: "TOKEN_TRANSFER", ContractAddress: testPool, Payload: evt.Payload},
	} {
		require.NoError(t, sub.settleRoutedSwap(context.Background(), nil, "tracking-1", skip))
	}
	require.NoError(t, sub.settleRoutedSwap(context.Background(), nil, "", evt))
	assert.Len(t, recorder.settlements, 2, "reverted, unrelated and untracked events settle nothing")
}

func TestFeeEventsReachTheRouter(t *testing.T) {
	pair := routerSignals(trackerEvent.Event{
		Success: true, TxType: "FEE_PAIR_UPDATED", ContractAddress: testQuoter, Block: 50,
		Payload: map[string]any{"tokenIn": testToken, "tokenOut": testAggregator, "oldFee": "0", "newFee": "3000"},
	})
	require.Len(t, pair, 1)
	d, ok := pair[0].(router.PairFeeDelta)
	require.True(t, ok)
	assert.Equal(t, uint64(3_000), d.FeePPM)
	assert.False(t, d.Removed)
	assert.Equal(t, uint64(50), d.Block)

	removed := routerSignals(trackerEvent.Event{
		Success: true, TxType: "FEE_PAIR_REMOVED", ContractAddress: testQuoter,
		Payload: map[string]any{"tokenIn": testToken, "tokenOut": testAggregator},
	})
	require.Len(t, removed, 1)
	r, ok := removed[0].(router.PairFeeDelta)
	require.True(t, ok)
	assert.True(t, r.Removed)

	for _, kind := range []string{"FEE_DEFAULT_UPDATED", "PROTOCOL_FEE_UPDATED", "PROTOCOL_FEE_RECIPIENT_UPDATED", "PROTOCOL_FEE_ACTIVE_UPDATED"} {
		signals := routerSignals(trackerEvent.Event{Success: true, TxType: kind, ContractAddress: testQuoter})
		require.Len(t, signals, 1, kind)
		assert.IsType(t, router.RefreshPoolDelta{}, signals[0], kind)
	}
	assert.Empty(t, routerSignals(trackerEvent.Event{
		Success: true, TxType: "FEE_PAIR_UPDATED", ContractAddress: testQuoter,
		Payload: map[string]any{"tokenIn": testToken, "tokenOut": testAggregator, "newFee": "2000000"},
	}), "a fee above 100 % is refused")

	zero := routerSignals(trackerEvent.Event{
		Success: true, TxType: "FEE_PAIR_UPDATED", ContractAddress: testQuoter,
		Payload: map[string]any{"tokenIn": testToken, "tokenOut": testAggregator, "oldFee": "3000", "newFee": "0"},
	})
	require.Len(t, zero, 1)
	z, ok := zero[0].(router.PairFeeDelta)
	require.True(t, ok)
	assert.True(t, z.Removed, "a zero pair fee means the default applies, as FeePolicy.getFee reads it")

	assert.Empty(t, routerSignals(trackerEvent.Event{
		Success: false, TxType: "FEE_PAIR_UPDATED", ContractAddress: testQuoter,
		Payload: map[string]any{"tokenIn": testToken, "tokenOut": testAggregator, "newFee": "3000"},
	}), "a reverted setter changed nothing")
}
