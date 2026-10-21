package store

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
)

// ProjectCatalogOwnership advances the position and catalog atomically. A
// dependency event is recorded but only an emitting voucher/SwapPool address
// changes a catalog owner. Late deployment promotion consults this same fact.
// The owner is stored checksummed, like the deploy pipeline's.
//
// Its SQL is inline rather than in core_queries.sql because
// cmd/reconcile-ownership calls it on a bare Pg, which loads no query file and
// runs no migrations.
func (pg *Pg) ProjectCatalogOwnership(ctx context.Context, tx pgx.Tx, chainID int64, address, owner string, block, index uint64) (int64, error) {
	if chainID <= 0 || !common.IsHexAddress(address) || !common.IsHexAddress(owner) || block > math.MaxInt64 || index > math.MaxInt64 {
		return 0, fmt.Errorf("invalid catalog ownership position or address")
	}
	if err := lockCatalogOwnership(ctx, tx, chainID, address); err != nil {
		return 0, err
	}
	var rows int64
	err := tx.QueryRow(ctx, `
WITH advanced AS (
    INSERT INTO catalog_ownership (chain_id, address, owner_address, block_number, log_index)
    VALUES ($1, lower($2), $3, $4, $5)
    ON CONFLICT (chain_id, address) DO UPDATE SET
        owner_address = EXCLUDED.owner_address,
        block_number = EXCLUDED.block_number,
        log_index = EXCLUDED.log_index,
        updated_at = now()
    WHERE (catalog_ownership.block_number, catalog_ownership.log_index)
        < (EXCLUDED.block_number, EXCLUDED.log_index)
    RETURNING chain_id, address, owner_address
), vouchers_updated AS (
    UPDATE vouchers v SET owner_address = a.owner_address, updated_at = now()
    FROM advanced a WHERE v.chain_id = a.chain_id AND lower(v.address) = a.address
    RETURNING v.id
), pools_updated AS (
    UPDATE pools p SET owner_address = a.owner_address, updated_at = now()
    FROM advanced a WHERE p.chain_id = a.chain_id AND lower(p.address) = a.address
    RETURNING p.id
)
SELECT (SELECT count(*) FROM vouchers_updated) + (SELECT count(*) FROM pools_updated)`,
		chainID, strings.ToLower(address), common.HexToAddress(owner).Hex(), int64(block), int64(index)).Scan(&rows)
	return rows, err
}

// Serialize event projection with deployment promotion, including when no
// catalog row exists yet. Separate statements get a fresh READ COMMITTED
// snapshot after waiting for the lock.
func lockCatalogOwnership(ctx context.Context, tx pgx.Tx, chainID int64, address string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, fmt.Sprintf("ownership:%d:%s", chainID, strings.ToLower(address)))
	return err
}

func lockDeploymentOwnership(ctx context.Context, tx pgx.Tx, table string, id uint64) error {
	if table != "deployed_tokens" && table != "deployed_pools" {
		return fmt.Errorf("invalid deployment table")
	}
	var chainID int64
	var address string
	err := tx.QueryRow(ctx, `SELECT pc.chain_id, d.address FROM `+table+` d
        JOIN platform_contracts pc ON pc.id = d.platform_contract_id
        WHERE d.id = $1 AND d.address IS NOT NULL`, id).Scan(&chainID, &address)
	if err != nil {
		return err
	}
	return lockCatalogOwnership(ctx, tx, chainID, address)
}
