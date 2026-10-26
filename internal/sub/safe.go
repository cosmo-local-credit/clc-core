package sub

import (
	"context"
	"errors"
	"fmt"

	"github.com/cosmo-local-credit/clc-core/internal/safe"
	"github.com/ethereum/go-ethereum/common"
	"github.com/grassrootseconomics/eth-tracker/pkg/event"
	"github.com/jackc/pgx/v5"
)

type (
	// SafeReader reads a Safe's signer set at a block. internal/safe.Reader
	// satisfies it; tests pass a fake.
	SafeReader interface {
		ReadSafe(ctx context.Context, address common.Address, block uint64) (*safe.State, error)
	}
)

// projectSafeSigners keeps persisted membership in step with the chain. Owner
// and threshold events refresh it; asset handovers and AccountIndex additions
// discover multisigs before they have emitted their own owner events.
//
// Neither is applied as a delta. A Safe event names one change, so the whole
// set is re-read at the event's block and replaced; a missed event then
// cannot leave membership wrong past the next one.
//
// The read needs the event block's state. A subscriber that falls behind the
// RPC's pruning window (hours on Celo) or meets an RPC outage must not hold
// back the catalog and OTX projections sharing this transaction, nor be
// redelivered forever, so a failed read queues a durable head refresh that
// commits with the event instead.
func (s *Sub) projectSafeSigners(ctx context.Context, tx pgx.Tx, evt event.Event) error {
	if s.safeReader == nil || !evt.Success {
		return nil
	}
	var safeAddress string
	switch evt.TxType {
	case "SAFE_OWNERS_CHANGED":
		safeAddress = evt.ContractAddress
	case "OWNERSHIP_TRANSFERRED":
		owner, ok := payloadAddress(evt, "newOwner")
		if !ok {
			return nil // projectCatalogOwnership already rejects the malformed event
		}
		safeAddress = owner
	case "INDEX_ADD":
		address, ok := payloadAddress(evt, "address")
		if !ok {
			return nil
		}
		safeAddress = address
	default:
		return nil
	}
	if !common.IsHexAddress(safeAddress) {
		return fmt.Errorf("malformed safe event for %s", evt.TxHash)
	}
	state, err := s.safeReader.ReadSafe(ctx, common.HexToAddress(safeAddress), evt.Block)
	if errors.Is(err, safe.ErrNotSafe) {
		return nil
	}
	if err != nil {
		if s.refreshSafe == nil {
			return err
		}
		s.logg.Warn("safe signer read at event block failed; queued a head refresh",
			"address", safeAddress, "block", evt.Block, "error", err)
		return s.refreshSafe(ctx, tx, safeAddress, evt.Block, evt.TxType == "OWNERSHIP_TRANSFERRED")
	}
	signers := make([]string, len(state.Owners))
	for i, owner := range state.Owners {
		signers[i] = owner.Hex()
	}
	_, err = s.store.ReplaceSafeSigners(ctx, tx, s.chainID, safeAddress, signers, state.Threshold, evt.Block, uint64(evt.Index))
	if err != nil {
		return err
	}
	// Ownership and first inspection enqueue the ordinary account-index path.
	// Its INDEX_ADD event already means registration is complete.
	if s.registerSafe != nil && evt.TxType == "OWNERSHIP_TRANSFERRED" {
		owned, err := s.store.CatalogHasOwner(ctx, tx, s.chainID, safeAddress)
		if err != nil {
			return err
		}
		if owned {
			return s.registerSafe(ctx, tx, safeAddress)
		}
	}
	return nil
}
