package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/safe"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/grassrootseconomics/ethutils"
	"github.com/jackc/pgx/v5"
	"github.com/lmittmann/w3/module/eth"
	"github.com/lmittmann/w3/w3types"
	"github.com/riverqueue/river"
)

const (
	// safeExecuteRetry spaces the check for a transaction queued behind an
	// earlier nonce.
	safeExecuteRetry = 30 * time.Second
	// maxSafeExecuteGas caps a single execution; a MultiSend of the largest
	// owner batch with threshold signatures stays well below it.
	maxSafeExecuteGas uint64 = 3_000_000
)

var (
	// safeExecutionNamespace derives one tracking ID per Safe transaction, so
	// a retried job finds the execution it already sent instead of sending
	// again.
	safeExecutionNamespace = uuid.MustParse("4f0c4b52-3c0e-4c64-9a4d-5afe0e7ec0de")
)

type (
	SafeExecuteArgs struct {
		SafeTxHash string `json:"safeTxHash" river:"unique"`
		Safe       string `json:"safe"`
	}

	safePreparer interface {
		Prepare(ctx context.Context, safeTxHash string) (*safe.Execution, error)
	}

	// SafeExecuteWorker submits a confirmed Safe transaction from the system
	// signer. The checks live in safe.Executor; this decides what each outcome
	// means for the job and sends the call.
	SafeExecuteWorker struct {
		river.WorkerDefaults[SafeExecuteArgs]

		wc       *WorkerContainer
		executor safePreparer
		// sent and send default to the OTX store and the system signer.
		sent func(ctx context.Context, trackingID string) (bool, error)
		send func(ctx context.Context, trackingID string, exec *safe.Execution) error
	}

	// safeResolver answers the policy's questions from the signer projection
	// and the catalog, as the API's does.
	safeResolver struct {
		store   store.Store
		chainID int64
	}
)

func (SafeExecuteArgs) Kind() string { return "SAFE_EXECUTE" }

// NewSafeResolver backs a safe.Policy with the catalog and signer projection.
func NewSafeResolver(s store.Store, chainID int64) safe.Resolver {
	return safeResolver{store: s, chainID: chainID}
}

func (r safeResolver) IsSafe(ctx context.Context, address common.Address) (bool, error) {
	tx, err := r.store.Pool().Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	_, err = r.store.GetSafeState(ctx, tx, r.chainID, address.Hex())
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (r safeResolver) ManagesAsset(ctx context.Context, safeAddress, target common.Address) (bool, error) {
	tx, err := r.store.Pool().Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	return r.store.SafeManagesAsset(ctx, tx, r.chainID, safeAddress.Hex(), target.Hex())
}

// EnqueueSafeExecution queues one execution per Safe transaction; repeated
// triggers for the same hash coalesce.
func EnqueueSafeExecution(ctx context.Context, queue *river.Client[pgx.Tx], safeAddress, safeTxHash string) error {
	if queue == nil {
		return errors.New("safe execution: worker queue not configured")
	}
	_, err := queue.Insert(ctx, SafeExecuteArgs{
		SafeTxHash: strings.ToLower(safeTxHash), Safe: strings.ToLower(safeAddress),
	}, &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true}})
	return err
}

func safeExecutionTrackingID(safeTxHash string) string {
	return uuid.NewSHA1(safeExecutionNamespace, []byte(strings.ToLower(safeTxHash))).String()
}

func (w *SafeExecuteWorker) Work(ctx context.Context, job *river.Job[SafeExecuteArgs]) error {
	sent, send := w.sent, w.send
	if sent == nil {
		sent = w.alreadySent
	}
	if send == nil {
		send = w.sendFromSystemSigner
	}
	trackingID := safeExecutionTrackingID(job.Args.SafeTxHash)
	done, err := sent(ctx, trackingID)
	if err != nil || done {
		return err
	}

	exec, err := w.executor.Prepare(ctx, job.Args.SafeTxHash)
	switch {
	case errors.Is(err, safe.ErrSettled), errors.Is(err, safe.ErrNotReady):
		// Executed elsewhere, replaced, or confirmed with signatures the
		// backend cannot use: the Safe app remains the place to execute.
		return nil
	case errors.Is(err, safe.ErrNotNext):
		return river.JobSnooze(safeExecuteRetry)
	case errors.Is(err, safe.ErrRejected):
		return river.JobCancel(err)
	case err != nil:
		return err
	}
	if !strings.EqualFold(exec.Safe.Hex(), job.Args.Safe) {
		return river.JobCancel(fmt.Errorf("safe execution: transaction belongs to %s, not %s", exec.Safe.Hex(), job.Args.Safe))
	}
	return send(ctx, trackingID, exec)
}

func (w *SafeExecuteWorker) alreadySent(ctx context.Context, trackingID string) (bool, error) {
	tx, err := w.wc.store.Pool().Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	otx, err := w.wc.store.GetOTXByTrackingID(ctx, tx, trackingID)
	return len(otx) > 0, err
}

// sendFromSystemSigner simulates the call first, since a revert would only
// burn the system signer's gas, then signs, records and dispatches it like
// any other tracked transaction.
func (w *SafeExecuteWorker) sendFromSystemSigner(ctx context.Context, trackingID string, exec *safe.Execution) error {
	tx, err := w.wc.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	systemKeypair, err := w.wc.store.LoadMasterSignerKey(ctx, tx)
	if err != nil {
		return err
	}
	privateKey, err := crypto.HexToECDSA(systemKeypair.Private)
	if err != nil {
		return err
	}

	from := common.HexToAddress(systemKeypair.Public)
	var estimate uint64
	if err := w.wc.chainProvider.Client.CallCtx(ctx, eth.EstimateGas(&w3types.Message{
		From: from, To: &exec.Safe, Input: exec.Calldata,
	}, nil).Returns(&estimate)); err != nil {
		if safe.UnsupportedCall(err) {
			return river.JobCancel(fmt.Errorf("safe execution would revert: %w", err))
		}
		return err
	}
	gasLimit := min(estimate+estimate/4, maxSafeExecuteGas)

	nonce, err := w.wc.store.AcquireNonce(ctx, tx, systemKeypair.Public)
	if err != nil {
		return err
	}
	gasSettings, err := w.wc.gasOracle.GetSettings()
	if err != nil {
		return err
	}
	builtTx, err := w.wc.chainProvider.SignContractExecutionTx(privateKey, ethutils.ContractExecutionTxOpts{
		ContractAddress: exec.Safe,
		InputData:       exec.Calldata,
		GasFeeCap:       gasSettings.GasFeeCap,
		GasTipCap:       gasSettings.GasTipCap,
		GasLimit:        gasLimit,
		Nonce:           nonce,
	})
	if err != nil {
		return err
	}
	prepared, err := w.wc.insertTrackedTx(ctx, tx, trackingID, store.SAFE_EXECUTE, systemKeypair.Public, nonce, builtTx)
	if err != nil {
		return err
	}
	if _, err := w.wc.queueClient.InsertTx(ctx, tx, DispatchArgs{
		TrackingID: trackingID,
		OTXID:      prepared.OTXID,
		RawTx:      prepared.RawHex,
		TxHash:     prepared.Hash,
	}, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
