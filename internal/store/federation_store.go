package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/knadh/goyesql/v2"
)

type (
	FederationQueries struct {
		LegacyUserExistsByBlockchainAddress string `query:"legacy-user-exists-by-blockchain-address"`
		ListUserTokenHoldings               string `query:"list-user-token-holdings"`
		ListUserTokenHoldingsNext           string `query:"list-user-token-holdings-next"`
		ListUserTokenHoldingsPrevious       string `query:"list-user-token-holdings-prev"`
		ListUserTokenBalances               string `query:"list-user-token-balances"`
		AccountActivityCounts               string `query:"account-activity-counts"`
		ListUserTransferHistory             string `query:"list-user-transfer-history"`
		ListUserTransferHistoryNext         string `query:"list-user-transfer-history-next"`
		ListUserTransferHistoryPrevious     string `query:"list-user-transfer-history-prev"`
		DashboardTransactionSeries          string `query:"dashboard-transaction-series"`
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
		ListUserTokenBalances(context.Context, string, []string) ([]FederationTokenBalance, error)
		AccountActivityCounts(context.Context, string) (AccountActivityCounts, error)
		ListUserTransferHistory(context.Context, string, uint64) ([]FederationTransfer, error)
		ListUserTransferHistoryNext(context.Context, string, uint64, uint64) ([]FederationTransfer, error)
		ListUserTransferHistoryPrevious(context.Context, string, uint64, uint64) ([]FederationTransfer, error)
		DashboardTransactionSeries(context.Context, time.Time, time.Time, int64) ([]DashboardTransactionPoint, error)
		ListAdminVoucherTransactions(context.Context, *uint64, uint64, bool, string, string, AdminTransactionSearch) ([]AdminVoucherTransaction, error)
		ListAdminPoolTransactions(context.Context, *uint64, uint64, bool, string, string, AdminTransactionSearch) ([]AdminPoolTransaction, error)
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

// LegacyUserExistsByBlockchainAddress reports whether the address belongs to a
// legacy Sarafu account. The sarafu_network schema only exists on deployments
// that carry that history; where the table is absent nobody is a legacy user,
// so undefined_table is answered with false rather than surfaced as an error.
func (s *FederationPg) LegacyUserExistsByBlockchainAddress(ctx context.Context, address string) (bool, error) {
	var exists bool
	if err := s.db.QueryRow(ctx, s.queries.LegacyUserExistsByBlockchainAddress, address).Scan(&exists); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UndefinedTable {
			return false, nil
		}
		return false, err
	}
	return exists, nil
}

func (s *FederationPg) Close() {
	s.db.Close()
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
