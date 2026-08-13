package sub

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	custodialEvent "github.com/cosmo-local-credit/clc-core/pkg/event"
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

	otx, err := s.store.GetOTXByTxHash(ctx, tx, chainEvent.TxHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
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
