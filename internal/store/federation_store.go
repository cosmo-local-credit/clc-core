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
	}

	FederationStoreOpts struct {
		Logg              *slog.Logger
		DSN               string
		QueriesFolderPath string
	}

	FederationStore interface {
		Pool() *pgxpool.Pool
		LegacyUserExistsByBlockchainAddress(context.Context, string) (bool, error)
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
