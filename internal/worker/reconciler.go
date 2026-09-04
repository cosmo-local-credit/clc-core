package worker

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/pkg/event"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/jackc/pgx/v5"
	"github.com/lmittmann/w3/module/eth"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

type (
	// NonceReconcilerArgs reconciles one signer's nonce sequence against the
	// chain. Signer is optional: empty means the relayer's master signer.
	NonceReconcilerArgs struct {
		Signer string `json:"signer,omitempty"`
	}

	NonceReconcilerWorker struct {
		river.WorkerDefaults[NonceReconcilerArgs]

		wc *WorkerContainer
	}
)

const (
	NonceReconcilerID = "NONCE_RECONCILER"

	// reconcileInterval is the periodic sweep. A stuck head is detected within
	// one interval rather than waiting on a receipt poller's backoff.
	reconcileInterval = 60 * time.Second

	// reconcileBatch bounds one pass. The advisory lock is held for the whole
	// walk, so an unbounded backlog would stall the dispatcher; the remainder
	// is picked up on the next sweep.
	reconcileBatch = 50

	// stuckThreshold is how long the head of a signer's queue may sit
	// unsettled before the sweep says so at WARN. This is the signal that was
	// missing on 2026-09-04, where the gap opened at 08:10 and was found by
	// hand at 09:16.
	stuckThreshold = 5 * time.Minute
)

// reconcileAction is what the reconciler decides to do with one transaction.
type reconcileAction int

const (
	// actionSettle — a receipt exists; record SUCCESS or REVERTED.
	actionSettle reconcileAction = iota
	// actionSupersede — the chain moved past this nonce without this
	// transaction, so something else consumed it.
	actionSupersede
	// actionRepublish — this is the head the chain is waiting for.
	actionRepublish
	// actionWait — correctly queued behind the head; nothing to do.
	actionWait
)

// decideReconcileAction is the reconciler's decision, separated from its IO so
// the ordering rules can be tested directly.
//
// Only the head is republished: pushing transactions in behind a head that is
// already queued is what reth's gapped-nonce and reservation rules reject, and
// it cannot help — the pool will not mine any of them until the head lands.
func decideReconcileAction(nonce, chainNonce uint64, hasReceipt, headTaken bool) reconcileAction {
	switch {
	case hasReceipt:
		return actionSettle
	case nonce < chainNonce:
		return actionSupersede
	case headTaken:
		return actionWait
	default:
		return actionRepublish
	}
}

func (NonceReconcilerArgs) Kind() string { return NonceReconcilerID }

// InsertOpts collapses concurrent reconcile requests for the same signer: the
// dispatcher, a dying receipt poller and the periodic sweep all enqueue this,
// and one pass per signer is enough.
func (NonceReconcilerArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: 3,
		UniqueOpts: river.UniqueOpts{
			ByArgs: true,
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable,
				rivertype.JobStateRunning,
				rivertype.JobStateRetryable,
				rivertype.JobStateScheduled,
			},
		},
	}
}

// Work walks the signer's unsettled transactions in nonce order and gives each
// one an outcome.
//
// Receipts are checked before anything is republished: a transaction that
// landed while its poller was backing off is finalised here, which is the half
// of the 2026-09-04 incident that left rows at IN_NETWORK long after they had
// succeeded.
//
// Then the lowest nonce the chain has not consumed is republished from its
// stored bytes. "already known" means the pool has it and the walk stops —
// there is no point pushing transactions in behind a head that is already
// queued.
func (w *NonceReconcilerWorker) Work(ctx context.Context, job *river.Job[NonceReconcilerArgs]) error {
	tx, err := w.wc.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	signer := job.Args.Signer
	if signer == "" {
		masterKey, err := w.wc.store.LoadMasterSignerKey(ctx, tx)
		if err != nil {
			return fmt.Errorf("reconciler: load master signer: %w", err)
		}
		signer = masterKey.Public
	}

	// Serialise against the dispatcher and any concurrent reconcile. Held for
	// the life of this transaction.
	if err := w.wc.store.LockSigner(ctx, tx, signer); err != nil {
		return fmt.Errorf("reconciler: lock signer: %w", err)
	}

	unsettled, err := w.wc.store.GetUnsettledOTXBySigner(ctx, tx, signer)
	if err != nil {
		return fmt.Errorf("reconciler: load unsettled otx: %w", err)
	}
	if len(unsettled) == 0 {
		return tx.Commit(ctx)
	}

	signerAddr := common.HexToAddress(signer)

	var chainNonce uint64
	if err := w.wc.chainProvider.Client.CallCtx(ctx,
		eth.Nonce(signerAddr, nil).Returns(&chainNonce),
	); err != nil {
		return fmt.Errorf("reconciler: read chain nonce: %w", err)
	}

	head := unsettled[0]
	stuckFor := time.Since(head.CreatedAt)

	// The gauge that would have caught this incident at 08:10 instead of
	// 09:16: the head of the queue is the nonce the chain wants next and it
	// has not moved.
	if head.Nonce == chainNonce && stuckFor > stuckThreshold {
		w.wc.logg.Warn("relayer nonce sequence is stuck",
			"signer", signer,
			"chainNonce", chainNonce,
			"headNonce", head.Nonce,
			"headTxHash", head.TxHash,
			"headStatus", head.DispatchStatus,
			"stuckFor", stuckFor.Round(time.Second).String(),
			"unsettled", len(unsettled),
		)
	} else {
		w.wc.logg.Debug("reconciling signer",
			"signer", signer,
			"chainNonce", chainNonce,
			"headNonce", head.Nonce,
			"unsettled", len(unsettled),
		)
	}

	if len(unsettled) > reconcileBatch {
		unsettled = unsettled[:reconcileBatch]
	}

	headTaken := false

	for _, otx := range unsettled {
		receipt, err := w.receiptFor(ctx, otx.TxHash)
		if err != nil {
			return err
		}

		switch decideReconcileAction(otx.Nonce, chainNonce, receipt != nil, headTaken) {
		case actionSettle:
			if err := w.settle(ctx, tx, otx, receipt); err != nil {
				return err
			}

		case actionSupersede:
			if err := w.wc.store.SetOTXReplaced(ctx, tx, otx.ID); err != nil {
				return err
			}
			if err := w.setStatus(ctx, tx, otx, store.LOW_NONCE); err != nil {
				return err
			}
			w.wc.logg.Warn("otx superseded by another transaction at the same nonce",
				"signer", signer, "nonce", otx.Nonce, "txHash", otx.TxHash, "chainNonce", chainNonce)

		case actionRepublish:
			headTaken = true
			if err := w.republish(ctx, tx, signer, otx); err != nil {
				return err
			}

		case actionWait:
		}
	}

	return tx.Commit(ctx)
}

// receiptFor returns the receipt for txHash, or nil when the transaction is
// not mined. A failed lookup is indistinguishable from "not mined" over an
// RPC that may be briefly unavailable, so both yield nil.
func (w *NonceReconcilerWorker) receiptFor(ctx context.Context, txHash string) (*types.Receipt, error) {
	var receipt *types.Receipt
	if err := w.wc.chainProvider.Client.CallCtx(ctx,
		eth.TxReceipt(common.HexToHash(txHash)).Returns(&receipt),
	); err != nil {
		return nil, nil
	}
	return receipt, nil
}

// settle records the outcome of a transaction that already has a receipt.
func (w *NonceReconcilerWorker) settle(ctx context.Context, tx pgx.Tx, otx *store.OTX, receipt *types.Receipt) error {
	status := store.SUCCESS
	if receipt.Status == 0 {
		status = store.REVERTED
	}

	if err := w.setStatus(ctx, tx, otx, status); err != nil {
		return err
	}

	w.wc.logg.Info("reconciler settled a mined transaction",
		"txHash", otx.TxHash,
		"nonce", otx.Nonce,
		"status", status,
		"blockNumber", receipt.BlockNumber.Uint64(),
		"previousStatus", otx.DispatchStatus,
	)

	return nil
}

// republish resends the head transaction's stored bytes and acts on the
// verdict. A rebuild verdict re-signs the same nonce at a higher fee.
func (w *NonceReconcilerWorker) republish(ctx context.Context, tx pgx.Tx, signer string, otx *store.OTX) error {
	rawTx, err := hexutil.Decode(otx.RawTx)
	if err != nil {
		return fmt.Errorf("reconciler: decode raw tx %s: %w", otx.TxHash, err)
	}

	sendErr := w.wc.sendRawTx(ctx, rawTx)
	if sendErr == nil {
		w.wc.logg.Info("reconciler republished a stuck transaction",
			"txHash", otx.TxHash, "nonce", otx.Nonce, "otxType", otx.OTXType)
		return w.setStatus(ctx, tx, otx, store.IN_NETWORK)
	}

	var dispatchErr *DispatchError
	if !errors.As(sendErr, &dispatchErr) {
		return sendErr
	}

	w.wc.logg.Warn("reconciler republish rejected",
		"txHash", otx.TxHash,
		"nonce", otx.Nonce,
		"otxType", otx.OTXType,
		"verdict", dispatchErr.Verdict,
		"error", dispatchErr.Err,
		"rpcError", dispatchErr.OriginalErr,
	)

	if dispatchErr.Verdict == VerdictRebuild {
		return w.rebuild(ctx, tx, signer, otx)
	}

	return w.setStatus(ctx, tx, otx, dispatchStatus(dispatchErr.Err))
}

// rebuild re-signs otx's transaction at the same nonce with a higher fee,
// records the replacement, and publishes it.
func (w *NonceReconcilerWorker) rebuild(ctx context.Context, tx pgx.Tx, signer string, otx *store.OTX) error {
	masterKey, err := w.wc.store.LoadMasterSignerKey(ctx, tx)
	if err != nil {
		return err
	}
	if !strings.EqualFold(masterKey.Public, signer) {
		// Only the master signer's key is available to this process; a bump
		// for any other signer needs an operator.
		return w.setStatus(ctx, tx, otx, store.LOW_GAS_PRICE)
	}

	privateKey, err := crypto.HexToECDSA(masterKey.Private)
	if err != nil {
		return err
	}

	rawTx, err := hexutil.Decode(otx.RawTx)
	if err != nil {
		return err
	}

	prev := new(types.Transaction)
	if err := prev.UnmarshalBinary(rawTx); err != nil {
		return fmt.Errorf("reconciler: decode stored tx %s: %w", otx.TxHash, err)
	}

	var targetFeeCap, targetTipCap *big.Int
	if settings, err := w.wc.gasOracle.GetSettings(); err == nil && settings != nil {
		targetFeeCap, targetTipCap = settings.GasFeeCap, settings.GasTipCap
	}

	bumped, err := rebuildAtHigherFee(
		types.LatestSignerForChainID(prev.ChainId()),
		prev,
		targetFeeCap,
		targetTipCap,
		privateKey,
	)
	if err != nil {
		return err
	}

	prepared, err := w.wc.insertTrackedTx(ctx, tx, otx.TrackingID, otx.OTXType, masterKey.Public, otx.Nonce, bumped)
	if err != nil {
		return err
	}

	if err := w.wc.store.SetOTXReplaced(ctx, tx, otx.ID); err != nil {
		return err
	}
	if err := w.setStatus(ctx, tx, otx, store.REPLACEMENT_UNDERPRICED); err != nil {
		return err
	}

	w.wc.logg.Info("reconciler bumped a stuck transaction",
		"nonce", otx.Nonce,
		"otxType", otx.OTXType,
		"oldTxHash", otx.TxHash,
		"newTxHash", prepared.Hash,
		"oldFeeCap", prev.GasFeeCap(),
		"newFeeCap", bumped.GasFeeCap(),
		"oldTipCap", prev.GasTipCap(),
		"newTipCap", bumped.GasTipCap(),
	)

	if _, err := w.wc.queueClient.InsertTx(ctx, tx, DispatchArgs{
		TrackingID: otx.TrackingID,
		OTXID:      prepared.OTXID,
		RawTx:      prepared.RawHex,
		TxHash:     prepared.Hash,
	}, nil); err != nil {
		return err
	}

	return nil
}

func (w *NonceReconcilerWorker) setStatus(ctx context.Context, tx pgx.Tx, otx *store.OTX, status string) error {
	if otx.DispatchStatus == status {
		return nil
	}

	if err := w.wc.store.UpdateDispatchTxStatus(ctx, tx, store.DispatchTx{
		OTXID:  otx.ID,
		Status: status,
	}); err != nil {
		return err
	}

	if w.wc.pub != nil {
		_ = w.wc.pub.Send(ctx, event.Event{
			TrackingID: otx.TrackingID,
			Status:     status,
		})
	}

	return nil
}
