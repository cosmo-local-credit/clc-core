package store

import (
	"context"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/keypair"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type (
	Store interface {
		ListTransferSearchTokens(context.Context, pgx.Tx, CatalogFilter) ([]string, error)
		CuratedCatalogStore
		SuggestUsername(context.Context, pgx.Tx, string) (string, error)
		SearchRecipients(context.Context, pgx.Tx, string, int64, string) ([]Recipient, error)
		ResolveRecipient(context.Context, pgx.Tx, string, int64, bool) (Recipient, error)
		ReserveDeploymentIdentity(context.Context, pgx.Tx, string, string, string, string) error
		ReconcileDeploymentIdentities(context.Context, pgx.Tx) error
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
		GetUnsettledOTXBySigner(context.Context, pgx.Tx, string) ([]*OTX, error)
		SetOTXReplaced(context.Context, pgx.Tx, uint64) error
		CountUndispatchedBelowNonce(context.Context, pgx.Tx, string, uint64) (uint64, error)
		LockSigner(context.Context, pgx.Tx, string) error
		GetPlatformContracts(context.Context, pgx.Tx, int64, string) (PlatformContracts, error)
		InsertDeployedToken(context.Context, pgx.Tx, DeployedToken) (uint64, error)
		ConfirmDeployedToken(context.Context, pgx.Tx, uint64, string) (uint64, error)
		FailDeployedToken(context.Context, pgx.Tx, uint64) error
		UpsertVoucherFromDeploy(context.Context, pgx.Tx, uint64) (uint64, error)
		InsertDeployedTokenTag(context.Context, pgx.Tx, uint64, string) error
		GetDeployedTokenByAddress(context.Context, pgx.Tx, string) (DeployedToken, error)
		GetDeployedTokenByTrackingID(context.Context, pgx.Tx, string) (DeployedToken, error)
		ListTakenVoucherSymbols(context.Context, pgx.Tx, string) ([]string, error)
		VoucherDisplayNameTaken(context.Context, pgx.Tx, string, *string) (bool, error)
		ListTokenTags(context.Context, pgx.Tx) ([]string, error)
		FindTokenTag(context.Context, pgx.Tx, string) (string, error)
		InsertCustomTokenTag(context.Context, pgx.Tx, string) error
		ListOfferingUnits(context.Context, pgx.Tx) ([]string, error)
		FindOfferingUnit(context.Context, pgx.Tx, string) (string, error)
		InsertCustomOfferingUnit(context.Context, pgx.Tx, string) error
		ListUnitsOfAccount(context.Context, pgx.Tx) ([]VocabularyEntry, error)
		FindUnitOfAccount(context.Context, pgx.Tx, string) (VocabularyEntry, error)
		InsertCustomUnitOfAccount(context.Context, pgx.Tx, string) error
		ListVocabulary(context.Context, pgx.Tx) (Vocabulary, error)
		LockTokenTag(context.Context, pgx.Tx, string) (VocabularyEntry, error)
		LockOfferingUnit(context.Context, pgx.Tx, string) (VocabularyEntry, error)
		LockUnitOfAccount(context.Context, pgx.Tx, string) (VocabularyEntry, error)
		RenameVocabulary(context.Context, pgx.Tx, bool, string, string) (string, error)
		HasUnfinishedTagDeployment(context.Context, pgx.Tx, string) (bool, error)
		DeleteCustomTokenTag(context.Context, pgx.Tx, string) (TagVocabularyUsage, error)
		DeleteCustomOfferingUnit(context.Context, pgx.Tx, string) (UnitVocabularyUsage, error)
		RenameUnitOfAccount(context.Context, pgx.Tx, string, string) (string, error)
		HasUnfinishedUnitOfAccountDeployment(context.Context, pgx.Tx, string) (bool, error)
		DeleteUnusedCustomUnitOfAccount(context.Context, pgx.Tx, string) (UnitOfAccountVocabularyUsage, error)
		GetProtocolAddress(context.Context, pgx.Tx, string) (string, error)
		SetProtocolAddress(context.Context, pgx.Tx, string, string) error
		InsertDeployedPool(context.Context, pgx.Tx, DeployedPool) (uint64, error)
		ConfirmDeployedPool(context.Context, pgx.Tx, uint64, string) (uint64, error)
		FailDeployedPool(context.Context, pgx.Tx, uint64) error
		UpsertPoolFromDeploy(context.Context, pgx.Tx, uint64) (uint64, error)
		LockPoolForProfileUpdate(context.Context, pgx.Tx, string) (PoolProfileOwner, error)
		UpdatePoolProfile(context.Context, pgx.Tx, string, PoolProfile) (uint64, error)
		ReplacePoolTags(context.Context, pgx.Tx, uint64, []string) error
		UpdateVoucherProfile(context.Context, pgx.Tx, string, VoucherProfile) (uint64, error)
		UpdateVoucherDisplayName(context.Context, pgx.Tx, uint64, string) error
		SetVoucherActive(context.Context, pgx.Tx, uint64, bool) error
		RestoreVoucherWithinLimits(context.Context, pgx.Tx, string) error
		RestoreVoucherWithinLimitsChanged(context.Context, pgx.Tx, string) (bool, error)
		RestorePoolWithinLimits(context.Context, pgx.Tx, string) error
		RestorePoolWithinLimitsChanged(context.Context, pgx.Tx, string) (bool, error)
		ReplaceVoucherTags(context.Context, pgx.Tx, uint64, []string) error
		ProjectCatalogOwnership(context.Context, pgx.Tx, int64, string, string, uint64, uint64) (int64, error)
		ReplaceSafeSigners(context.Context, pgx.Tx, int64, string, []string, int, uint64, uint64) (bool, error)
		GetSafeState(context.Context, pgx.Tx, int64, string) (SafeState, error)
		CatalogHasOwner(context.Context, pgx.Tx, int64, string) (bool, error)
		SafeManagesAsset(context.Context, pgx.Tx, int64, string, string) (bool, error)
		SafeExecutionsQueued(context.Context, pgx.Tx, []string) (map[string]bool, error)
		SetPoolTokenMembership(context.Context, pgx.Tx, string, string, bool) (int64, error)
		SetPoolTokenLimit(context.Context, pgx.Tx, string, string, string) (int64, error)
		SetPoolTokenOracle(context.Context, pgx.Tx, string, string, string) (int64, error)
		ClearPoolTokenOracle(context.Context, pgx.Tx, string, string) (int64, error)
		GetDeployedPoolByAddress(context.Context, pgx.Tx, string) (DeployedPool, error)
		GetDeployedPoolByTrackingID(context.Context, pgx.Tx, string) (DeployedPool, error)
		ListTakenPoolSymbols(context.Context, pgx.Tx, string) ([]string, error)
		PoolDisplayNameTaken(context.Context, pgx.Tx, string, *string) (bool, error)
		InsertDeployedPoolTag(context.Context, pgx.Tx, uint64, string) error
		GetOracleFeedByName(context.Context, pgx.Tx, int64, string) (OracleFeed, error)
		ListOracleFeeds(context.Context, pgx.Tx, int64) ([]OracleFeed, error)
		GetOTXByTrackingID(context.Context, pgx.Tx, string) ([]*OTX, error)
		GetOTXByAccount(context.Context, pgx.Tx, string, uint64) ([]*OTX, error)
		GetOTXByAccountNext(context.Context, pgx.Tx, string, uint64, uint64) ([]*OTX, error)
		GetOTXByAccountPrevious(context.Context, pgx.Tx, string, uint64, uint64) ([]*OTX, error)
		ListAdminOTX(context.Context, pgx.Tx, string, string, AdminTransactionSearch, CatalogPage, uint64, uint64) ([]AdminOTX, error)
		PeekNonce(context.Context, pgx.Tx, string) (uint64, error)
		CheckKeypair(context.Context, pgx.Tx, string) (bool, error)

		InsertUser(context.Context, pgx.Tx, User) (User, error)
		GetUserByAddress(context.Context, pgx.Tx, string) (User, error)
		GetUserByEmail(context.Context, pgx.Tx, string) (User, error)
		GetUserByID(context.Context, pgx.Tx, uint64) (User, error)
		UpdateUserStatus(context.Context, pgx.Tx, string, string) (User, error)
		ApprovePendingUserByAddress(context.Context, pgx.Tx, string) (User, error)
		UpdateUserLimits(context.Context, pgx.Tx, string, UserLimitsUpdate) (User, error)
		UpdateUserProfile(context.Context, pgx.Tx, UserProfileUpdate) (User, error)
		InsertUserShare(context.Context, pgx.Tx, UserShare) (uint64, error)
		GetUserShareByTxHash(context.Context, pgx.Tx, string, uint64) (UserShareDetails, error)
		ListUserShareTxHashes(context.Context, pgx.Tx, []string, uint64) ([]string, error)
		InsertTransferValuationSnapshots(context.Context, pgx.Tx, string, []string) ([]VoucherClaimSnapshot, error)
		ListTransferValuationSnapshotsByTxHashes(context.Context, pgx.Tx, []string) ([]TransferValuationSnapshot, error)
		ListUsersFiltered(context.Context, pgx.Tx, UserFilter, CatalogPage, uint64, uint64) ([]User, error)
		AccountRelationshipCounts(context.Context, pgx.Tx, string) (AccountRelationshipCounts, error)
		ListAccountPools(context.Context, pgx.Tx, string, CatalogPage, uint64, uint64) ([]Pool, error)
		DashboardCatalogSnapshot(context.Context, pgx.Tx, time.Time, time.Time) (DashboardCatalogSnapshot, error)
		DashboardCatalogSeries(context.Context, pgx.Tx, time.Time, time.Time, int64) ([]DashboardCatalogPoint, error)

		InsertUserDeployment(context.Context, pgx.Tx, UserDeployment) error
		CheckUserResourceLimit(context.Context, pgx.Tx, uint64, string) error
		GetUserResourceUsage(context.Context, pgx.Tx, uint64) (UserResourceUsage, error)
		LockQuotaUser(context.Context, pgx.Tx, uint64) error
		GetUserResourceUsageForVoucher(context.Context, pgx.Tx, uint64, string) (UserResourceUsage, error)
		CheckVoucherOfferingLimit(context.Context, pgx.Tx, uint64, uint64) error
		CountUserTokens(context.Context, pgx.Tx, uint64) (int, error)
		CountUserPools(context.Context, pgx.Tx, uint64) (int, error)
		ListUserDeployments(context.Context, pgx.Tx, uint64) ([]UserDeployment, error)
		GetUserDeploymentByTrackingID(context.Context, pgx.Tx, string, uint64) (UserDeployment, error)
		ListAllDeployments(context.Context, pgx.Tx) ([]UserDeploymentWithUser, error)
		ListTokensByUser(context.Context, pgx.Tx, uint64, uint64) ([]DeployedToken, error)
		ListTokensByUserNext(context.Context, pgx.Tx, uint64, uint64, uint64) ([]DeployedToken, error)
		ListTokensByUserPrevious(context.Context, pgx.Tx, uint64, uint64, uint64) ([]DeployedToken, error)
		ListOwnedVouchers(context.Context, pgx.Tx, uint64, uint64) ([]OwnedVoucher, error)
		ListOwnedVouchersNext(context.Context, pgx.Tx, uint64, uint64, uint64) ([]OwnedVoucher, error)
		ListOwnedVouchersPrevious(context.Context, pgx.Tx, uint64, uint64, uint64) ([]OwnedVoucher, error)
		ListPoolsByUser(context.Context, pgx.Tx, uint64, uint64) ([]DeployedPool, error)
		ListPoolsByUserNext(context.Context, pgx.Tx, uint64, uint64, uint64) ([]DeployedPool, error)
		ListPoolsByUserPrevious(context.Context, pgx.Tx, uint64, uint64, uint64) ([]DeployedPool, error)
		ListConfirmedPools(context.Context, pgx.Tx, uint64) ([]DeployedPool, error)
		ListConfirmedPoolsNext(context.Context, pgx.Tx, uint64, uint64) ([]DeployedPool, error)
		ListConfirmedPoolsPrevious(context.Context, pgx.Tx, uint64, uint64) ([]DeployedPool, error)
		ListAllTokens(context.Context, pgx.Tx) ([]DeployedToken, error)
		ListAllPools(context.Context, pgx.Tx) ([]DeployedPool, error)
		ListVoucherMetadataByAddresses(context.Context, pgx.Tx, []string) ([]VoucherMetadata, error)
		ListOwnedVoucherTokens(context.Context, pgx.Tx, string, CatalogPage, uint64, uint64) ([]FederationTokenBalance, error)
		ListPools(context.Context, pgx.Tx, uint64) ([]Pool, error)
		ListPoolsNext(context.Context, pgx.Tx, uint64, uint64) ([]Pool, error)
		ListPoolsPrevious(context.Context, pgx.Tx, uint64, uint64) ([]Pool, error)
		GetPoolByAddress(context.Context, pgx.Tx, string) (Pool, error)
		ListManagedPools(context.Context, pgx.Tx, CatalogFilter, string, string, CatalogPage, uint64, uint64) ([]Pool, error)
		CheckPoolOperationsAllowed(context.Context, pgx.Tx, int64, []string) error
		ListPoolVouchers(context.Context, pgx.Tx, string, uint64) ([]PoolVoucher, error)
		ListPoolVouchersNext(context.Context, pgx.Tx, string, uint64, uint64) ([]PoolVoucher, error)
		ListPoolVouchersPrevious(context.Context, pgx.Tx, string, uint64, uint64) ([]PoolVoucher, error)
		CountAdminPoolVouchers(context.Context, pgx.Tx, string) (uint64, error)
		ListAdminPoolVouchers(context.Context, pgx.Tx, string, uint64) ([]PoolVoucher, error)
		ListAdminPoolVouchersNext(context.Context, pgx.Tx, string, uint64, uint64) ([]PoolVoucher, error)
		ListAdminPoolVouchersPrevious(context.Context, pgx.Tx, string, uint64, uint64) ([]PoolVoucher, error)
		ListOfferings(context.Context, pgx.Tx, uint64) ([]Offering, error)
		ListOfferingsNext(context.Context, pgx.Tx, uint64, uint64) ([]Offering, error)
		ListOfferingsPrevious(context.Context, pgx.Tx, uint64, uint64) ([]Offering, error)
		ListVoucherOfferings(context.Context, pgx.Tx, string, uint64) ([]Offering, error)
		ListVoucherOfferingsNext(context.Context, pgx.Tx, string, uint64, uint64) ([]Offering, error)
		ListVoucherOfferingsPrevious(context.Context, pgx.Tx, string, uint64, uint64) ([]Offering, error)
		ListPoolOfferings(context.Context, pgx.Tx, string, uint64) ([]Offering, error)
		ListPoolOfferingsNext(context.Context, pgx.Tx, string, uint64, uint64) ([]Offering, error)
		ListPoolOfferingsPrevious(context.Context, pgx.Tx, string, uint64, uint64) ([]Offering, error)
		GetVoucherOffering(context.Context, pgx.Tx, string, uint64) (*Offering, error)
		GetOfferingForMutation(context.Context, pgx.Tx, uint64) (*Offering, error)
		LockVoucherForOfferingCreate(context.Context, pgx.Tx, string) (*VoucherOfferingParent, error)
		LockOfferingVoucherOwner(context.Context, pgx.Tx, uint64, string) (*OfferingParent, error)
		CreateOffering(context.Context, pgx.Tx, uint64, string, OfferingWrite) (uint64, error)
		UpdateOffering(context.Context, pgx.Tx, uint64, OfferingWrite) error
		DeleteOffering(context.Context, pgx.Tx, uint64) (uint64, error)
		ListReports(context.Context, pgx.Tx, uint64) ([]Report, error)
		ListReportsNext(context.Context, pgx.Tx, ReportCursor, uint64) ([]Report, error)
		ListReportsFiltered(context.Context, pgx.Tx, CatalogFilter, ReportCursor, bool, uint64) ([]Report, error)
		ListReportTagFacets(context.Context, pgx.Tx, CatalogFilter) ([]TagFacet, error)
		GetReport(context.Context, pgx.Tx, uint64) (*Report, error)
		GetReportForMutation(context.Context, pgx.Tx, uint64) (*Report, error)
		CreateReport(context.Context, pgx.Tx, ReportWrite, string, string) (uint64, error)
		LockReportAuthor(context.Context, pgx.Tx, uint64) (string, error)
		UpdateReport(context.Context, pgx.Tx, uint64, ReportWrite, string, *string) error

		// Post management reads. Deliberately separate from the four calls
		// above, which serve the approved-only public catalog: these return
		// every status and are authorized per caller, so sharing a signature
		// with the catalog reads is how one of them would end up serving the
		// other's audience.
		ListManagedReports(context.Context, pgx.Tx, ManagedReportFilter, ReportCursor, CatalogPage, uint64) ([]Report, error)
		GetManagedReport(context.Context, pgx.Tx, uint64, string) (*Report, error)
		ApproveReport(context.Context, pgx.Tx, uint64, string) error
		ReplaceReportRelations(context.Context, pgx.Tx, uint64, ReportWrite) error
		DeleteReport(context.Context, pgx.Tx, uint64) error
		ListMarketVouchers(context.Context, pgx.Tx, *MarketVoucherBounds, uint64) ([]MarketVoucher, error)
		ListMarketVouchersNext(context.Context, pgx.Tx, *MarketVoucherBounds, uint64, uint64) ([]MarketVoucher, error)
		ListMarketVouchersPrevious(context.Context, pgx.Tx, *MarketVoucherBounds, uint64, uint64) ([]MarketVoucher, error)
		GetMarketVoucherByAddress(context.Context, pgx.Tx, string) (MarketVoucherDetail, error)
		ListAdminVouchers(context.Context, pgx.Tx, CatalogFilter, AdminVoucherStatus, CatalogPage, uint64, uint64) ([]AdminVoucher, error)
		GetAdminVoucherByAddress(context.Context, pgx.Tx, string) (AdminVoucherDetail, error)

		// Catalog search and tag filtering.
		ListMarketVouchersFiltered(context.Context, pgx.Tx, *MarketVoucherBounds, CatalogFilter, CatalogPage, uint64, uint64) ([]MarketVoucher, error)
		ListMarketVoucherTagFacets(context.Context, pgx.Tx, *MarketVoucherBounds, CatalogFilter) ([]TagFacet, error)
		ListOfferingsFiltered(context.Context, pgx.Tx, CatalogFilter, OfferingCursor, uint64) ([]Offering, error)
		ListOfferingTagFacets(context.Context, pgx.Tx, CatalogFilter) ([]TagFacet, error)
		ListPoolsFiltered(context.Context, pgx.Tx, CatalogFilter, CatalogPage, uint64, uint64) ([]Pool, error)
		ListPoolTagFacets(context.Context, pgx.Tx, CatalogFilter) ([]TagFacet, error)
		ListPoolVoucherFacets(context.Context, pgx.Tx, CatalogFilter) ([]VoucherFacet, error)
		ListReportVoucherFacets(context.Context, pgx.Tx, CatalogFilter) ([]VoucherFacet, error)
		ListReportPoolFacets(context.Context, pgx.Tx, CatalogFilter) ([]PoolFacet, error)
		UnknownTokenTags(context.Context, pgx.Tx, []string) ([]string, error)
		ResolveVoucherRefs(context.Context, pgx.Tx, []VoucherRef) ([]uint64, error)
		ResolvePoolRefs(context.Context, pgx.Tx, []PoolRef) ([]uint64, error)
		CountVouchersOwnedBy(context.Context, pgx.Tx, []uint64, string) (int, error)
		CountPoolsOwnedBy(context.Context, pgx.Tx, []uint64, string) (int, error)
		SetCatalogStatementTimeout(context.Context, pgx.Tx) error
		ListMarketplace(context.Context, pgx.Tx, CatalogFilter, []string, *CatalogLocation, MarketplaceCursor, uint64, bool) ([]MarketplaceResult, error)
		ListMarketMap(context.Context, pgx.Tx, CatalogFilter, []string, float64, bool) (MarketMapResult, error)
		ListWants(context.Context, pgx.Tx, CatalogFilter, *MarketVoucherBounds, CatalogPage, uint64, uint64) ([]Want, error)
		ListWantTagFacets(context.Context, pgx.Tx, CatalogFilter, *MarketVoucherBounds) ([]TagFacet, error)
		ListPoolWants(context.Context, pgx.Tx, string, CatalogPage, uint64, uint64) ([]Want, error)
		ListWantCreatorVouchers(context.Context, pgx.Tx, uint64, CatalogPage, uint64, uint64) ([]WantCreatorVoucher, error)
		ListWantCreatorPools(context.Context, pgx.Tx, uint64, CatalogPage, uint64, uint64) ([]WantCreatorPool, error)
		ListOwnedWants(context.Context, pgx.Tx, uint64, uint64, uint64) ([]Want, error)
		GetWant(context.Context, pgx.Tx, uint64, *uint64) (*Want, error)
		LockWantOwner(context.Context, pgx.Tx, uint64) (uint64, error)
		CreateWant(context.Context, pgx.Tx, uint64, WantWrite) (uint64, error)
		UpdateWant(context.Context, pgx.Tx, uint64, WantWrite, bool) error
		SetWantStatus(context.Context, pgx.Tx, uint64, string) error
		DeleteWant(context.Context, pgx.Tx, uint64) error
		ReplaceWantTags(context.Context, pgx.Tx, uint64, []string) error

		SetPlatformContracts(context.Context, pgx.Tx, int64, string, string, string) (PlatformContracts, error)
		ListPlatformContracts(context.Context, pgx.Tx, int64) ([]PlatformContracts, error)
		InsertUpgradeMeta(context.Context, pgx.Tx, UpgradeMeta) error
		GetUpgradeMetaByOTXID(context.Context, pgx.Tx, uint64) (UpgradeMeta, error)

		ClaimRelayOperation(context.Context, pgx.Tx, RelayOperation) (RelayOperation, bool, error)
		RecordWebAuthnRegistration(context.Context, pgx.Tx, WebAuthnCredential) error
		GetWebAuthnCredential(context.Context, pgx.Tx, string) (WebAuthnCredential, error)
		GetActiveWebAuthnCredential(context.Context, pgx.Tx, string) (WebAuthnCredential, error)
		ListWebAuthnCredentialsByAddress(context.Context, pgx.Tx, string) ([]WebAuthnCredential, error)
		ListLegacyWebAuthnCredentials(context.Context, pgx.Tx) ([]WebAuthnCredential, error)
		ResolveLegacyWebAuthnCredential(context.Context, pgx.Tx, string, string) error
		SetNonceIfHigher(context.Context, pgx.Tx, string, uint64) error
	}
)
