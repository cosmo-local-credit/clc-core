package store

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

type (
	PoolDeployMeta struct {
		ID                           uint64    `db:"id"                              json:"id"`
		OTXID                        uint64    `db:"otx_id"                          json:"otxId"`
		PlatformContractID           uint64    `db:"platform_contract_id"            json:"platformContractId"`
		PoolName                     string    `db:"pool_name"                       json:"poolName"`
		PoolSymbol                   string    `db:"pool_symbol"                     json:"poolSymbol"`
		PoolDecimals                 uint8     `db:"pool_decimals"                   json:"poolDecimals"`
		TokenRegistryAddress         string    `db:"token_registry_address"          json:"tokenRegistryAddress"`
		LimiterAddress               string    `db:"limiter_address"                 json:"limiterAddress"`
		FeePolicyAddress             string    `db:"fee_policy_address"              json:"feePolicyAddress"`
		QuoterAddress                string    `db:"quoter_address"                  json:"quoterAddress"`
		ProtocolFeeControllerAddress string    `db:"protocol_fee_controller_address" json:"protocolFeeControllerAddress"`
		FeesDecoupled                bool      `db:"fees_decoupled"                  json:"feesDecoupled"`
		Admin                        string    `db:"admin"                           json:"admin"`
		Description                  *string   `db:"description"                     json:"description"`
		Website                      *string   `db:"website"                         json:"website"`
		Email                        *string   `db:"email"                           json:"email"`
		Phone                        *string   `db:"phone"                           json:"phone"`
		LocationName                 *string   `db:"location_name"                   json:"locationName"`
		GeoLat                       *float64  `db:"geo_lat"                         json:"geoLat"`
		GeoLng                       *float64  `db:"geo_lng"                         json:"geoLng"`
		CreatedAt                    time.Time `db:"created_at"                      json:"createdAt"`
		UpdatedAt                    time.Time `db:"updated_at"                      json:"updatedAt"`
	}

	DeployedPool struct {
		ID                           uint64    `db:"id"                              json:"id"`
		OTXID                        uint64    `db:"otx_id"                          json:"otxId"`
		PlatformContractID           uint64    `db:"platform_contract_id"            json:"platformContractId"`
		Address                      string    `db:"address"                         json:"address"`
		Name                         string    `db:"name"                            json:"name"`
		Symbol                       string    `db:"symbol"                          json:"symbol"`
		TokenRegistryAddress         string    `db:"token_registry_address"          json:"tokenRegistryAddress"`
		LimiterAddress               string    `db:"limiter_address"                 json:"limiterAddress"`
		FeePolicyAddress             string    `db:"fee_policy_address"              json:"feePolicyAddress"`
		QuoterAddress                string    `db:"quoter_address"                  json:"quoterAddress"`
		ProtocolFeeControllerAddress string    `db:"protocol_fee_controller_address" json:"protocolFeeControllerAddress"`
		FeesDecoupled                bool      `db:"fees_decoupled"                  json:"feesDecoupled"`
		Admin                        string    `db:"admin"                           json:"admin"`
		Description                  *string   `db:"description"                     json:"description"`
		Website                      *string   `db:"website"                         json:"website"`
		Email                        *string   `db:"email"                           json:"email"`
		Phone                        *string   `db:"phone"                           json:"phone"`
		LocationName                 *string   `db:"location_name"                   json:"locationName"`
		GeoLat                       *float64  `db:"geo_lat"                         json:"geoLat"`
		GeoLng                       *float64  `db:"geo_lng"                         json:"geoLng"`
		Tags                         []string  `db:"tags"                            json:"tags"`
		TrackingID                   string    `db:"tracking_id"                     json:"trackingId"`
		CreatedAt                    time.Time `db:"created_at"                      json:"createdAt"`
		UpdatedAt                    time.Time `db:"updated_at"                      json:"updatedAt"`
	}
)

func (pg *Pg) InsertPoolDeployMeta(ctx context.Context, tx pgx.Tx, meta PoolDeployMeta) error {
	_, err := tx.Exec(
		ctx,
		pg.queries.InsertPoolDeployMeta,
		meta.OTXID,
		meta.PlatformContractID,
		meta.PoolName,
		meta.PoolSymbol,
		meta.PoolDecimals,
		meta.TokenRegistryAddress,
		meta.LimiterAddress,
		meta.FeePolicyAddress,
		meta.QuoterAddress,
		meta.ProtocolFeeControllerAddress,
		meta.FeesDecoupled,
		meta.Admin,
		meta.Description,
		meta.Website,
		meta.Email,
		meta.Phone,
		meta.LocationName,
		meta.GeoLat,
		meta.GeoLng,
	)
	return err
}

func (pg *Pg) GetPoolDeployMetaByOTXID(ctx context.Context, tx pgx.Tx, otxID uint64) (PoolDeployMeta, error) {
	var meta PoolDeployMeta

	row, err := tx.Query(ctx, pg.queries.GetPoolDeployMetaByOTXID, otxID)
	if err != nil {
		return meta, err
	}

	if err := pgxscan.ScanOne(&meta, row); err != nil {
		return meta, err
	}

	return meta, nil
}

func (pg *Pg) InsertDeployedPool(ctx context.Context, tx pgx.Tx, dp DeployedPool) (uint64, error) {
	var id uint64

	if err := tx.QueryRow(
		ctx,
		pg.queries.InsertDeployedPool,
		dp.OTXID,
		dp.PlatformContractID,
		dp.Address,
		dp.Name,
		dp.Symbol,
		dp.TokenRegistryAddress,
		dp.LimiterAddress,
		dp.FeePolicyAddress,
		dp.QuoterAddress,
		dp.ProtocolFeeControllerAddress,
		dp.FeesDecoupled,
		dp.Admin,
		dp.Description,
		dp.Website,
		dp.Email,
		dp.Phone,
		dp.LocationName,
		dp.GeoLat,
		dp.GeoLng,
	).Scan(&id); err != nil {
		return id, err
	}

	return id, nil
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

func (pg *Pg) InsertPoolDeployMetaTag(ctx context.Context, tx pgx.Tx, otxID uint64, tag string) error {
	_, err := tx.Exec(ctx, pg.queries.InsertPoolDeployMetaTag, otxID, tag)
	return err
}

func (pg *Pg) GetPoolDeployMetaTags(ctx context.Context, tx pgx.Tx, otxID uint64) ([]string, error) {
	rows, err := tx.Query(ctx, pg.queries.GetPoolDeployMetaTags, otxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tags []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, err
		}
		tags = append(tags, tag)
	}
	return tags, rows.Err()
}

func (pg *Pg) InsertDeployedPoolTag(ctx context.Context, tx pgx.Tx, deployedPoolID uint64, tag string) error {
	_, err := tx.Exec(ctx, pg.queries.InsertDeployedPoolTag, deployedPoolID, tag)
	return err
}
