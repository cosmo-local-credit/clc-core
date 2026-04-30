package store

import (
	"context"

	"github.com/cosmo-local-credit/clc-core/internal/keypair"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store interface {
	Pool() *pgxpool.Pool
	Bootstrap() error
	InsertKeyPair(context.Context, pgx.Tx, keypair.Key) error
	ActivateKeyPair(context.Context, pgx.Tx, string) error
	LoadMasterSignerKey(context.Context, pgx.Tx) (keypair.Key, error)
	AcquireNonce(context.Context, pgx.Tx, string) (uint64, error)
	InsertOTX(context.Context, pgx.Tx, OTX) (uint64, error)
	GetOTXByTxHash(context.Context, pgx.Tx, string) (OTX, error)
	InsertDispatchTx(context.Context, pgx.Tx, DispatchTx) error
	UpdateDispatchTxStatus(context.Context, pgx.Tx, DispatchTx) error
	GetPlatformContracts(context.Context, pgx.Tx, int64, string) (PlatformContracts, error)
	InsertDeployedToken(context.Context, pgx.Tx, DeployedToken) (uint64, error)
	GetDeployedTokenByAddress(context.Context, pgx.Tx, string) (DeployedToken, error)
	InsertTokenDeployMeta(context.Context, pgx.Tx, TokenDeployMeta) error
	GetTokenDeployMetaByOTXID(context.Context, pgx.Tx, uint64) (TokenDeployMeta, error)
	GetPoolAddress(context.Context, pgx.Tx, string) (string, error)
	SetPoolAddress(context.Context, pgx.Tx, string, string) error
	InsertDeployedPool(context.Context, pgx.Tx, DeployedPool) (uint64, error)
	GetDeployedPoolByAddress(context.Context, pgx.Tx, string) (DeployedPool, error)
	InsertPoolDeployMeta(context.Context, pgx.Tx, PoolDeployMeta) error
	GetPoolDeployMetaByOTXID(context.Context, pgx.Tx, uint64) (PoolDeployMeta, error)
	GetOracleFeedByName(context.Context, pgx.Tx, string) (OracleFeed, error)
	ListOracleFeeds(context.Context, pgx.Tx) ([]OracleFeed, error)
	GetOTXByTrackingID(context.Context, pgx.Tx, string) ([]*OTX, error)
	GetOTXByAccount(context.Context, pgx.Tx, string, uint64) ([]*OTX, error)
	GetOTXByAccountNext(context.Context, pgx.Tx, string, uint64, uint64) ([]*OTX, error)
	GetOTXByAccountPrevious(context.Context, pgx.Tx, string, uint64, uint64) ([]*OTX, error)
	PeekNonce(context.Context, pgx.Tx, string) (uint64, error)
	CheckKeypair(context.Context, pgx.Tx, string) (bool, error)
	SetPlatformContracts(context.Context, pgx.Tx, int64, string, string, string) (PlatformContracts, error)
	ListPlatformContracts(context.Context, pgx.Tx, int64) ([]PlatformContracts, error)
	InsertUpgradeMeta(context.Context, pgx.Tx, UpgradeMeta) error
	GetUpgradeMetaByOTXID(context.Context, pgx.Tx, uint64) (UpgradeMeta, error)
}
