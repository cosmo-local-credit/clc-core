package worker

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/safe"
	"github.com/ethereum/go-ethereum/common"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"
)

const (
	executeHash = "0xabababababababababababababababababababababababababababababababab"
	executeSafe = "0x1111111111111111111111111111111111111111"
)

type (
	fakePreparer struct {
		exec  *safe.Execution
		err   error
		calls int
	}
)

func (f *fakePreparer) Prepare(context.Context, string) (*safe.Execution, error) {
	f.calls++
	return f.exec, f.err
}

func executeJob() *river.Job[SafeExecuteArgs] {
	return &river.Job[SafeExecuteArgs]{
		JobRow: &rivertype.JobRow{},
		Args:   SafeExecuteArgs{SafeTxHash: executeHash, Safe: executeSafe},
	}
}

func TestSafeExecuteWorkerOutcomes(t *testing.T) {
	ready := &safe.Execution{Safe: common.HexToAddress(executeSafe), Calldata: []byte{0x6a, 0x76, 0x12, 0x02}}
	for name, tc := range map[string]struct {
		prepare  *fakePreparer
		alreadyS bool
		check    func(t *testing.T, err error, sent []string)
	}{
		"sends a ready execution under its stable tracking ID": {
			prepare: &fakePreparer{exec: ready},
			check: func(t *testing.T, err error, sent []string) {
				require.NoError(t, err)
				require.Equal(t, []string{safeExecutionTrackingID(executeHash)}, sent)
			},
		},
		"never sends twice for one Safe transaction": {
			prepare: &fakePreparer{exec: ready}, alreadyS: true,
			check: func(t *testing.T, err error, sent []string) {
				require.NoError(t, err)
				require.Empty(t, sent)
			},
		},
		"executed or replaced elsewhere is done": {
			prepare: &fakePreparer{err: safe.ErrSettled},
			check: func(t *testing.T, err error, sent []string) {
				require.NoError(t, err)
				require.Empty(t, sent)
			},
		},
		"signatures the backend cannot use stay with the Safe app": {
			prepare: &fakePreparer{err: fmt.Errorf("%w: 1 of 2", safe.ErrNotReady)},
			check: func(t *testing.T, err error, sent []string) {
				require.NoError(t, err)
				require.Empty(t, sent)
			},
		},
		"waits for an earlier nonce": {
			prepare: &fakePreparer{err: safe.ErrNotNext},
			check: func(t *testing.T, err error, sent []string) {
				var snooze *river.JobSnoozeError
				require.ErrorAs(t, err, &snooze)
				require.Empty(t, sent)
			},
		},
		"policy refusal is final": {
			prepare: &fakePreparer{err: fmt.Errorf("%w: not an asset", safe.ErrRejected)},
			check: func(t *testing.T, err error, sent []string) {
				var cancel *river.JobCancelError
				require.ErrorAs(t, err, &cancel)
				require.Empty(t, sent)
			},
		},
		"a transport failure is retried": {
			prepare: &fakePreparer{err: errors.New("sts unavailable")},
			check: func(t *testing.T, err error, sent []string) {
				require.EqualError(t, err, "sts unavailable")
				require.Empty(t, sent)
			},
		},
		"refuses an execution for another Safe": {
			prepare: &fakePreparer{exec: &safe.Execution{Safe: common.HexToAddress("0x2222222222222222222222222222222222222222")}},
			check: func(t *testing.T, err error, sent []string) {
				var cancel *river.JobCancelError
				require.ErrorAs(t, err, &cancel)
				require.Empty(t, sent)
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			var sent []string
			w := &SafeExecuteWorker{
				executor: tc.prepare,
				sent:     func(context.Context, string) (bool, error) { return tc.alreadyS, nil },
				send: func(_ context.Context, trackingID string, _ *safe.Execution) error {
					sent = append(sent, trackingID)
					return nil
				},
			}
			tc.check(t, w.Work(context.Background(), executeJob()), sent)
			if tc.alreadyS {
				require.Zero(t, tc.prepare.calls, "a sent execution is not prepared again")
			}
		})
	}
}

func TestSafeExecutionTrackingIDIsStablePerHash(t *testing.T) {
	require.Equal(t, safeExecutionTrackingID(executeHash), safeExecutionTrackingID("0xABABABABABABABABABABABABABABABABABABABABABABABABABABABABABABABAB"))
	require.NotEqual(t, safeExecutionTrackingID(executeHash), safeExecutionTrackingID("0x"+executeHash[4:]+"cd"))
}
