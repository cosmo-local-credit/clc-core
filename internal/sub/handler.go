package sub

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/internal/worker"
	custodialEvent "github.com/cosmo-local-credit/clc-core/pkg/event"
	"github.com/ethereum/go-ethereum/common"
	"github.com/grassrootseconomics/eth-tracker/pkg/event"
	"github.com/jackc/pgx/v5"
	"github.com/lmittmann/w3"
	"github.com/lmittmann/w3/module/eth"
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

	otx, err := s.store.GetOTXByTxHash(
		ctx,
		tx,
		chainEvent.TxHash,
	)
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
		switch msgSubject {
		case "TRACKER.TOKEN_TRANSFER":
			var inIndex bool
			if err := s.provider.Client.CallCtx(
				ctx,
				eth.CallFunc(
					s.usersIndex,
					worker.Abi[worker.Have],
					w3.A(otx.SignerAccount),
				).Returns(&inIndex),
			); err != nil {
				return err
			}
			if inIndex {
				s.logg.Debug("queueing gas refill for sn user", "address", otx.SignerAccount)
				_, err = s.queueClient.InsertTx(ctx, tx, worker.GasRefillArgs{
					TrackingID: otx.TrackingID,
					Address:    otx.SignerAccount,
				}, nil)
				if err != nil {
					return err
				}
			}
		}
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

	// Divvi refferal submission
	// Best effort, no error checking here
	if s.activateDivviSubmissions {
		s.provider.SubmitReferral(ctx, common.HexToHash(chainEvent.TxHash))
	}

	return tx.Commit(ctx)
}
