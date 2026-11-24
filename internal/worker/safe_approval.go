package worker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/safe"
	"github.com/cosmo-local-credit/clc-core/pkg/event"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

const (
	safeApprovalAudienceChunk = 16
)

var (
	safeApprovalEventNamespace = uuid.MustParse("48b74931-52dc-4c55-a884-1b14e68893c1")
	addVoucherSelector         = crypto.Keccak256([]byte("add(address)"))[:4]
)

type (
	safeApprovalSTS interface {
		Transaction(context.Context, string) (*safe.Transaction, error)
		Queued(context.Context, string, string) ([]safe.Transaction, error)
	}

	SafeApprovalSyncArgs struct {
		SafeAddress string `json:"safeAddress"`
		SafeTxHash  string `json:"safeTxHash,omitempty"`
		Signer      string `json:"signer,omitempty"`
	}

	// SafeApprovalReconcileArgs identifies scheduled jobs from older releases
	// so the new worker can drain them.
	SafeApprovalReconcileArgs struct{}

	SafeApprovalSyncWorker struct {
		river.WorkerDefaults[SafeApprovalSyncArgs]

		wc     *WorkerContainer
		sts    safeApprovalSTS
		policy *safe.Policy
	}

	SafeApprovalReconcileWorker struct {
		river.WorkerDefaults[SafeApprovalReconcileArgs]
	}

	workerSafeResolver struct{ wc *WorkerContainer }
)

func (SafeApprovalSyncArgs) Kind() string      { return "SAFE_APPROVAL_SYNC" }
func (SafeApprovalReconcileArgs) Kind() string { return "SAFE_APPROVAL_RECONCILE" }

func safeApprovalSyncInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
		rivertype.JobStateRetryable, rivertype.JobStateScheduled,
	}}}
}

func EnqueueSafeApprovalSync(ctx context.Context, queue *river.Client[pgx.Tx], safeAddress, safeTxHash, signer string) error {
	if queue == nil {
		return errors.New("safe approval sync: no queue client")
	}
	if !common.IsHexAddress(safeAddress) {
		return errors.New("safe approval sync: invalid Safe address")
	}
	if len(safeTxHash) != 66 {
		return errors.New("safe approval sync: invalid transaction hash")
	}
	if !common.IsHexAddress(signer) {
		return errors.New("safe approval sync: invalid signer")
	}
	// River deduplicates by args. Key on the signer too so a local confirmation
	// can schedule a fresh read even if the proposal's earlier sync is running.
	_, err := queue.Insert(ctx, SafeApprovalSyncArgs{
		SafeAddress: common.HexToAddress(safeAddress).Hex(),
		SafeTxHash:  strings.ToLower(safeTxHash),
		Signer:      strings.ToLower(signer),
	}, safeApprovalSyncInsertOpts())
	return err
}

func (r workerSafeResolver) IsSafe(ctx context.Context, address common.Address) (bool, error) {
	tx, err := r.wc.store.Pool().Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	_, err = r.wc.store.GetSafeState(ctx, tx, r.wc.chainID, address.Hex())
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (r workerSafeResolver) ManagesAsset(ctx context.Context, safeAddress, target common.Address) (bool, error) {
	tx, err := r.wc.store.Pool().Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	return r.wc.store.SafeManagesAsset(ctx, tx, r.wc.chainID, safeAddress.Hex(), target.Hex())
}

func (*SafeApprovalReconcileWorker) Work(context.Context, *river.Job[SafeApprovalReconcileArgs]) error {
	return nil
}

func (w *SafeApprovalSyncWorker) Work(ctx context.Context, job *river.Job[SafeApprovalSyncArgs]) error {
	if job.Args.SafeTxHash == "" {
		// Older reconcilers queued hashless jobs to scan a Safe's whole queue.
		return nil
	}
	tx, err := w.sts.Transaction(ctx, job.Args.SafeTxHash)
	if err != nil {
		return snoozeSafeQuota(err)
	}
	if job.Args.Signer != "" && (tx == nil || !slices.ContainsFunc(tx.Confirmations, func(confirmation safe.Confirmation) bool {
		return strings.EqualFold(confirmation.Owner, job.Args.Signer)
	})) {
		return errors.New("safe approval sync: accepted signature not visible yet")
	}
	return snoozeSafeQuota(w.syncTransaction(ctx, tx))
}

func snoozeSafeQuota(err error) error {
	if limit, ok := safe.QuotaFailure(err); ok && limit.Monthly {
		return river.JobSnooze(max(time.Second, time.Until(limit.RetryAt)))
	}
	return err
}

func (w *SafeApprovalSyncWorker) syncTransaction(ctx context.Context, safeTx *safe.Transaction) error {
	if safeTx == nil || !common.IsHexAddress(safeTx.Safe) || safeTx.SafeTxHash == "" || safeTx.ConfirmationsRequired < 1 {
		return nil
	}
	exec, err := safeTx.Exec()
	if err != nil {
		return nil
	}
	if err := w.policy.CheckTransaction(ctx, common.HexToAddress(safeTx.Safe), exec); err != nil {
		if errors.Is(err, safe.ErrRejected) {
			return nil
		}
		return err
	}
	safeAddress := common.HexToAddress(safeTx.Safe)
	isRejection := safe.IsRejection(safeAddress, safeTx.TxFields)
	relatedHashes := []string(nil)
	relatedExecs := []*safe.Exec(nil)
	if isRejection {
		queued, qErr := w.sts.Queued(ctx, safeTx.Safe, safeTx.Nonce)
		if qErr != nil {
			return qErr
		}
		for i := range queued {
			if queued[i].Nonce != safeTx.Nonce || safe.IsRejection(safeAddress, queued[i].TxFields) {
				continue
			}
			candidate, decodeErr := queued[i].Exec()
			if decodeErr != nil || w.policy.CheckTransaction(ctx, safeAddress, candidate) != nil {
				continue
			}
			relatedHashes = append(relatedHashes, strings.ToLower(queued[i].SafeTxHash))
			relatedExecs = append(relatedExecs, candidate)
		}
	}
	targets, allOwned := safeApprovalTargets(safeAddress, exec, w.policy)
	if isRejection {
		targets, allOwned = nil, false
		for _, related := range relatedExecs {
			more, ownsAll := safeApprovalTargets(safeAddress, related, w.policy)
			targets = append(targets, more...)
			allOwned = allOwned || ownsAll
		}
		targets = deduplicateStrings(targets)
	}
	notificationAction := safeApprovalAction(exec, safeAddress, w.policy)
	if isRejection && len(relatedExecs) > 0 {
		// Describe the action being rejected, while TransactionKind keeps the
		// notification itself unambiguously identified as a rejection.
		notificationAction = safeApprovalAction(relatedExecs[0], safeAddress, w.policy)
	}
	confirmations := append([]safe.Confirmation(nil), safeTx.Confirmations...)
	sort.SliceStable(confirmations, func(i, j int) bool {
		if confirmations[i].SubmittedAt == confirmations[j].SubmittedAt {
			return strings.ToLower(confirmations[i].Owner) < strings.ToLower(confirmations[j].Owner)
		}
		return confirmations[i].SubmittedAt < confirmations[j].SubmittedAt
	})

	tx, err := w.wc.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if w.wc.safeExecutor != nil && !safeTx.IsExecuted && safeTx.ConfirmationsRequired > 0 &&
		len(confirmations) >= safeTx.ConfirmationsRequired {
		if err := EnqueueSafeExecutionTx(ctx, w.wc.queueClient, tx, safeTx.Safe, safeTx.SafeTxHash); err != nil {
			return err
		}
	}
	audience, err := w.wc.store.ListOwnerMessageTargets(ctx, tx, w.wc.chainID, safeTx.Safe)
	if err != nil {
		return err
	}
	resources, err := w.wc.store.ListSafeApprovalResources(ctx, tx, w.wc.chainID, safeTx.Safe, targets, allOwned)
	if err != nil {
		return err
	}
	if len(audience) == 0 || len(resources) == 0 {
		w.wc.logg.Warn("safe approval notification skipped", "safe", safeTx.Safe, "safeTxHash", safeTx.SafeTxHash,
			"audience", len(audience), "resources", len(resources), "rejection", isRejection)
		return tx.Commit(ctx)
	}
	for _, candidateExec := range append([]*safe.Exec{exec}, relatedExecs...) {
		candidate := safeApprovalVoucherCandidate(candidateExec, w.policy)
		if candidate == "" {
			continue
		}
		voucher, lookupErr := w.wc.store.MessagingVoucherByAddress(ctx, tx, w.wc.chainID, candidate)
		if lookupErr == nil {
			for i := range resources {
				if resources[i].Kind == "pool" {
					resources[i].VoucherAddress = voucher.Address
					resources[i].VoucherName = voucher.Name
				}
			}
		}
		break
	}

	for index, confirmation := range confirmations {
		if !common.IsHexAddress(confirmation.Owner) {
			continue
		}
		occurredAt, err := time.Parse(time.RFC3339Nano, confirmation.SubmittedAt)
		if err != nil {
			continue
		}
		baseID := uuid.NewSHA1(safeApprovalEventNamespace, fmt.Appendf(nil, "%d:%s:%s", w.wc.chainID, strings.ToLower(safeTx.SafeTxHash), strings.ToLower(confirmation.Owner)))
		emit, err := w.wc.store.RecordSafeApprovalMilestone(ctx, tx, w.wc.chainID, safeTx.SafeTxHash, confirmation.Owner, baseID.String(), occurredAt)
		if err != nil {
			return err
		}
		if !emit {
			continue
		}
		kind, proposed := safeApprovalStage(index, safeTx.ConfirmationsRequired, isRejection)
		actorID := int64(0)
		for _, member := range audience {
			if strings.EqualFold(member.Address, confirmation.Owner) {
				actorID = member.UserID
				break
			}
		}
		for start := 0; start < len(audience); start += safeApprovalAudienceChunk {
			end := min(start+safeApprovalAudienceChunk, len(audience))
			chunk := make([]event.MessagingAudience, 0, end-start)
			for _, member := range audience[start:end] {
				chunk = append(chunk, event.MessagingAudience{UserID: member.UserID, Address: common.HexToAddress(member.Address).Hex(), Push: true})
			}
			eventID := uuid.NewSHA1(baseID, fmt.Appendf(nil, "audience:%d", start)).String()
			ev := event.MessagingEvent{
				EventID:    eventID,
				Kind:       kind,
				OccurredAt: occurredAt.UTC().Format(time.RFC3339Nano),
				Actor:      event.MessagingActor{UserID: actorID, Address: common.HexToAddress(confirmation.Owner).Hex()},
				Audience:   chunk,
				SafeApproval: &event.MessagingSafeApproval{
					SafeAddress: common.HexToAddress(safeTx.Safe).Hex(), SafeTxHash: strings.ToLower(safeTx.SafeTxHash),
					Stage: kind, Approvals: index + 1, ApprovalsRequired: safeTx.ConfirmationsRequired, Proposed: proposed,
					TransactionKind: map[bool]string{true: "rejection", false: "action"}[isRejection],
					Action:          notificationAction, RejectsSafeTxHashes: relatedHashes,
				},
			}
			for _, resource := range resources {
				ev.Resources = append(ev.Resources, event.MessagingResource{Kind: resource.Kind, Address: common.HexToAddress(resource.Address).Hex(), Name: resource.Name,
					VoucherAddress: resource.VoucherAddress, VoucherName: resource.VoucherName})
			}
			if err := EnqueueMessagingEvent(ctx, w.wc.queueClient, tx, ev); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

func safeApprovalStage(index, required int, rejection bool) (string, bool) {
	if rejection {
		if index+1 == required {
			return event.SafeRejectionApproved, index == 0
		}
		if index == 0 {
			return event.SafeRejectionProposed, true
		}
		return event.SafeRejectionConfirmed, false
	}
	if index+1 == required {
		return event.SafeApproved, index == 0
	}
	if index == 0 {
		return event.SafeProposed, true
	}
	return event.SafeConfirmed, false
}

func deduplicateStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(value)
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func safeApprovalVoucherCandidate(exec *safe.Exec, policy *safe.Policy) string {
	calls := []safe.SubCall{{Operation: exec.Operation, To: exec.To, Value: exec.Value, Data: exec.Data}}
	if exec.Operation == safe.OperationDelegateCall {
		if _, ok := policy.MultiSend[exec.To]; ok {
			if decoded, err := safe.DecodeMultiSend(exec.Data); err == nil {
				calls = decoded
			}
		}
	}
	for _, call := range calls {
		if len(call.Data) != 36 || string(call.Data[:4]) != string(addVoucherSelector) {
			continue
		}
		return common.BytesToAddress(call.Data[16:36]).Hex()
	}
	return ""
}

func safeApprovalAction(exec *safe.Exec, safeAddress common.Address, policy *safe.Policy) string {
	if exec.To == safeAddress && exec.Operation == safe.OperationCall && exec.Value.Sign() == 0 && len(exec.Data) == 0 {
		return "reject"
	}
	if safeApprovalVoucherCandidate(exec, policy) != "" {
		return "approve_voucher"
	}
	return "manage_resource"
}

func safeApprovalTargets(safeAddress common.Address, exec *safe.Exec, policy *safe.Policy) ([]string, bool) {
	calls := []safe.SubCall{{Operation: exec.Operation, To: exec.To, Value: exec.Value, Data: exec.Data}}
	if exec.Operation == safe.OperationDelegateCall {
		if _, ok := policy.MultiSend[exec.To]; ok {
			if decoded, err := safe.DecodeMultiSend(exec.Data); err == nil {
				calls = decoded
			}
		}
	}
	seen := map[string]struct{}{}
	targets := make([]string, 0, len(calls))
	allOwned := false
	for _, call := range calls {
		if call.To == safeAddress {
			allOwned = true
			continue
		}
		address := strings.ToLower(call.To.Hex())
		if _, ok := seen[address]; ok {
			continue
		}
		seen[address] = struct{}{}
		targets = append(targets, address)
	}
	return targets, allOwned
}
