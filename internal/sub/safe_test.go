package sub

import (
	"context"
	"errors"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/safe"
	"github.com/ethereum/go-ethereum/common"
	"github.com/grassrootseconomics/eth-tracker/pkg/event"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func (r *recordingStore) ReplaceSafeSigners(_ context.Context, _ pgx.Tx, chain int64, address string, signers []string, threshold int, block, index uint64) (bool, error) {
	r.calls = append(r.calls, call{"safe", []any{chain, address, signers, threshold, block, index}})
	return true, nil
}

type fakeSafeReader struct {
	state *safe.State
	err   error
	reads []uint64
}

func (f *fakeSafeReader) ReadSafe(_ context.Context, _ common.Address, block uint64) (*safe.State, error) {
	f.reads = append(f.reads, block)
	return f.state, f.err
}

var (
	ownerOne = common.HexToAddress("0x00000000000000000000000000000000000000a1")
	ownerTwo = common.HexToAddress("0x00000000000000000000000000000000000000a2")
)

func safeEvent(txType string) event.Event {
	return event.Event{Success: true, TxType: txType, ContractAddress: testPool, Block: 90, Index: 4,
		Payload: map[string]any{"newOwner": testToken, "change": "added", "owner": ownerTwo.Hex()}}
}

func TestSafeOwnersChangedReplacesTheWholeSetAtTheEventBlock(t *testing.T) {
	s, r := newTestSub()
	s.chainID = 100
	reader := &fakeSafeReader{state: &safe.State{Owners: []common.Address{ownerOne, ownerTwo}, Threshold: 2}}
	s.safeReader = reader

	require.NoError(t, s.projectSafeSigners(context.Background(), nil, safeEvent("SAFE_OWNERS_CHANGED")))
	require.Equal(t, []uint64{90}, reader.reads)
	require.Equal(t, []call{{"safe", []any{int64(100), testPool, []string{ownerOne.Hex(), ownerTwo.Hex()}, 2, uint64(90), uint64(4)}}}, r.calls)
}

func TestOwnershipTransferSeedsTheNewOwnerWhenItIsASafe(t *testing.T) {
	s, r := newTestSub()
	s.chainID = 100
	s.safeReader = &fakeSafeReader{state: &safe.State{Owners: []common.Address{ownerOne}, Threshold: 1}}

	require.NoError(t, s.projectSafeSigners(context.Background(), nil, safeEvent("OWNERSHIP_TRANSFERRED")))
	require.Equal(t, []call{{"safe", []any{int64(100), testToken, []string{ownerOne.Hex()}, 1, uint64(90), uint64(4)}}}, r.calls)
}

func TestOwnershipTransferToANonSafeIsIgnored(t *testing.T) {
	s, r := newTestSub()
	s.safeReader = &fakeSafeReader{err: safe.ErrNotSafe}
	require.NoError(t, s.projectSafeSigners(context.Background(), nil, safeEvent("OWNERSHIP_TRANSFERRED")))
	require.Empty(t, r.calls)
}

func TestSafeProjectionRetriesOnReadFailure(t *testing.T) {
	s, r := newTestSub()
	s.safeReader = &fakeSafeReader{err: errors.New("missing trie node")}
	require.Error(t, s.projectSafeSigners(context.Background(), nil, safeEvent("SAFE_OWNERS_CHANGED")))
	require.Empty(t, r.calls)
}

func TestSafeProjectionSkipsRevertedUnrelatedAndDisabled(t *testing.T) {
	s, r := newTestSub()
	reader := &fakeSafeReader{state: &safe.State{Owners: []common.Address{ownerOne}, Threshold: 1}}
	s.safeReader = reader

	reverted := safeEvent("SAFE_OWNERS_CHANGED")
	reverted.Success = false
	require.NoError(t, s.projectSafeSigners(context.Background(), nil, reverted))
	require.NoError(t, s.projectSafeSigners(context.Background(), nil, safeEvent("POOL_SWAP")))

	s.safeReader = nil
	require.NoError(t, s.projectSafeSigners(context.Background(), nil, safeEvent("SAFE_OWNERS_CHANGED")))

	require.Empty(t, r.calls)
	require.Empty(t, reader.reads)
}
