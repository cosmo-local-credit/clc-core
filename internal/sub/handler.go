package sub

import (
	"context"
	"errors"
	"math/big"

	"github.com/cosmo-local-credit/clc-core/internal/router"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	custodialEvent "github.com/cosmo-local-credit/clc-core/pkg/event"
	"github.com/ethereum/go-ethereum/common"
	"github.com/grassrootseconomics/eth-tracker/pkg/event"
	"github.com/jackc/pgx/v5"
)

func (s *Sub) processEvent(ctx context.Context, msgSubject string, msg []byte) error {
	s.logg.Debug("Sub processing event", "subject", msgSubject, "data", string(msg))

	chainEvent, err := decodeEvent(msg)
	if err != nil {
		return err
	}

	deltas, err := s.processEventTx(ctx, chainEvent)
	if err != nil {
		return err
	}
	// Phase two: the graph sees a change only after the database has
	// committed it, so it can never reflect a rolled-back write.
	if s.router != nil && len(deltas) > 0 {
		s.router.Apply(deltas...)
	}
	return nil
}

// processEventTx runs every projection in one transaction and returns the
// router deltas the committed writes imply.
func (s *Sub) processEventTx(ctx context.Context, chainEvent event.Event) ([]router.Delta, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// Catalog projection runs first and unconditionally. Pool composition is a
	// fact about the chain, not about this service: an operator console, a
	// script, or another relayer can all change it, and only the events say so.
	// Gating it on the OTX lookup below would mean the catalog only tracked
	// transactions clc-core happened to sign.
	if err := s.projectCatalogOwnership(ctx, tx, chainEvent); err != nil {
		return nil, err
	}
	deltas, err := s.projectPoolComposition(ctx, tx, chainEvent)
	if err != nil {
		return nil, err
	}
	deltas = append(deltas, routerSignals(chainEvent)...)
	if err := s.projectSafeSigners(ctx, tx, chainEvent); err != nil {
		return nil, err
	}

	otx, err := s.store.GetOTXByTxHash(ctx, tx, chainEvent.TxHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Not ours to track, but the projection above may still have work to
			// commit.
			return deltas, tx.Commit(ctx)
		}
		return nil, err
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
		return nil, err
	}

	if err := s.settleRoutedSwap(ctx, tx, otx.TrackingID, chainEvent); err != nil {
		return nil, err
	}

	switch otx.OTXType {
	case store.TOKEN_PROXY_DEPLOY:
		if chainEvent.Success {
			if err := s.confirmToken(ctx, tx, otx.ID, chainEvent.ContractAddress); err != nil {
				return nil, err
			}
		} else {
			if err := s.store.FailDeployedToken(ctx, tx, otx.ID); err != nil {
				return nil, err
			}
		}
	case store.POOL_PROXY_DEPLOY:
		if chainEvent.Success {
			if err := s.confirmPool(ctx, tx, otx.ID, chainEvent.ContractAddress); err != nil {
				return nil, err
			}
			// A new pool is a new graph node; reload rather than patch.
			deltas = append(deltas, router.ReloadDelta{})
		} else {
			if err := s.store.FailDeployedPool(ctx, tx, otx.ID); err != nil {
				return nil, err
			}
		}
	}

	_ = s.pub.Send(ctx, custodialEvent.Event{
		TrackingID: otx.TrackingID,
		Status:     updateDispatchStatus.Status,
	})

	return deltas, tx.Commit(ctx)
}

// confirmToken finalizes a token deploy and promotes it into the voucher
// catalog. Both steps share the caller's transaction, so the deploy cannot
// become CONFIRMED without a catalog row.
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

// confirmPool is confirmToken's counterpart for pools.
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
// caps, price indexes and price feeds only ever reflect whatever an importer
// last loaded. Each branch touches one column and resolves the pool from the
// contract that emitted the event, because that is the only address the event
// carries — a registry, a limiter and a quoter do not know which pools point at
// them.
//
// Every write carries the event's (block, log index) and lands only when that
// is newer than the field's last write: NATS redelivers a failed message after
// newer ones, and the older value must not win. The rows a write changed come
// back as router deltas, which the caller applies once the transaction has
// committed.
//
// A reverted transaction changes nothing on chain and so must change nothing
// here; eth-tracker reports those with Success false, including calls it decoded
// from input data alone.
//
// An event naming a pool or voucher this service has not catalogued matches no
// rows. That is expected for the wider network's contracts and is logged at
// debug rather than failed, which would park the message and stall the consumer.
func (s *Sub) projectPoolComposition(ctx context.Context, tx pgx.Tx, chainEvent event.Event) ([]router.Delta, error) {
	if !chainEvent.Success {
		return nil, nil
	}

	var (
		keys   []router.EdgeKey
		deltas []router.Delta
		err    error
		pos    = eventPosition(chainEvent)
	)

	switch chainEvent.TxType {
	case indexAddEvent, indexRemoveEvent:
		token, ok := payloadAddress(chainEvent, "address")
		if !ok {
			return nil, nil
		}
		allowed := chainEvent.TxType == indexAddEvent
		keys, err = s.store.SetPoolTokenMembership(ctx, tx, chainEvent.ContractAddress, token, allowed, pos)
		deltas = edgeDeltas(keys, func(k router.EdgeKey) router.Delta {
			return router.EdgeAllowedDelta{Key: k, Allowed: allowed, Pos: pos}
		})

	case limitSetEvent:
		token, tokenOK := payloadAddress(chainEvent, "token")
		holder, holderOK := payloadAddress(chainEvent, "holder")
		value, valueOK := payloadUint(chainEvent, "value")
		limit, limitOK := parseUint256(value)
		if !tokenOK || !holderOK || !valueOK || !limitOK {
			return nil, nil
		}
		// The holder is the pool. A limit set for any other holder is not this
		// pool's business and resolves to no rows.
		keys, err = s.store.SetPoolTokenLimit(ctx, tx, holder, token, value, pos)
		deltas = edgeDeltas(keys, func(k router.EdgeKey) router.Delta {
			return router.EdgeLimitDelta{Key: k, Limit: limit, Pos: pos}
		})

	case quoterPriceEvent:
		token, tokenOK := payloadAddress(chainEvent, "token")
		value, valueOK := payloadUint(chainEvent, "exchangeRate")
		index, indexOK := parseUint256(value)
		if !tokenOK || !valueOK || !indexOK {
			return nil, nil
		}
		keys, err = s.store.SetPoolTokenPriceIndex(ctx, tx, chainEvent.ContractAddress, token, value, pos)
		deltas = edgeDeltas(keys, func(k router.EdgeKey) router.Delta {
			return router.EdgePriceDelta{Key: k, Index: index, Pos: pos}
		})

	case oracleUpdatedEvent:
		token, tokenOK := payloadAddress(chainEvent, "token")
		aggregator, oracleOK := payloadAddress(chainEvent, "oracle")
		if !tokenOK || !oracleOK {
			return nil, nil
		}
		// Oracle-priced pools are not routed, so the graph has nothing to apply.
		keys, err = s.store.SetPoolTokenOracle(ctx, tx, chainEvent.ContractAddress, token, aggregator, pos)

	case oracleRemovedEvent:
		token, ok := payloadAddress(chainEvent, "token")
		if !ok {
			return nil, nil
		}
		keys, err = s.store.ClearPoolTokenOracle(ctx, tx, chainEvent.ContractAddress, token, pos)

	default:
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	if len(keys) == 0 {
		s.logg.Debug("Sub: pool composition event changed no catalog row",
			"type", chainEvent.TxType, "contract", chainEvent.ContractAddress, "tx", chainEvent.TxHash)
		return nil, nil
	}

	s.logg.Info("Sub: projected pool composition from chain",
		"type", chainEvent.TxType, "contract", chainEvent.ContractAddress, "rows", len(keys))
	return deltas, nil
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
