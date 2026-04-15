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
	CheckKeypair(context.Context, pgx.Tx, string) (bool, error)
	LoadPrivateKey(context.Context, pgx.Tx, string) (keypair.Key, error)
	LoadMasterSignerKey(context.Context, pgx.Tx) (keypair.Key, error)
	PeekNonce(context.Context, pgx.Tx, string) (uint64, error)
	AcquireNonce(context.Context, pgx.Tx, string) (uint64, error)
	SetAccountNonce(context.Context, pgx.Tx, string, uint64) error
	InsertOTX(context.Context, pgx.Tx, OTX) (uint64, error)
	GetOTXByTxHash(context.Context, pgx.Tx, string) (OTX, error)
	GetOTXByTrackingID(context.Context, pgx.Tx, string) ([]*OTX, error)
	GetOTXByAccount(context.Context, pgx.Tx, string, int) ([]*OTX, error)
	GetOTXByAccountNext(context.Context, pgx.Tx, string, int, int) ([]*OTX, error)
	GetOTXByAccountPrevious(context.Context, pgx.Tx, string, int, int) ([]*OTX, error)
	GetFailedOTX(context.Context, pgx.Tx) ([]*OTX, error)
	InsertDispatchTx(context.Context, pgx.Tx, DispatchTx) error
	UpdateDispatchTxStatus(context.Context, pgx.Tx, DispatchTx) error
	InsertNextERC20Workflow(context.Context, pgx.Tx, NextERC20Workflow) error
	GetNextERC20WorkflowByTrackingID(context.Context, pgx.Tx, string) (NextERC20Workflow, error)
	GetLatestNextERC20WorkflowByProxy(context.Context, pgx.Tx, string) (NextERC20Workflow, error)
	GetLatestSuccessfulNextERC20WorkflowByProxy(context.Context, pgx.Tx, string) (NextERC20Workflow, error)
	UpdateNextERC20Workflow(context.Context, pgx.Tx, NextERC20WorkflowUpdate) error
}
