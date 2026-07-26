package store

import (
	"context"

	"github.com/cosmo-local-credit/clc-core/internal/keypair"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type (
	Store interface {
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
		ConfirmDeployedToken(context.Context, pgx.Tx, uint64, string) (uint64, error)
		FailDeployedToken(context.Context, pgx.Tx, uint64) error
		InsertDeployedTokenTag(context.Context, pgx.Tx, uint64, string) error
		GetDeployedTokenByAddress(context.Context, pgx.Tx, string) (DeployedToken, error)
		GetDeployedTokenByTrackingID(context.Context, pgx.Tx, string) (DeployedToken, error)
		ListTokenTags(context.Context, pgx.Tx) ([]string, error)
		GetProtocolAddress(context.Context, pgx.Tx, string) (string, error)
		SetProtocolAddress(context.Context, pgx.Tx, string, string) error
		InsertDeployedPool(context.Context, pgx.Tx, DeployedPool) (uint64, error)
		ConfirmDeployedPool(context.Context, pgx.Tx, uint64, string) (uint64, error)
		FailDeployedPool(context.Context, pgx.Tx, uint64) error
		GetDeployedPoolByAddress(context.Context, pgx.Tx, string) (DeployedPool, error)
		GetDeployedPoolByTrackingID(context.Context, pgx.Tx, string) (DeployedPool, error)
		InsertDeployedPoolTag(context.Context, pgx.Tx, uint64, string) error
		GetOracleFeedByName(context.Context, pgx.Tx, string) (OracleFeed, error)
		ListOracleFeeds(context.Context, pgx.Tx) ([]OracleFeed, error)
		GetOTXByTrackingID(context.Context, pgx.Tx, string) ([]*OTX, error)
		GetOTXByAccount(context.Context, pgx.Tx, string, uint64) ([]*OTX, error)
		GetOTXByAccountNext(context.Context, pgx.Tx, string, uint64, uint64) ([]*OTX, error)
		GetOTXByAccountPrevious(context.Context, pgx.Tx, string, uint64, uint64) ([]*OTX, error)
		PeekNonce(context.Context, pgx.Tx, string) (uint64, error)
		CheckKeypair(context.Context, pgx.Tx, string) (bool, error)

		InsertUser(context.Context, pgx.Tx, User) (User, error)
		GetUserByAddress(context.Context, pgx.Tx, string) (User, error)
		GetUserByEmail(context.Context, pgx.Tx, string) (User, error)
		GetUserByID(context.Context, pgx.Tx, uint64) (User, error)
		UpdateUserStatus(context.Context, pgx.Tx, string, string) (User, error)
		ApprovePendingUserByAddress(context.Context, pgx.Tx, string) (User, error)
		UpdateUserLimits(context.Context, pgx.Tx, string, int, int) (User, error)
		UpdateUserProfile(context.Context, pgx.Tx, string, string, string, string, string) (User, error)
		ListUsers(context.Context, pgx.Tx) ([]User, error)
		ListUsersByStatus(context.Context, pgx.Tx, string) ([]User, error)

		InsertUserDeployment(context.Context, pgx.Tx, UserDeployment) error
		CountUserTokens(context.Context, pgx.Tx, uint64) (int, error)
		CountUserPools(context.Context, pgx.Tx, uint64) (int, error)
		ListUserDeployments(context.Context, pgx.Tx, uint64) ([]UserDeployment, error)
		GetUserDeploymentByTrackingID(context.Context, pgx.Tx, string, uint64) (UserDeployment, error)
		ListAllDeployments(context.Context, pgx.Tx) ([]UserDeploymentWithUser, error)
		ListTokensByUser(context.Context, pgx.Tx, uint64, uint64) ([]DeployedToken, error)
		ListTokensByUserNext(context.Context, pgx.Tx, uint64, uint64, uint64) ([]DeployedToken, error)
		ListTokensByUserPrevious(context.Context, pgx.Tx, uint64, uint64, uint64) ([]DeployedToken, error)
		ListPoolsByUser(context.Context, pgx.Tx, uint64, uint64) ([]DeployedPool, error)
		ListPoolsByUserNext(context.Context, pgx.Tx, uint64, uint64, uint64) ([]DeployedPool, error)
		ListPoolsByUserPrevious(context.Context, pgx.Tx, uint64, uint64, uint64) ([]DeployedPool, error)
		ListConfirmedPools(context.Context, pgx.Tx, uint64) ([]DeployedPool, error)
		ListConfirmedPoolsNext(context.Context, pgx.Tx, uint64, uint64) ([]DeployedPool, error)
		ListConfirmedPoolsPrevious(context.Context, pgx.Tx, uint64, uint64) ([]DeployedPool, error)
		ListAllTokens(context.Context, pgx.Tx) ([]DeployedToken, error)
		ListAllPools(context.Context, pgx.Tx) ([]DeployedPool, error)
		ListVoucherMetadataByAddresses(context.Context, pgx.Tx, []string) ([]VoucherMetadata, error)
		ListPools(context.Context, pgx.Tx, uint64) ([]Pool, error)
		ListPoolsNext(context.Context, pgx.Tx, uint64, uint64) ([]Pool, error)
		ListPoolsPrevious(context.Context, pgx.Tx, uint64, uint64) ([]Pool, error)
		GetPoolByAddress(context.Context, pgx.Tx, string) (Pool, error)
		ListPoolVouchers(context.Context, pgx.Tx, string, uint64) ([]PoolVoucher, error)
		ListPoolVouchersNext(context.Context, pgx.Tx, string, uint64, uint64) ([]PoolVoucher, error)
		ListPoolVouchersPrevious(context.Context, pgx.Tx, string, uint64, uint64) ([]PoolVoucher, error)
		ListOfferings(context.Context, pgx.Tx, uint64) ([]Offering, error)
		ListOfferingsNext(context.Context, pgx.Tx, uint64, uint64) ([]Offering, error)
		ListOfferingsPrevious(context.Context, pgx.Tx, uint64, uint64) ([]Offering, error)
		ListVoucherOfferings(context.Context, pgx.Tx, string, uint64) ([]Offering, error)
		ListVoucherOfferingsNext(context.Context, pgx.Tx, string, uint64, uint64) ([]Offering, error)
		ListVoucherOfferingsPrevious(context.Context, pgx.Tx, string, uint64, uint64) ([]Offering, error)
		ListMarketVouchers(context.Context, pgx.Tx, *MarketVoucherBounds, uint64) ([]MarketVoucher, error)
		ListMarketVouchersNext(context.Context, pgx.Tx, *MarketVoucherBounds, uint64, uint64) ([]MarketVoucher, error)
		ListMarketVouchersPrevious(context.Context, pgx.Tx, *MarketVoucherBounds, uint64, uint64) ([]MarketVoucher, error)

		SetPlatformContracts(context.Context, pgx.Tx, int64, string, string, string) (PlatformContracts, error)
		ListPlatformContracts(context.Context, pgx.Tx, int64) ([]PlatformContracts, error)
		InsertUpgradeMeta(context.Context, pgx.Tx, UpgradeMeta) error
		GetUpgradeMetaByOTXID(context.Context, pgx.Tx, uint64) (UpgradeMeta, error)

		UpsertWebAuthnCredential(context.Context, pgx.Tx, WebAuthnCredential) error
		GetWebAuthnCredential(context.Context, pgx.Tx, string) (WebAuthnCredential, error)
		SetNonceIfHigher(context.Context, pgx.Tx, string, uint64) error
	}
)
