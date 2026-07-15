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

	if chainEvent.Success && otx.OTXType == store.TOKEN_PROXY_DEPLOY {
		meta, err := s.store.GetTokenDeployMetaByOTXID(ctx, tx, otx.ID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				s.logg.Warn("Sub: token deploy meta missing for confirmed OTX, skipping finalization", "otxId", otx.ID)
				_ = s.pub.Send(ctx, custodialEvent.Event{TrackingID: otx.TrackingID, Status: updateDispatchStatus.Status})
				return tx.Commit(ctx)
			}
			return err
		}

		deployedID, err := s.store.InsertDeployedToken(ctx, tx, store.DeployedToken{
			OTXID:              otx.ID,
			PlatformContractID: meta.PlatformContractID,
			Address:            chainEvent.ContractAddress,
			Symbol:             meta.TokenSymbol,
			Name:               meta.TokenName,
			Admin:              meta.Admin,
			Description:        meta.Description,
			Website:            meta.Website,
			Email:              meta.Email,
			Phone:              meta.Phone,
			VoucherUoa:         meta.VoucherUoa,
			VoucherValue:       meta.VoucherValue,
			LocationName:       meta.LocationName,
			GeoLat:             meta.GeoLat,
			GeoLng:             meta.GeoLng,
		})
		if err != nil {
			return err
		}

		tags, err := s.store.GetTokenDeployMetaTags(ctx, tx, otx.ID)
		if err != nil {
			return err
		}
		for _, tag := range tags {
			if err := s.store.InsertDeployedTokenTag(ctx, tx, deployedID, tag); err != nil {
				return err
			}
		}
	}

	if chainEvent.Success && otx.OTXType == store.POOL_PROXY_DEPLOY {
		meta, err := s.store.GetPoolDeployMetaByOTXID(ctx, tx, otx.ID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				s.logg.Warn("Sub: pool deploy meta missing for confirmed OTX, skipping finalization", "otxId", otx.ID)
				_ = s.pub.Send(ctx, custodialEvent.Event{TrackingID: otx.TrackingID, Status: updateDispatchStatus.Status})
				return tx.Commit(ctx)
			}
			return err
		}

		deployedPoolID, err := s.store.InsertDeployedPool(ctx, tx, store.DeployedPool{
			OTXID:                        otx.ID,
			PlatformContractID:           meta.PlatformContractID,
			Address:                      chainEvent.ContractAddress,
			Name:                         meta.PoolName,
			Symbol:                       meta.PoolSymbol,
			TokenRegistryAddress:         meta.TokenRegistryAddress,
			LimiterAddress:               meta.LimiterAddress,
			FeePolicyAddress:             meta.FeePolicyAddress,
			QuoterAddress:                meta.QuoterAddress,
			ProtocolFeeControllerAddress: meta.ProtocolFeeControllerAddress,
			FeesDecoupled:                meta.FeesDecoupled,
			Admin:                        meta.Admin,
			Description:                  meta.Description,
			Website:                      meta.Website,
			Email:                        meta.Email,
			Phone:                        meta.Phone,
			LocationName:                 meta.LocationName,
			GeoLat:                       meta.GeoLat,
			GeoLng:                       meta.GeoLng,
			ImageURL:                     meta.ImageURL,
			BannerURL:                    meta.BannerURL,
		})
		if err != nil {
			return err
		}

		poolTags, err := s.store.GetPoolDeployMetaTags(ctx, tx, otx.ID)
		if err != nil {
			return err
		}
		for _, tag := range poolTags {
			if err := s.store.InsertDeployedPoolTag(ctx, tx, deployedPoolID, tag); err != nil {
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
