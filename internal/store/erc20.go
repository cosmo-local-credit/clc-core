package store

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

type (
	PlatformContracts struct {
	ID                    uint64    `db:"id"`
	ChainID               int64     `db:"chain_id"`
	ContractType          string    `db:"contract_type"`
	FactoryAddress        string    `db:"factory_address"`
	ImplementationAddress string    `db:"implementation_address"`
	Version               string    `db:"version"`
	CreatedAt             time.Time `db:"created_at"`
	UpdatedAt             time.Time `db:"updated_at"`
}
)

func (pg *Pg) GetPlatformContracts(ctx context.Context, tx pgx.Tx, chainID int64, contractType string) (PlatformContracts, error) {
	var pc PlatformContracts

	rows, err := tx.Query(ctx, pg.queries.GetPlatformContracts, chainID, contractType)
	if err != nil {
		return pc, err
	}

	if err := pgxscan.ScanOne(&pc, rows); err != nil {
		return pc, err
	}

	return pc, nil
}

func (pg *Pg) SetPlatformContracts(ctx context.Context, tx pgx.Tx, chainID int64, contractType string, implAddress string, version string) (PlatformContracts, error) {
	var pc PlatformContracts

	rows, err := tx.Query(ctx, pg.queries.SetPlatformContracts, implAddress, version, chainID, contractType)
	if err != nil {
		return pc, err
	}

	if err := pgxscan.ScanOne(&pc, rows); err != nil {
		return pc, err
	}

	return pc, nil
}

func (pg *Pg) ListPlatformContracts(ctx context.Context, tx pgx.Tx, chainID int64) ([]PlatformContracts, error) {
	var pcs []PlatformContracts

	rows, err := tx.Query(ctx, pg.queries.ListPlatformContracts, chainID)
	if err != nil {
		return nil, err
	}

	if err := pgxscan.ScanAll(&pcs, rows); err != nil {
		return nil, err
	}

	return pcs, nil
}
