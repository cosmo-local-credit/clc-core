package store

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

type (
	// Pool is a network pool catalog row (MIGRATION.md §3) — the directory
	// serving model. Distinct from DeployedPool, which is the deploy
	// pipeline's record. Most fields are pointers: catalog rows merged from
	// sparse sources (federation import, deploy metadata) may lack them.
	Pool struct {
		ID                   uint64   `db:"id"                              json:"id"`
		ChainID              int64    `db:"chain_id"                        json:"chainId"`
		Address              string   `db:"address"                         json:"address"`
		Name                 *string  `db:"name"                            json:"name"`
		Symbol               *string  `db:"symbol"                          json:"symbol"`
		OwnerAddress         *string  `db:"owner_address"                   json:"ownerAddress"`
		FeePPM               *float64 `db:"fee_ppm"                         json:"feePpm"`
		FeeAddress           *string  `db:"fee_address"                     json:"feeAddress"`
		TokenRegistryAddress *string  `db:"token_registry_address"          json:"tokenRegistryAddress"`
		LimiterAddress       *string  `db:"limiter_address"                 json:"limiterAddress"`
		QuoterAddress        *string  `db:"quoter_address"                  json:"quoterAddress"`
		// The quoter kind the pool was published with (decimal, oracle or
		// relative). NULL for imported pools and for deploys that predate
		// migration 020 — the three quoters are indistinguishable over ERC-165,
		// so nothing can reconstruct it after the fact.
		QuoterType                   *string   `db:"quoter_type"                     json:"quoterType"`
		FeePolicyAddress             *string   `db:"fee_policy_address"              json:"feePolicyAddress"`
		ProtocolFeeControllerAddress *string   `db:"protocol_fee_controller_address" json:"protocolFeeControllerAddress"`
		FeesDecoupled                *bool     `db:"fees_decoupled"                  json:"feesDecoupled"`
		Decimals                     *int32    `db:"decimals"                        json:"decimals"`
		Description                  *string   `db:"description"                     json:"description"`
		UnitOfAccount                *string   `db:"unit_of_account"                 json:"unitOfAccount"`
		LocationName                 *string   `db:"location_name"                   json:"locationName"`
		GeoLat                       *float64  `db:"geo_lat"                         json:"geoLat"`
		GeoLng                       *float64  `db:"geo_lng"                         json:"geoLng"`
		Email                        *string   `db:"email"                           json:"email"`
		Website                      *string   `db:"website"                         json:"website"`
		Phone                        *string   `db:"phone"                           json:"phone"`
		ImageURL                     *string   `db:"image_url"                       json:"imageUrl"`
		BannerURL                    *string   `db:"banner_url"                      json:"bannerUrl"`
		Terms                        *string   `db:"terms"                           json:"terms"`
		DefaultVoucherID             *uint64   `db:"default_voucher_id"              json:"defaultVoucherId"`
		PlatformContractID           *uint64   `db:"platform_contract_id"            json:"platformContractId"`
		VoucherCount                 uint64    `db:"voucher_count"                   json:"voucherCount"`
		Tags                         []string  `db:"tags"                            json:"tags"`
		CreatedAt                    time.Time `db:"created_at"                      json:"createdAt"`
		UpdatedAt                    time.Time `db:"updated_at"                      json:"updatedAt"`
	}
)

func (pg *Pg) ListPools(ctx context.Context, tx pgx.Tx, limit uint64) ([]Pool, error) {
	var pools []Pool
	rows, err := tx.Query(ctx, pg.queries.ListPools, limit)
	if err != nil {
		return nil, err
	}
	if err := pgxscan.ScanAll(&pools, rows); err != nil {
		return nil, err
	}
	return pools, nil
}

func (pg *Pg) ListPoolsNext(ctx context.Context, tx pgx.Tx, cursor uint64, limit uint64) ([]Pool, error) {
	var pools []Pool
	rows, err := tx.Query(ctx, pg.queries.ListPoolsNext, cursor, limit)
	if err != nil {
		return nil, err
	}
	if err := pgxscan.ScanAll(&pools, rows); err != nil {
		return nil, err
	}
	return pools, nil
}

func (pg *Pg) ListPoolsPrevious(ctx context.Context, tx pgx.Tx, cursor uint64, limit uint64) ([]Pool, error) {
	var pools []Pool
	rows, err := tx.Query(ctx, pg.queries.ListPoolsPrev, cursor, limit)
	if err != nil {
		return nil, err
	}
	if err := pgxscan.ScanAll(&pools, rows); err != nil {
		return nil, err
	}
	return pools, nil
}

func (pg *Pg) GetPoolByAddress(ctx context.Context, tx pgx.Tx, address string) (Pool, error) {
	var pool Pool
	rows, err := tx.Query(ctx, pg.queries.GetPoolByAddress, address)
	if err != nil {
		return pool, err
	}
	if err := pgxscan.ScanOne(&pool, rows); err != nil {
		return pool, err
	}
	return pool, nil
}
