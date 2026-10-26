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
