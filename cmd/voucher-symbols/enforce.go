package main

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Keep enforcement out of startup migrations so an unbackfilled deployment can
// start. An operator must use the explicit flag after both environments pass.
func enforceRequiredSymbols(ctx context.Context, conn *pgx.Conn) error {
	// The audit must see transactions that committed while we waited for locks.
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '3s'`); err != nil {
		return err
	}
	// Acquire the ALTER's final lock upfront. NOWAIT keeps a busy request
	// from deadlocking against a late lock upgrade or a held claims lock.
	if _, err := tx.Exec(ctx, `LOCK TABLE vouchers IN ACCESS EXCLUSIVE MODE NOWAIT`); err != nil {
		return fmt.Errorf("lock Vouchers for required symbol check: %w", err)
	}
	// Also serialize with claims and deployment processing. If either is
	// busy, abort the operator transaction instead of queuing behind it.
	if _, err := tx.Exec(ctx, `LOCK TABLE pools, deployed_tokens, deployed_pools, identity_claims, otx
		IN SHARE ROW EXCLUSIVE MODE NOWAIT`); err != nil {
		return fmt.Errorf("lock catalog for required symbol check: %w", err)
	}
	var hasQueue bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('river_job') IS NOT NULL`).Scan(&hasQueue); err != nil {
		return err
	}
	if hasQueue {
		if _, err := tx.Exec(ctx, `LOCK TABLE river_job IN SHARE ROW EXCLUSIVE MODE NOWAIT`); err != nil {
			return fmt.Errorf("lock deployment queue for required symbol check: %w", err)
		}
	}

	issues, err := auditRequired(ctx, tx)
	if err != nil {
		return err
	}
	if len(issues) != 0 {
		return fmt.Errorf("%d Voucher symbol issue(s) block enforcement; run --check-required for details", len(issues))
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE vouchers ALTER COLUMN display_symbol SET NOT NULL`); err != nil {
		return fmt.Errorf("require Voucher display symbols: %w", err)
	}
	return tx.Commit(ctx)
}
