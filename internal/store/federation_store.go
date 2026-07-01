package store

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/knadh/goyesql/v2"
)

type (
	FederationQueries struct {
		LegacyUserExistsByBlockchainAddress string `query:"legacy-user-exists-by-blockchain-address"`
		ListUserTokenHoldings               string `query:"list-user-token-holdings"`
		ListUserTokenHoldingsNext           string `query:"list-user-token-holdings-next"`
		ListUserTokenHoldingsPrevious       string `query:"list-user-token-holdings-prev"`
		ListUserTransferHistory             string `query:"list-user-transfer-history"`
		ListUserTransferHistoryNext         string `query:"list-user-transfer-history-next"`
		ListUserTransferHistoryPrevious     string `query:"list-user-transfer-history-prev"`

		CreateAccountTokenHoldingMV              string `query:"create-account-token-holding-mv"`
		CreateAccountTokenHoldingHolderTokenIdx  string `query:"create-account-token-holding-holder-token-idx"`
		CreateAccountTokenHoldingHolderLastTxIdx string `query:"create-account-token-holding-holder-last-tx-idx"`
		CreateAccountTransferMV                  string `query:"create-account-transfer-mv"`
		CreateAccountTransferHolderTransferIdx   string `query:"create-account-transfer-holder-transfer-idx"`
		RefreshAccountTokenHoldingMV             string `query:"refresh-account-token-holding-mv"`
		RefreshAccountTransferMV                 string `query:"refresh-account-transfer-mv"`
	}

	FederationStoreOpts struct {
		Logg              *slog.Logger
		DSN               string
		QueriesFolderPath string
	}

	FederationStore interface {
		Pool() *pgxpool.Pool
		LegacyUserExistsByBlockchainAddress(context.Context, string) (bool, error)
		ListUserTokenHoldings(context.Context, string, uint64) ([]FederationTokenBalance, error)
		ListUserTokenHoldingsNext(context.Context, string, uint64, uint64) ([]FederationTokenBalance, error)
		ListUserTokenHoldingsPrevious(context.Context, string, uint64, uint64) ([]FederationTokenBalance, error)
		ListUserTransferHistory(context.Context, string, uint64) ([]FederationTransfer, error)
		ListUserTransferHistoryNext(context.Context, string, uint64, uint64) ([]FederationTransfer, error)
		ListUserTransferHistoryPrevious(context.Context, string, uint64, uint64) ([]FederationTransfer, error)
		Bootstrap(context.Context) error
		StartMaintenance(context.Context)
		Close()
	}

	FederationPg struct {
		logg    *slog.Logger
		db      *pgxpool.Pool
		queries *FederationQueries
	}
)

func NewFederationStore(o FederationStoreOpts) (FederationStore, error) {
	parsedConfig, err := pgxpool.ParseConfig(o.DSN)
	if err != nil {
		return nil, err
	}

	dbPool, err := pgxpool.NewWithConfig(context.Background(), parsedConfig)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := dbPool.Ping(ctx); err != nil {
		dbPool.Close()
		return nil, err
	}

	queries, err := loadFederationQueries(o.QueriesFolderPath)
	if err != nil {
		return nil, err
	}

	o.Logg.Info("federation DB pool initialized successfully")

	return &FederationPg{
		logg:    o.Logg,
		db:      dbPool,
		queries: queries,
	}, nil
}

func (s *FederationPg) Pool() *pgxpool.Pool {
	return s.db
}

func (s *FederationPg) LegacyUserExistsByBlockchainAddress(ctx context.Context, address string) (bool, error) {
	var exists bool
	if err := s.db.QueryRow(ctx, s.queries.LegacyUserExistsByBlockchainAddress, address).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

func (s *FederationPg) Close() {
	s.db.Close()
}

// Bootstrap creates the account_token_holding and account_transfer materialized
// views and their indexes on the federation database. The statements are
// idempotent (IF NOT EXISTS) and run outside a transaction via pool.Exec —
// CREATE MATERIALIZED VIEW cannot be wrapped in a transaction when it depends
// on a concurrently-built structure, and REFRESH ... CONCURRENTLY is never
// transactional. On first creation the views are populated immediately; on
// subsequent restarts the DDL is a no-op and the maintenance loop keeps them
// fresh. A failure here is logged by the caller rather than fatal so that the
// legacy-user-approval path (which does not use the views) keeps working.
func (s *FederationPg) Bootstrap(ctx context.Context) error {
	ddls := []string{
		s.queries.CreateAccountTokenHoldingMV,
		s.queries.CreateAccountTokenHoldingHolderTokenIdx,
		s.queries.CreateAccountTokenHoldingHolderLastTxIdx,
		s.queries.CreateAccountTransferMV,
		s.queries.CreateAccountTransferHolderTransferIdx,
	}
	for _, ddl := range ddls {
		if _, err := s.db.Exec(ctx, ddl); err != nil {
			return fmt.Errorf("federation bootstrap DDL: %w", err)
		}
	}
	s.logg.Info("federation materialized views ready")
	return nil
}

// StartMaintenance periodically refreshes the materialized views so balance and
// transfer reads stay eventually consistent with the indexer. It refreshes once
// immediately on startup (covers the restart-with-existing-views case) then on
// a fixed interval until ctx is cancelled. REFRESH MATERIALIZED VIEW
// CONCURRENTLY does not block readers but requires the unique indexes created
// by Bootstrap.
func (s *FederationPg) StartMaintenance(ctx context.Context) {
	const (
		refreshInterval = 2 * time.Minute
		refreshTimeout  = 5 * time.Minute
	)

	if err := s.refreshViews(ctx, refreshTimeout); err != nil {
		s.logg.Warn("federation materialized view initial refresh failed", "error", err)
	}

	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.refreshViews(ctx, refreshTimeout); err != nil {
				s.logg.Warn("federation materialized view refresh failed", "error", err)
			}
		}
	}
}

func (s *FederationPg) refreshViews(ctx context.Context, timeout time.Duration) error {
	refreshCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if _, err := s.db.Exec(refreshCtx, s.queries.RefreshAccountTokenHoldingMV); err != nil {
		return fmt.Errorf("refresh account_token_holding: %w", err)
	}
	if _, err := s.db.Exec(refreshCtx, s.queries.RefreshAccountTransferMV); err != nil {
		return fmt.Errorf("refresh account_transfer: %w", err)
	}
	return nil
}

func loadFederationQueries(queriesPath string) (*FederationQueries, error) {
	parsedQueries, err := goyesql.ParseFile(queriesPath)
	if err != nil {
		return nil, err
	}

	loaded := &FederationQueries{}
	if err := goyesql.ScanToStruct(loaded, parsedQueries, nil); err != nil {
		return nil, fmt.Errorf("failed to scan federation queries: %w", err)
	}
	return loaded, nil
}
