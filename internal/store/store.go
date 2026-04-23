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
}
