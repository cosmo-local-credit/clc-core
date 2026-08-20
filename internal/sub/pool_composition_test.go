package sub

import (
	"context"
	"log/slog"
	"testing"

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

		calls []call
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

func (r *recordingStore) SetPoolTokenMembership(
	_ context.Context, _ pgx.Tx, registry, token string, allowed bool,
) (int64, error) {
	r.calls = append(r.calls, call{"membership", []any{registry, token, allowed}})
	return 1, nil
}

func (r *recordingStore) SetPoolTokenLimit(
	_ context.Context, _ pgx.Tx, pool, token, limit string,
) (int64, error) {
	r.calls = append(r.calls, call{"limit", []any{pool, token, limit}})
	return 1, nil
}

func (r *recordingStore) SetPoolTokenOracle(
	_ context.Context, _ pgx.Tx, quoter, token, aggregator string,
) (int64, error) {
	r.calls = append(r.calls, call{"oracle", []any{quoter, token, aggregator}})
	return 1, nil
}

func (r *recordingStore) ClearPoolTokenOracle(
	_ context.Context, _ pgx.Tx, quoter, token string,
) (int64, error) {
	r.calls = append(r.calls, call{"clearOracle", []any{quoter, token}})
	return 1, nil
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
	require.NoError(t, sub.projectPoolComposition(context.Background(), nil, evt))
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
