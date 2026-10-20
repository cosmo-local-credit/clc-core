package sub

import (
	"context"
	"testing"

	"github.com/grassrootseconomics/eth-tracker/pkg/event"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func (r *recordingStore) ProjectCatalogOwnership(_ context.Context, _ pgx.Tx, chain int64, address, owner string, block, index uint64) (int64, error) {
	r.calls = append(r.calls, call{"ownership", []any{chain, address, owner, block, index}})
	return 1, nil
}

func TestOwnershipProjectionWithoutDeploymentOTX(t *testing.T) {
	s, r := newTestSub()
	s.chainID = 100
	evt := event.Event{Success: true, TxType: "OWNERSHIP_TRANSFERRED", ContractAddress: testPool, Block: 42, Index: 7, Payload: map[string]any{"newOwner": testToken}}
	require.NoError(t, s.projectCatalogOwnership(context.Background(), nil, evt))
	require.Equal(t, []call{{"ownership", []any{int64(100), testPool, testToken, uint64(42), uint64(7)}}}, r.calls)
	r.calls = nil
	evt.Success = false
	require.NoError(t, s.projectCatalogOwnership(context.Background(), nil, evt))
	require.Empty(t, r.calls)
	evt.Success = true
	evt.Payload["newOwner"] = "invalid"
	require.Error(t, s.projectCatalogOwnership(context.Background(), nil, evt))
	require.Empty(t, r.calls)
}
