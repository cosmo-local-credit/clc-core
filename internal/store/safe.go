package store

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
)

type (
	// SafeState is the persisted, event-projected signer set of one multisig.
	SafeState struct {
		Address     string
		Signers     []string
		Threshold   int // zero means the compatible contract has no threshold getter
		Block       uint64
		RefreshedAt time.Time
	}

	// OwnerMessageTarget is the deliberately public identity of an approved CLC
	// account that can receive a message for an owner address. For a Safe these
	// are its current projected signers; for an EOA this is the owner itself.
	OwnerMessageTarget struct {
		UserID   int64   `db:"id"                 json:"-"`
		Address  string  `db:"blockchain_address" json:"address"`
		Username *string `db:"username"           json:"username"`
		Avatar   *string `db:"avatar_url"         json:"avatar"`
	}

	SafeApprovalResource struct {
		Kind           string
		Address        string
		Name           string
		VoucherAddress string
		VoucherName    string
	}

	SafeExecutionProgress struct {
		JobID        int64
		JobState     string
		OTXActive    bool
		OTXSucceeded bool
		OTXFailed    bool
	}
)

// ReplaceSafeSigners installs a Safe's full signer set, but only when the
// position is newer than what is stored: a replayed or out-of-order event can
// never roll the set back. Reports whether the set was written. Inline SQL for
// the same reason as ProjectCatalogOwnership: cmd/reconcile-ownership calls it
// on a bare Pg.
func (pg *Pg) ReplaceSafeSigners(ctx context.Context, tx pgx.Tx, chainID int64, safe string, signers []string, threshold int, block, index uint64) (bool, error) {
	if chainID <= 0 || !common.IsHexAddress(safe) || threshold < 0 || len(signers) == 0 || len(signers) < threshold ||
		block > math.MaxInt64 || index > math.MaxInt64 {
		return false, fmt.Errorf("invalid safe signer set")
	}
	lowered := make([]string, 0, len(signers))
	seen := make(map[string]struct{}, len(signers))
	for _, s := range signers {
		if !common.IsHexAddress(s) {
			return false, fmt.Errorf("invalid safe signer address")
		}
		s = strings.ToLower(s)
		if _, dup := seen[s]; dup {
			return false, fmt.Errorf("duplicate safe signer")
		}
		seen[s] = struct{}{}
		lowered = append(lowered, s)
	}
	safe = strings.ToLower(safe)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		fmt.Sprintf("safe:%d:%s", chainID, safe)); err != nil {
		return false, err
	}
	var advanced bool
	err := tx.QueryRow(ctx, `
WITH advanced AS (
    INSERT INTO safe_state (chain_id, safe_address, threshold, block_number, log_index)
    VALUES ($1, $2, NULLIF($3, 0), $4, $5)
    ON CONFLICT (chain_id, safe_address) DO UPDATE SET
        threshold = EXCLUDED.threshold,
        block_number = EXCLUDED.block_number,
        log_index = EXCLUDED.log_index,
        refreshed_at = now()
    WHERE (safe_state.block_number, safe_state.log_index)
        < (EXCLUDED.block_number, EXCLUDED.log_index)
    RETURNING 1
), cleared AS (
    DELETE FROM safe_signers
    WHERE chain_id = $1 AND safe_address = $2 AND EXISTS (SELECT 1 FROM advanced)
    RETURNING 1
)
SELECT EXISTS (SELECT 1 FROM advanced)`,
		chainID, safe, threshold, int64(block), int64(index)).Scan(&advanced)
	if err != nil || !advanced {
		return false, err
	}
	_, err = tx.Exec(ctx, `
INSERT INTO safe_signers (chain_id, safe_address, signer)
SELECT $1, $2, unnest($3::text[])`, chainID, safe, lowered)
	return advanced, err
}

// GetSafeState returns the persisted signer set, or pgx.ErrNoRows for an address
// that is not a known Safe.
func (pg *Pg) GetSafeState(ctx context.Context, tx pgx.Tx, chainID int64, safe string) (SafeState, error) {
	state := SafeState{Address: strings.ToLower(safe)}
	var block int64
	err := tx.QueryRow(ctx, `
SELECT COALESCE(threshold, 0), block_number, refreshed_at FROM safe_state
WHERE chain_id = $1 AND safe_address = $2`, chainID, state.Address).
		Scan(&state.Threshold, &block, &state.RefreshedAt)
	if err != nil {
		return SafeState{}, err
	}
	state.Block = uint64(block)
	rows, err := tx.Query(ctx, `
SELECT signer FROM safe_signers WHERE chain_id = $1 AND safe_address = $2 ORDER BY signer`,
		chainID, state.Address)
	if err != nil {
		return SafeState{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var signer string
		if err := rows.Scan(&signer); err != nil {
			return SafeState{}, err
		}
		state.Signers = append(state.Signers, signer)
	}
	return state, rows.Err()
}

// ListSafesSignedBy returns every projected Safe that lists signer, each with
// its full signer set, in address order. It reads the same projection that
// authorizes Safe writes, so a Safe appears here exactly when its signers may
// act for it.
func (pg *Pg) ListSafesSignedBy(ctx context.Context, tx pgx.Tx, chainID int64, signer string) ([]SafeState, error) {
	rows, err := tx.Query(ctx, `
SELECT s.safe_address, COALESCE(s.threshold, 0), s.block_number, s.refreshed_at,
       array_agg(member.signer ORDER BY member.signer)
FROM safe_signers me
JOIN safe_state s ON s.chain_id = me.chain_id AND s.safe_address = me.safe_address
JOIN safe_signers member ON member.chain_id = s.chain_id AND member.safe_address = s.safe_address
WHERE me.chain_id = $1 AND me.signer = lower($2)
GROUP BY s.safe_address, s.threshold, s.block_number, s.refreshed_at
ORDER BY s.safe_address`, chainID, signer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var states []SafeState
	for rows.Next() {
		var (
			state SafeState
			block int64
		)
		if err := rows.Scan(&state.Address, &state.Threshold, &block, &state.RefreshedAt, &state.Signers); err != nil {
			return nil, err
		}
		state.Block = uint64(block)
		states = append(states, state)
	}
	return states, rows.Err()
}

// ListSafeApprovalResources resolves managed transaction targets to their
// public Voucher or Pool. Pool wiring contracts collapse to the parent Pool.
// A Safe self-management call can affect every resource it owns.
func (pg *Pg) ListSafeApprovalResources(ctx context.Context, tx pgx.Tx, chainID int64, safe string, targets []string, includeAllOwned bool) ([]SafeApprovalResource, error) {
	rows, err := tx.Query(ctx, `
WITH target(address) AS (SELECT unnest($3::text[])), resources AS (
    SELECT 'voucher'::text AS kind, lower(v.address) AS address,
           COALESCE(v.display_name, v.name) AS name
    FROM vouchers v
    WHERE v.chain_id = $1 AND lower(v.owner_address) = lower($2)
      AND ($4 OR lower(v.address) IN (SELECT lower(address) FROM target))
    UNION ALL
    SELECT 'pool'::text, lower(p.address), COALESCE(p.display_name, p.name)
    FROM pools p
    WHERE p.chain_id = $1 AND lower(p.owner_address) = lower($2)
      AND ($4 OR EXISTS (
          SELECT 1 FROM target t WHERE lower(t.address) IN (
              lower(p.address), lower(p.token_registry_address), lower(p.limiter_address),
              lower(p.fee_policy_address), lower(p.quoter_address),
              lower(p.protocol_fee_controller_address))))
)
SELECT kind, address, name FROM resources ORDER BY kind, lower(name), address`, chainID, safe, targets, includeAllOwned)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var resources []SafeApprovalResource
	for rows.Next() {
		var resource SafeApprovalResource
		if err := rows.Scan(&resource.Kind, &resource.Address, &resource.Name); err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}
	return resources, rows.Err()
}

// RecordSafeApprovalMilestone atomically claims one signer milestone. Events
// older than the rollout activation time are recorded as seen but not emitted.
func (pg *Pg) RecordSafeApprovalMilestone(ctx context.Context, tx pgx.Tx, chainID int64, safeTxHash, signer, eventID string, occurredAt time.Time) (bool, error) {
	var emit bool
	err := tx.QueryRow(ctx, `
WITH inserted AS (
    INSERT INTO safe_approval_milestones
        (chain_id, safe_tx_hash, signer_address, event_id, occurred_at)
    VALUES ($1, lower($2), lower($3), $4, $5)
    ON CONFLICT (chain_id, safe_tx_hash, signer_address) DO NOTHING
    RETURNING occurred_at
)
SELECT EXISTS (
    SELECT 1 FROM inserted i CROSS JOIN safe_approval_notification_config c
    WHERE c.singleton AND i.occurred_at >= c.activated_at
)`, chainID, safeTxHash, signer, eventID, occurredAt).Scan(&emit)
	return emit, err
}

// ListOwnerMessageTargets returns only public identities that the messaging
// write route itself may resolve. Exact addresses are intentional: this does
// not provide a directory or expose private profile fields.
func (pg *Pg) ListOwnerMessageTargets(ctx context.Context, tx pgx.Tx, chainID int64, owner string) ([]OwnerMessageTarget, error) {
	rows, err := tx.Query(ctx, `
WITH owner_kind AS (
    SELECT EXISTS (
        SELECT 1 FROM safe_signers
        WHERE chain_id = $1 AND safe_address = lower($2)
    ) AS is_safe
)
SELECT u.id, u.blockchain_address, u.username, u.avatar_url
FROM users u CROSS JOIN owner_kind k
WHERE u.status = 'APPROVED' AND NOT user_retiring(u.id)
  AND ((NOT k.is_safe AND lower(u.blockchain_address) = lower($2))
    OR (k.is_safe AND EXISTS (
        SELECT 1 FROM safe_signers ss
        WHERE ss.chain_id = $1 AND ss.safe_address = lower($2)
          AND ss.signer = lower(u.blockchain_address))))
ORDER BY lower(COALESCE(NULLIF(BTRIM(u.username), ''), u.blockchain_address)),
         lower(u.blockchain_address)`, chainID, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := []OwnerMessageTarget{}
	for rows.Next() {
		var target OwnerMessageTarget
		if err := rows.Scan(&target.UserID, &target.Address, &target.Username, &target.Avatar); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

// CatalogHasOwner limits sponsored index registration to real catalog owners.
func (pg *Pg) CatalogHasOwner(ctx context.Context, tx pgx.Tx, chainID int64, owner string) (bool, error) {
	var found bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (
    SELECT 1 FROM vouchers WHERE chain_id=$1 AND lower(owner_address)=lower($2)
    UNION ALL SELECT 1 FROM pools WHERE chain_id=$1 AND lower(owner_address)=lower($2)
)`, chainID, owner).Scan(&found)
	return found, err
}

// SafeManagesAsset reports whether target is a voucher or pool owned by safe,
// or one of the wiring contracts of a pool owned by safe.
func (pg *Pg) SafeManagesAsset(ctx context.Context, tx pgx.Tx, chainID int64, safe, target string) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1 FROM vouchers
    WHERE chain_id = $1 AND lower(address) = lower($3) AND lower(owner_address) = lower($2)
) OR EXISTS (
    SELECT 1 FROM pools
    WHERE chain_id = $1 AND lower(owner_address) = lower($2) AND lower($3) IN (
        lower(address), lower(token_registry_address), lower(limiter_address),
        lower(fee_policy_address), lower(quoter_address), lower(protocol_fee_controller_address))
)`, chainID, safe, target).Scan(&ok)
	return ok, err
}

// SafeExecutionProgresses separates an active queue job from a finalized one
// and from the OTX it submitted. River retains completed jobs, so treating
// every non-cancelled row as active leaves clients displaying "Executing…"
// long after there is no work left to run.
func (pg *Pg) SafeExecutionProgresses(ctx context.Context, tx pgx.Tx, safeTxHashes, trackingIDs []string) (map[string]SafeExecutionProgress, error) {
	if len(safeTxHashes) != len(trackingIDs) {
		return nil, fmt.Errorf("safe execution progress: hashes and tracking IDs differ in length")
	}
	rows, err := tx.Query(ctx, `
WITH requested AS (
    SELECT lower(hash) AS hash, tracking_id
    FROM unnest($1::text[], $2::text[]) AS item(hash, tracking_id)
)
SELECT requested.hash,
       COALESCE(job.id, 0), COALESCE(job.state, ''),
       COALESCE(bool_or(otx.id IS NOT NULL AND
           (dispatch.status IS NULL OR dispatch.status NOT IN ('SUCCESS', 'REVERTED', 'LOW_NONCE', 'INVALID_TX'))), false),
       COALESCE(bool_or(dispatch.status = 'SUCCESS'), false),
       COALESCE(bool_or(dispatch.status IN ('REVERTED', 'LOW_NONCE', 'INVALID_TX')), false)
FROM requested
LEFT JOIN LATERAL (
    SELECT id, state::text AS state
    FROM river_job
    WHERE kind = 'SAFE_EXECUTE' AND lower(args->>'safeTxHash') = requested.hash
    ORDER BY id DESC
    LIMIT 1
) AS job ON true
LEFT JOIN otx ON otx.tracking_id::text = requested.tracking_id
LEFT JOIN LATERAL (
    SELECT status
    FROM dispatch
    WHERE dispatch.otx_id = otx.id
    ORDER BY dispatch.id DESC
    LIMIT 1
) AS dispatch ON true
GROUP BY requested.hash, job.id, job.state`, safeTxHashes, trackingIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	progress := make(map[string]SafeExecutionProgress, len(safeTxHashes))
	for rows.Next() {
		var hash string
		var item SafeExecutionProgress
		if err := rows.Scan(&hash, &item.JobID, &item.JobState, &item.OTXActive, &item.OTXSucceeded, &item.OTXFailed); err != nil {
			return nil, err
		}
		progress[hash] = item
	}
	return progress, rows.Err()
}
