package store

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

const (
	NextERC20ActionDeploy  = "DEPLOY"
	NextERC20ActionUpgrade = "UPGRADE"

	NextERC20StatusPending = "PENDING"
	NextERC20StatusRunning = "RUNNING"
	NextERC20StatusSuccess = "SUCCESS"
	NextERC20StatusFailed  = "FAILED"
)

type NextERC20Workflow struct {
	ID                            uint64    `db:"id" json:"id"`
	TrackingID                    string    `db:"tracking_id" json:"trackingId"`
	Action                        string    `db:"action" json:"action"`
	Status                        string    `db:"status" json:"status"`
	Name                          string    `db:"name" json:"name"`
	Symbol                        string    `db:"symbol" json:"symbol"`
	Decimals                      int       `db:"decimals" json:"decimals"`
	InitialSupply                 int64     `db:"initial_supply" json:"initialSupply"`
	InitialMintee                 string    `db:"initial_mintee" json:"initialMintee"`
	Owner                         string    `db:"owner" json:"owner"`
	ExpiryTimestamp               int64     `db:"expiry_timestamp" json:"expiryTimestamp"`
	FactoryAddress                string    `db:"factory_address" json:"factoryAddress"`
	AdminAddress                  string    `db:"admin_address" json:"adminAddress"`
	ProxyAddress                  string    `db:"proxy_address" json:"proxyAddress"`
	ImplementationAddress         string    `db:"implementation_address" json:"implementationAddress"`
	PreviousImplementationAddress string    `db:"previous_implementation_address" json:"previousImplementationAddress"`
	Version                       int       `db:"version" json:"version"`
	UpgradeCalldata               string    `db:"upgrade_calldata" json:"upgradeCalldata"`
	ImplementationTxHash          string    `db:"implementation_tx_hash" json:"implementationTxHash"`
	ActionTxHash                  string    `db:"action_tx_hash" json:"actionTxHash"`
	CreatedAt                     time.Time `db:"created_at" json:"createdAt"`
	UpdatedAt                     time.Time `db:"updated_at" json:"updatedAt"`
}

type NextERC20WorkflowUpdate struct {
	TrackingID                    string
	Status                        string
	ProxyAddress                  string
	ImplementationAddress         string
	PreviousImplementationAddress string
	Version                       int
	ImplementationTxHash          string
	ActionTxHash                  string
}

func (pg *Pg) InsertNextERC20Workflow(ctx context.Context, tx pgx.Tx, workflow NextERC20Workflow) error {
	_, err := tx.Exec(
		ctx,
		pg.queries.InsertNextERC20Workflow,
		workflow.TrackingID,
		workflow.Action,
		workflow.Status,
		workflow.Name,
		workflow.Symbol,
		workflow.Decimals,
		workflow.InitialSupply,
		workflow.InitialMintee,
		workflow.Owner,
		workflow.ExpiryTimestamp,
		workflow.FactoryAddress,
		workflow.AdminAddress,
		workflow.ProxyAddress,
		workflow.ImplementationAddress,
		workflow.PreviousImplementationAddress,
		workflow.Version,
		workflow.UpgradeCalldata,
		workflow.ImplementationTxHash,
		workflow.ActionTxHash,
	)
	return err
}

func (pg *Pg) GetNextERC20WorkflowByTrackingID(ctx context.Context, tx pgx.Tx, trackingID string) (NextERC20Workflow, error) {
	var workflow NextERC20Workflow

	rows, err := tx.Query(ctx, pg.queries.GetNextERC20ByTrackingID, trackingID)
	if err != nil {
		return workflow, err
	}

	if err := pgxscan.ScanOne(&workflow, rows); err != nil {
		return workflow, err
	}

	return workflow, nil
}

func (pg *Pg) GetLatestNextERC20WorkflowBySymbol(ctx context.Context, tx pgx.Tx, symbol string) (NextERC20Workflow, error) {
	var workflow NextERC20Workflow

	rows, err := tx.Query(ctx, pg.queries.GetLatestNextERC20BySymbol, symbol)
	if err != nil {
		return workflow, err
	}

	if err := pgxscan.ScanOne(&workflow, rows); err != nil {
		return workflow, err
	}

	return workflow, nil
}

func (pg *Pg) GetLatestNextERC20WorkflowByProxy(ctx context.Context, tx pgx.Tx, proxyAddress string) (NextERC20Workflow, error) {
	var workflow NextERC20Workflow

	rows, err := tx.Query(ctx, pg.queries.GetLatestNextERC20ByProxy, proxyAddress)
	if err != nil {
		return workflow, err
	}

	if err := pgxscan.ScanOne(&workflow, rows); err != nil {
		return workflow, err
	}

	return workflow, nil
}

func (pg *Pg) GetLatestSuccessfulNextERC20WorkflowByProxy(ctx context.Context, tx pgx.Tx, proxyAddress string) (NextERC20Workflow, error) {
	var workflow NextERC20Workflow

	rows, err := tx.Query(ctx, pg.queries.GetLatestSuccessfulNextERC20ByProxy, proxyAddress)
	if err != nil {
		return workflow, err
	}

	if err := pgxscan.ScanOne(&workflow, rows); err != nil {
		return workflow, err
	}

	return workflow, nil
}

func (pg *Pg) UpdateNextERC20Workflow(ctx context.Context, tx pgx.Tx, workflow NextERC20WorkflowUpdate) error {
	_, err := tx.Exec(
		ctx,
		pg.queries.UpdateNextERC20Workflow,
		workflow.TrackingID,
		workflow.Status,
		workflow.ProxyAddress,
		workflow.ImplementationAddress,
		workflow.PreviousImplementationAddress,
		workflow.Version,
		workflow.ImplementationTxHash,
		workflow.ActionTxHash,
	)
	return err
}
