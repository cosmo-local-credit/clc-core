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
	s.logg.Debug("sub processing event", "subject", msgSubject, "data", string(msg))
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

	s.pub.Send(ctx, custodialEvent.Event{
		TrackingID: otx.TrackingID,
		Status:     updateDispatchStatus.Status,
	})

	return tx.Commit(ctx)
}
