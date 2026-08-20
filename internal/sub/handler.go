package sub

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	custodialEvent "github.com/cosmo-local-credit/clc-core/pkg/event"
	"github.com/ethereum/go-ethereum/common"
	"github.com/grassrootseconomics/eth-tracker/pkg/event"
	"github.com/jackc/pgx/v5"
)

func (s *Sub) processEvent(ctx context.Context, msgSubject string, msg []byte) error {
	s.logg.Debug("Sub processing event", "subject", msgSubject, "data", string(msg))
	var chainEvent event.Event

	if err := json.Unmarshal(msg, &chainEvent); err != nil {
		return err
	}

	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Catalog projection runs first and unconditionally. Pool composition is a
	// fact about the chain, not about this service: an operator console, a
	// script, or another relayer can all change it, and only the events say so.
	// Gating it on the OTX lookup below would mean the catalog only tracked
	// transactions clc-core happened to sign.
	if err := s.projectPoolComposition(ctx, tx, chainEvent); err != nil {
		return err
	}

	otx, err := s.store.GetOTXByTxHash(ctx, tx, chainEvent.TxHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Not ours to track, but the projection above may still have work to
			// commit.
			return tx.Commit(ctx)
		}
		return err
	}

	updateDispatchStatus := store.DispatchTx{
		OTXID: otx.ID,
	}

	if chainEvent.Success {
		updateDispatchStatus.Status = store.SUCCESS
	} else {
		updateDispatchStatus.Status = store.REVERTED
	}

	if err := s.store.UpdateDispatchTxStatus(ctx, tx, updateDispatchStatus); err != nil {
		return err
	}

	switch otx.OTXType {
	case store.TOKEN_PROXY_DEPLOY:
		if chainEvent.Success {
			if err := s.confirmToken(ctx, tx, otx.ID, chainEvent.ContractAddress); err != nil {
				return err
			}
		} else {
			if err := s.store.FailDeployedToken(ctx, tx, otx.ID); err != nil {
				return err
			}
		}
	case store.POOL_PROXY_DEPLOY:
		if chainEvent.Success {
			if err := s.confirmPool(ctx, tx, otx.ID, chainEvent.ContractAddress); err != nil {
				return err
			}
		} else {
			if err := s.store.FailDeployedPool(ctx, tx, otx.ID); err != nil {
				return err
			}
		}
	}

	_ = s.pub.Send(ctx, custodialEvent.Event{
		TrackingID: otx.TrackingID,
		Status:     updateDispatchStatus.Status,
	})

	return tx.Commit(ctx)
}

// confirmToken finalizes a token deploy and promotes it into the voucher
// catalog. Both steps share the caller's transaction, so the deploy cannot
// become CONFIRMED without a catalog row (MIGRATION.md §2.4).
//
// Confirmation is guarded on status = 'PENDING', so a redelivered event finds no
// row and promotion is skipped rather than repeated.
func (s *Sub) confirmToken(ctx context.Context, tx pgx.Tx, otxID uint64, contractAddress string) error {
	deployedTokenID, err := s.store.ConfirmDeployedToken(ctx, tx, otxID, contractAddress)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			s.logg.Warn("Sub: pending token row missing for confirmed OTX, skipping finalization", "otxId", otxID)
			return nil
		}
		return err
	}

	voucherID, err := s.store.UpsertVoucherFromDeploy(ctx, tx, deployedTokenID)
	if err != nil {
		return err
	}

	s.logg.Info("Sub: promoted confirmed token deploy into voucher catalog",
		"otxId", otxID, "deployedTokenId", deployedTokenID, "voucherId", voucherID)
	return nil
}

// confirmPool is confirmToken's counterpart for pools (MIGRATION.md §3.5).
func (s *Sub) confirmPool(ctx context.Context, tx pgx.Tx, otxID uint64, contractAddress string) error {
	deployedPoolID, err := s.store.ConfirmDeployedPool(ctx, tx, otxID, contractAddress)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			s.logg.Warn("Sub: pending pool row missing for confirmed OTX, skipping finalization", "otxId", otxID)
			return nil
		}
		return err
	}

	poolID, err := s.store.UpsertPoolFromDeploy(ctx, tx, deployedPoolID)
	if err != nil {
		return err
	}

	s.logg.Info("Sub: promoted confirmed pool deploy into pool catalog",
		"otxId", otxID, "deployedPoolId", deployedPoolID, "poolId", poolID)
	return nil
}

// Chain events that change what a pool holds. The names are eth-tracker's
// transaction types (pkg/event), one per emitting contract.
const (
	indexAddEvent      = "INDEX_ADD"
	indexRemoveEvent   = "INDEX_REMOVE"
	limitSetEvent      = "LIMIT_SET"
	oracleUpdatedEvent = "ORACLE_UPDATED"
	oracleRemovedEvent = "ORACLE_REMOVED"
)

// projectPoolComposition maintains pool_tokens from the chain.
//
// Nothing else writes that table, so without this a pool's membership, deposit
// caps and price feeds only ever reflect whatever an importer last loaded. Each
// branch touches one column and resolves the pool from the contract that emitted
// the event, because that is the only address the event carries — a registry and
// a limiter do not know which pools point at them.
//
// A reverted transaction changes nothing on chain and so must change nothing
// here; eth-tracker reports those with Success false, including calls it decoded
// from input data alone.
//
// An event naming a pool or voucher this service has not catalogued matches no
// rows. That is expected for the wider network's contracts and is logged at
// debug rather than failed, which would park the message and stall the consumer.
func (s *Sub) projectPoolComposition(ctx context.Context, tx pgx.Tx, chainEvent event.Event) error {
	if !chainEvent.Success {
		return nil
	}

	var (
		rows int64
		err  error
	)

	switch chainEvent.TxType {
	case indexAddEvent, indexRemoveEvent:
		token, ok := payloadAddress(chainEvent, "address")
		if !ok {
			return nil
		}
		rows, err = s.store.SetPoolTokenMembership(
			ctx, tx, chainEvent.ContractAddress, token, chainEvent.TxType == indexAddEvent)

	case limitSetEvent:
		token, tokenOK := payloadAddress(chainEvent, "token")
		holder, holderOK := payloadAddress(chainEvent, "holder")
		value, valueOK := payloadUint(chainEvent, "value")
		if !tokenOK || !holderOK || !valueOK {
			return nil
		}
		// The holder is the pool. A limit set for any other holder is not this
		// pool's business and resolves to no rows.
		rows, err = s.store.SetPoolTokenLimit(ctx, tx, holder, token, value)

	case oracleUpdatedEvent:
		token, tokenOK := payloadAddress(chainEvent, "token")
		aggregator, oracleOK := payloadAddress(chainEvent, "oracle")
		if !tokenOK || !oracleOK {
			return nil
		}
		rows, err = s.store.SetPoolTokenOracle(ctx, tx, chainEvent.ContractAddress, token, aggregator)

	case oracleRemovedEvent:
		token, ok := payloadAddress(chainEvent, "token")
		if !ok {
			return nil
		}
		rows, err = s.store.ClearPoolTokenOracle(ctx, tx, chainEvent.ContractAddress, token)

	default:
		return nil
	}

	if err != nil {
		return err
	}

	if rows == 0 {
		s.logg.Debug("Sub: pool composition event matched no catalog row",
			"type", chainEvent.TxType, "contract", chainEvent.ContractAddress, "tx", chainEvent.TxHash)
		return nil
	}

	s.logg.Info("Sub: projected pool composition from chain",
		"type", chainEvent.TxType, "contract", chainEvent.ContractAddress, "rows", rows)
	return nil
}

func payloadAddress(chainEvent event.Event, key string) (string, bool) {
	value, ok := chainEvent.Payload[key].(string)
	if !ok || !common.IsHexAddress(value) {
		return "", false
	}
	return value, true
}

// payloadUint keeps the value a decimal string all the way to NUMERIC: a uint256
// does not fit a float64, which is what encoding/json would otherwise give a
// number here.
func payloadUint(chainEvent event.Event, key string) (string, bool) {
	value, ok := chainEvent.Payload[key].(string)
	if !ok {
		return "", false
	}
	if _, ok := new(big.Int).SetString(value, 10); !ok {
		return "", false
	}
	return value, true
}
