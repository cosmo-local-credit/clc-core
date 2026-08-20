package store

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

type (
	DeployedPool struct {
		ID                           uint64    `db:"id"                              json:"id"`
		OTXID                        uint64    `db:"otx_id"                          json:"otxId"`
		PlatformContractID           uint64    `db:"platform_contract_id"            json:"platformContractId"`
		Status                       string    `db:"status"                          json:"status"`
		Address                      *string   `db:"address"                         json:"address"`
		Name                         string    `db:"name"                            json:"name"`
		Symbol                       string    `db:"symbol"                          json:"symbol"`
		Decimals                     uint8     `db:"decimals"                        json:"decimals"`
		TokenRegistryAddress         string    `db:"token_registry_address"          json:"tokenRegistryAddress"`
		LimiterAddress               string    `db:"limiter_address"                 json:"limiterAddress"`
		FeePolicyAddress             string    `db:"fee_policy_address"              json:"feePolicyAddress"`
		QuoterAddress                string    `db:"quoter_address"                  json:"quoterAddress"`
		QuoterType                   *string   `db:"quoter_type"                     json:"quoterType"`
		ProtocolFeeControllerAddress string    `db:"protocol_fee_controller_address" json:"protocolFeeControllerAddress"`
		FeesDecoupled                bool      `db:"fees_decoupled"                  json:"feesDecoupled"`
		Admin                        string    `db:"admin"                           json:"admin"`
		Description                  *string   `db:"description"                     json:"description"`
		Website                      *string   `db:"website"                         json:"website"`
		Email                        *string   `db:"email"                           json:"email"`
		Phone                        *string   `db:"phone"                           json:"phone"`
		Terms                        *string   `db:"terms"                           json:"terms"`
		UnitOfAccount                *string   `db:"unit_of_account"                 json:"unitOfAccount"`
		LocationName                 *string   `db:"location_name"                   json:"locationName"`
		GeoLat                       *float64  `db:"geo_lat"                         json:"geoLat"`
		GeoLng                       *float64  `db:"geo_lng"                         json:"geoLng"`
		ImageURL                     *string   `db:"image_url"                       json:"imageUrl"`
		BannerURL                    *string   `db:"banner_url"                      json:"bannerUrl"`
		Tags                         []string  `db:"tags"                            json:"tags"`
		TrackingID                   string    `db:"tracking_id"                     json:"trackingId"`
		CreatedAt                    time.Time `db:"created_at"                      json:"createdAt"`
		UpdatedAt                    time.Time `db:"updated_at"                      json:"updatedAt"`
	}
)

func (pg *Pg) InsertDeployedPool(ctx context.Context, tx pgx.Tx, dp DeployedPool) (uint64, error) {
	var id uint64

	if err := tx.QueryRow(
		ctx,
		pg.queries.InsertDeployedPool,
		dp.OTXID,
		dp.PlatformContractID,
		dp.Name,
		dp.Symbol,
		dp.Decimals,
		dp.TokenRegistryAddress,
		dp.LimiterAddress,
		dp.FeePolicyAddress,
		dp.QuoterAddress,
		dp.QuoterType,
		dp.ProtocolFeeControllerAddress,
		dp.FeesDecoupled,
		dp.Admin,
		dp.Description,
		dp.Website,
		dp.Email,
		dp.Phone,
		dp.Terms,
		dp.UnitOfAccount,
		dp.LocationName,
		dp.GeoLat,
		dp.GeoLng,
		dp.ImageURL,
		dp.BannerURL,
	).Scan(&id); err != nil {
		return id, err
	}

	return id, nil
}

func (pg *Pg) ConfirmDeployedPool(ctx context.Context, tx pgx.Tx, otxID uint64, address string) (uint64, error) {
	var id uint64

	if err := tx.QueryRow(ctx, pg.queries.ConfirmDeployedPool, otxID, address).Scan(&id); err != nil {
		return id, err
	}

	return id, nil
}

func (pg *Pg) FailDeployedPool(ctx context.Context, tx pgx.Tx, otxID uint64) error {
	_, err := tx.Exec(ctx, pg.queries.FailDeployedPool, otxID)
	return err
}

// UpsertPoolFromDeploy promotes a confirmed pool deploy into the network pool
// catalog, copies its tags to pool_tags, sets deployed_pools.pool_id, and
// returns the catalog pool ID. The voucher-side counterpart is
// UpsertVoucherFromDeploy; both carry the same transaction and ErrNoRows
// contract.
func (pg *Pg) UpsertPoolFromDeploy(ctx context.Context, tx pgx.Tx, deployedPoolID uint64) (uint64, error) {
	var poolID uint64

	if err := tx.QueryRow(ctx, pg.queries.UpsertPoolFromDeploy, deployedPoolID).Scan(&poolID); err != nil {
		return poolID, err
	}

	return poolID, nil
}

func (pg *Pg) GetDeployedPoolByAddress(ctx context.Context, tx pgx.Tx, address string) (DeployedPool, error) {
	var dp DeployedPool

	row, err := tx.Query(ctx, pg.queries.GetDeployedPoolByAddress, address)
	if err != nil {
		return dp, err
	}

	if err := pgxscan.ScanOne(&dp, row); err != nil {
		return dp, err
	}

	return dp, nil
}

func (pg *Pg) GetDeployedPoolByTrackingID(ctx context.Context, tx pgx.Tx, trackingID string) (DeployedPool, error) {
	var dp DeployedPool

	row, err := tx.Query(ctx, pg.queries.GetDeployedPoolByTrackingID, trackingID)
	if err != nil {
		return dp, err
	}

	if err := pgxscan.ScanOne(&dp, row); err != nil {
		return dp, err
	}

	return dp, nil
}

func (pg *Pg) InsertDeployedPoolTag(ctx context.Context, tx pgx.Tx, deployedPoolID uint64, tag string) error {
	_, err := tx.Exec(ctx, pg.queries.InsertDeployedPoolTag, deployedPoolID, tag)
	return err
}
