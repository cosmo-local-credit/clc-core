package store

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/tern/v2/migrate"
	"github.com/knadh/goyesql/v2"
)

type (
	Queries struct {
		InsertKeyPair                string `query:"insert-keypair"`
		ActivateKeyPair              string `query:"activate-keypair"`
		LoadMasterKey                string `query:"load-master-key"`
		BootstrapMasterKey           string `query:"bootstrap-master-key"`
		AcquireNonce                 string `query:"acquire-nonce"`
		InsertOTX                    string `query:"insert-otx"`
		GetOTXByTxHash               string `query:"get-otx-by-tx-hash"`
		InsertDispatchTx             string `query:"insert-dispatch-tx"`
		UpdateDispatchTxStatus       string `query:"update-dispatch-tx-status"`
		GetUnsettledOTXBySigner      string `query:"get-unsettled-otx-by-signer"`
		SetOTXReplaced               string `query:"set-otx-replaced"`
		LockSigner                   string `query:"lock-signer"`
		CountUndispatchedBelowNonce  string `query:"count-undispatched-below-nonce"`
		GetPlatformContracts         string `query:"get-platform-contracts"`
		InsertDeployedToken          string `query:"insert-deployed-token"`
		ConfirmDeployedToken         string `query:"confirm-deployed-token"`
		FailDeployedToken            string `query:"fail-deployed-token"`
		UpsertVoucherFromDeploy      string `query:"upsert-voucher-from-deploy"`
		InsertDeployedTokenTag       string `query:"insert-deployed-token-tag"`
		GetDeployedTokenByAddress    string `query:"get-deployed-token-by-address"`
		GetDeployedTokenByTrackingID string `query:"get-deployed-token-by-tracking-id"`
		ListTokenTags                string `query:"list-token-tags"`
		GetProtocolAddress           string `query:"get-protocol-address"`
		SetProtocolAddress           string `query:"set-protocol-address"`
		InsertDeployedPool           string `query:"insert-deployed-pool"`
		ConfirmDeployedPool          string `query:"confirm-deployed-pool"`
		FailDeployedPool             string `query:"fail-deployed-pool"`
		UpsertPoolFromDeploy         string `query:"upsert-pool-from-deploy"`
		GetDeployedPoolByAddress     string `query:"get-deployed-pool-by-address"`
		GetDeployedPoolByTrackingID  string `query:"get-deployed-pool-by-tracking-id"`
		InsertDeployedPoolTag        string `query:"insert-deployed-pool-tag"`
		UpdatePoolProfile            string `query:"update-pool-profile"`
		DeletePoolTags               string `query:"delete-pool-tags"`
		InsertPoolTag                string `query:"insert-pool-tag"`
		UpdateVoucherProfile         string `query:"update-voucher-profile"`
		UpdateVoucherDisplayName     string `query:"update-voucher-display-name"`
		DeleteVoucherTags            string `query:"delete-voucher-tags"`
		InsertVoucherTag             string `query:"insert-voucher-tag"`
		SetPoolTokenMembership       string `query:"set-pool-token-membership"`
		SetPoolTokenLimit            string `query:"set-pool-token-limit"`
		SetPoolTokenOracle           string `query:"set-pool-token-oracle"`
		ClearPoolTokenOracle         string `query:"clear-pool-token-oracle"`
		GetOracleFeedByName          string `query:"get-oracle-feed-by-name"`
		ListOracleFeeds              string `query:"list-oracle-feeds"`
		GetOTXByTrackingID           string `query:"get-otx-by-tracking-id"`
		GetOTXByAccount              string `query:"get-otx-by-account"`
		GetOTXByAccountNext          string `query:"get-otx-by-account-next"`
		GetOTXByAccountPrev          string `query:"get-otx-by-account-prev"`
		ListOTX                      string `query:"list-otx"`
		ListOTXNext                  string `query:"list-otx-next"`
		ListOTXPrev                  string `query:"list-otx-prev"`
		PeekNonce                    string `query:"peek-nonce"`
		CheckKeypair                 string `query:"check-keypair"`

		InsertUser                    string `query:"insert-user"`
		GetUserByAddress              string `query:"get-user-by-address"`
		GetUserByEmail                string `query:"get-user-by-email"`
		GetUserByID                   string `query:"get-user-by-id"`
		UpdateUserStatus              string `query:"update-user-status"`
		ApprovePendingUserByAddress   string `query:"approve-pending-user-by-address"`
		UpdateUserLimits              string `query:"update-user-limits"`
		UpdateUserProfile             string `query:"update-user-profile"`
		InsertUserShare               string `query:"insert-user-share"`
		GetUserShareByTxHash          string `query:"get-user-share-by-tx-hash"`
		AccountRelationshipCounts     string `query:"account-relationship-counts"`
		CountUserTokens               string `query:"count-user-tokens"`
		CountUserPools                string `query:"count-user-pools"`
		InsertUserDeployment          string `query:"insert-user-deployment"`
		ListUserDeployments           string `query:"list-user-deployments"`
		GetUserDeploymentByTrackingID string `query:"get-user-deployment-by-tracking-id"`
		ListAllDeployments            string `query:"list-all-deployments"`
		ListTokensByUser              string `query:"list-tokens-by-user"`
		ListTokensByUserNext          string `query:"list-tokens-by-user-next"`
		ListTokensByUserPrev          string `query:"list-tokens-by-user-prev"`
		ListPoolsByUser               string `query:"list-pools-by-user"`
		ListPoolsByUserNext           string `query:"list-pools-by-user-next"`
		ListPoolsByUserPrev           string `query:"list-pools-by-user-prev"`
		ListConfirmedPools            string `query:"list-confirmed-pools"`
		ListConfirmedPoolsNext        string `query:"list-confirmed-pools-next"`
		ListConfirmedPoolsPrev        string `query:"list-confirmed-pools-prev"`
		ListAllTokens                 string `query:"list-all-tokens"`
		ListAllPools                  string `query:"list-all-pools"`

		SetPlatformContracts  string `query:"set-platform-contracts"`
		ListPlatformContracts string `query:"list-platform-contracts"`
		InsertUpgradeMeta     string `query:"insert-upgrade-meta"`
		GetUpgradeMetaByOTXID string `query:"get-upgrade-meta-by-otx-id"`

		UpsertWebAuthnCredential string `query:"upsert-webauthn-credential"`
		GetWebAuthnCredential    string `query:"get-webauthn-credential"`
		SetNonceIfHigher         string `query:"set-nonce-if-higher"`

		ListVoucherMetadataByAddresses string `query:"list-voucher-metadata-by-addresses"`

		ListPools                    string `query:"list-pools"`
		ListPoolsNext                string `query:"list-pools-next"`
		ListPoolsPrev                string `query:"list-pools-prev"`
		GetPoolByAddress             string `query:"get-pool-by-address"`
		ListPoolVouchers             string `query:"list-pool-vouchers"`
		ListPoolVouchersNext         string `query:"list-pool-vouchers-next"`
		ListPoolVouchersPrev         string `query:"list-pool-vouchers-prev"`
		ListOfferings                string `query:"list-offerings"`
		ListOfferingsNext            string `query:"list-offerings-next"`
		ListOfferingsPrev            string `query:"list-offerings-prev"`
		ListVoucherOfferings         string `query:"list-voucher-offerings"`
		ListVoucherOfferingsNext     string `query:"list-voucher-offerings-next"`
		ListVoucherOfferingsPrev     string `query:"list-voucher-offerings-prev"`
		ListPoolOfferings            string `query:"list-pool-offerings"`
		ListPoolOfferingsNext        string `query:"list-pool-offerings-next"`
		ListPoolOfferingsPrev        string `query:"list-pool-offerings-prev"`
		GetVoucherOffering           string `query:"get-voucher-offering"`
		GetOfferingForMutation       string `query:"get-offering-for-mutation"`
		LockVoucherForOfferingCreate string `query:"lock-voucher-for-offering-create"`
		LockOfferingVoucherOwner     string `query:"lock-offering-voucher-owner"`
		CreateOffering               string `query:"create-offering"`
		UpdateOffering               string `query:"update-offering"`
		DeleteOffering               string `query:"delete-offering"`
		ListReports                  string `query:"list-reports"`
		ListReportsNext              string `query:"list-reports-next"`
		GetReport                    string `query:"get-report"`
		GetManagedReport             string `query:"get-managed-report"`
		CreateReport                 string `query:"create-report"`
		LockReportAuthor             string `query:"lock-report-author"`
		UpdateReport                 string `query:"update-report"`
		ApproveReport                string `query:"approve-report"`
		DeleteReport                 string `query:"delete-report"`
		DeleteReportTags             string `query:"delete-report-tags"`
		InsertReportTag              string `query:"insert-report-tag"`
		DeleteReportVouchers         string `query:"delete-report-vouchers"`
		InsertReportVoucher          string `query:"insert-report-voucher"`
		DeleteReportImages           string `query:"delete-report-images"`
		InsertReportImage            string `query:"insert-report-image"`
		DeleteReportAttachments      string `query:"delete-report-attachments"`
		InsertReportAttachment       string `query:"insert-report-attachment"`

		// Bounded and unbounded market pages are separate statements so the
		// bbox predicate never hides behind a nullable OR, which would stop
		// Postgres from reliably choosing the GiST point index.
		ListMarketVouchers            string `query:"list-market-vouchers"`
		ListMarketVouchersNext        string `query:"list-market-vouchers-next"`
		ListMarketVouchersPrev        string `query:"list-market-vouchers-prev"`
		ListMarketVouchersBounded     string `query:"list-market-vouchers-bounded"`
		ListMarketVouchersNextBounded string `query:"list-market-vouchers-next-bounded"`
		ListMarketVouchersPrevBounded string `query:"list-market-vouchers-prev-bounded"`

		GetMarketVoucherByAddress string `query:"get-market-voucher-by-address"`

		// Catalog search support. The filtered voucher and pool statements are
		// composed in Go from constant fragments (internal/store/catalog_search.go)
		// rather than enumerated here: one statement per combination of entity,
		// page direction, viewport, search, and tag mode would be an
		// unmaintainable set of near-identical queries.
		UnknownTokenTags           string `query:"unknown-token-tags"`
		SetCatalogStatementTimeout string `query:"set-catalog-statement-timeout"`
		ResolveVoucherRefs         string `query:"resolve-voucher-refs"`

		// Facet vocabularies are parameterless and request-independent; only
		// the count half varies with the search, and it is composed.
		ListVoucherTagVocabulary string `query:"list-voucher-tag-vocabulary"`
		ListPoolTagVocabulary    string `query:"list-pool-tag-vocabulary"`
		ListReportTagVocabulary  string `query:"list-report-tag-vocabulary"`

		DashboardCatalogSnapshot string `query:"dashboard-catalog-snapshot"`
		DashboardCatalogSeries   string `query:"dashboard-catalog-series"`
	}

	PgOpts struct {
		Logg                 *slog.Logger
		DSN                  string
		MigrationsFolderPath string
		QueriesFolderPath    string
	}

	Pg struct {
		logg    *slog.Logger
		db      *pgxpool.Pool
		queries *Queries
	}
)

func NewPgStore(o PgOpts) (Store, error) {
	parsedConfig, err := pgxpool.ParseConfig(o.DSN)
	if err != nil {
		return nil, err
	}

	dbPool, err := pgxpool.NewWithConfig(context.Background(), parsedConfig)
	if err != nil {
		return nil, err
	}

	queries, err := loadQueries(o.QueriesFolderPath)
	if err != nil {
		return nil, err
	}

	if err := runMigrations(dbPool, o.MigrationsFolderPath); err != nil {
		return nil, err
	}
	o.Logg.Info("migrations ran successfully")

	return &Pg{
		logg:    o.Logg,
		db:      dbPool,
		queries: queries,
	}, nil
}

func (s *Pg) Bootstrap() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.db.Ping(ctx); err != nil {
		s.db.Close()
		return err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		} else {
			_ = tx.Commit(ctx)
		}
	}()

	return s.bootstrapMasterSigner(ctx, tx)
}

func (s *Pg) Pool() *pgxpool.Pool {
	return s.db
}

func loadQueries(queriesPath string) (*Queries, error) {
	parsedQueries, err := goyesql.ParseFile(queriesPath)
	if err != nil {
		return nil, err
	}

	loadedQueries := &Queries{}

	if err := goyesql.ScanToStruct(loadedQueries, parsedQueries, nil); err != nil {
		return nil, fmt.Errorf("failed to scan queries: %w", err)
	}

	return loadedQueries, nil
}

func runMigrations(dbPool *pgxpool.Pool, migrationsPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, err := dbPool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	migrator, err := migrate.NewMigrator(ctx, conn.Conn(), "schema_version")
	if err != nil {
		return err
	}

	if err := migrator.LoadMigrations(os.DirFS(migrationsPath)); err != nil {
		return err
	}

	if err := migrator.Migrate(ctx); err != nil {
		return err
	}

	return nil
}
