// Audit existing Voucher labels and apply an explicitly reviewed backfill.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type (
	auditRow struct {
		ChainID        int64   `json:"chainId"`
		Address        string  `json:"address"`
		ContractSymbol *string `json:"contractSymbol"`
		DisplaySymbol  *string `json:"displaySymbol"`
		Reason         string  `json:"reason"`
		Suggested      string  `json:"suggestedDisplaySymbol,omitempty"`
	}

	assignment struct {
		ChainID                int64   `json:"chainId"`
		Address                string  `json:"address"`
		ExpectedContractSymbol *string `json:"expectedContractSymbol"`
		ExpectedDisplaySymbol  *string `json:"expectedDisplaySymbol"`
		DisplaySymbol          string  `json:"displaySymbol"`
	}

	resourceKey struct {
		chainID int64
		address string
	}
)

var (
	symbolPattern  = regexp.MustCompile(`^[A-Z0-9]{3,6}$`)
	addressPattern = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	applyPath := flag.String("apply", "", "JSON file of reviewed assignments (default: read-only audit)")
	checkOnly := flag.Bool("check", false, "exit nonzero when active Voucher labels still need review")
	flag.Parse()
	if flag.NArg() != 0 || (*applyPath != "" && *checkOnly) {
		return errors.New("use read-only audit, --check, or --apply FILE")
	}
	dsn := os.Getenv("CORE_VOUCHER_SYMBOLS_DSN")
	if dsn == "" {
		return errors.New("CORE_VOUCHER_SYMBOLS_DSN is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return errors.New("could not connect using CORE_VOUCHER_SYMBOLS_DSN")
	}
	defer conn.Close(ctx)

	if *applyPath != "" {
		plan, err := readAssignments(*applyPath)
		if err != nil {
			return err
		}
		return applyAssignments(ctx, conn, plan)
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := audit(ctx, tx)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(rows); err != nil {
		return err
	}
	if *checkOnly && len(rows) != 0 {
		return fmt.Errorf("%d active Voucher labels need review", len(rows))
	}
	return tx.Commit(ctx)
}

// Count distinct owners so a Voucher's own claim and catalog record count once.
// Include the existing Pool namespace and in-flight reservations via 087.
func audit(ctx context.Context, tx pgx.Tx) ([]auditRow, error) {
	rows, err := tx.Query(ctx, `
		WITH duplicates AS (
			SELECT symbol_key FROM clc_symbol_occupancy('display_symbol')
			WHERE symbol_key IS NOT NULL AND symbol_key <> ''
			GROUP BY symbol_key HAVING count(DISTINCT (owner_kind, owner_key)) > 1
		)
		SELECT v.chain_id, v.address, v.symbol, v.display_symbol,
		       d.symbol_key IS NOT NULL
		FROM vouchers v
		LEFT JOIN duplicates d ON d.symbol_key = normalize_identity_name(COALESCE(v.display_symbol, v.symbol))
		WHERE v.active AND (v.display_symbol IS NULL OR d.symbol_key IS NOT NULL)
		ORDER BY v.chain_id, lower(v.address)`)
	if err != nil {
		return nil, fmt.Errorf("audit Voucher labels after migration 087: %w", err)
	}
	defer rows.Close()

	report := make([]auditRow, 0)
	for rows.Next() {
		var row auditRow
		var collision bool
		if err := rows.Scan(&row.ChainID, &row.Address, &row.ContractSymbol, &row.DisplaySymbol, &collision); err != nil {
			return nil, err
		}
		var canonical string
		if row.ContractSymbol != nil {
			canonical = strings.ToUpper(strings.TrimSpace(*row.ContractSymbol))
		}
		switch {
		case collision:
			row.Reason = "duplicate-or-reserved"
		case symbolPattern.MatchString(canonical):
			row.Reason = "missing"
			row.Suggested = canonical
		default:
			row.Reason = "invalid-legacy-symbol"
		}
		report = append(report, row)
	}
	return report, rows.Err()
}

func readAssignments(path string) ([]assignment, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var rows []assignment
	if err := decoder.Decode(&rows); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("assignment file must contain one JSON array")
	}
	if len(rows) == 0 {
		return nil, errors.New("assignment file must contain at least one reviewed row")
	}
	return rows, nil
}

func applyAssignments(ctx context.Context, conn *pgx.Conn, rows []assignment) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Take locks briefly during an operator-run backfill, not server startup.
	// A busy writer can cause this to fail and roll back, requiring a fresh audit.
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '3s'`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `LOCK TABLE vouchers, pools, deployed_tokens, deployed_pools, identity_claims
		IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return fmt.Errorf("lock catalog for backfill: %w", err)
	}

	review, err := audit(ctx, tx)
	if err != nil {
		return err
	}
	if err := validateAssignments(review, rows); err != nil {
		return err
	}
	for _, row := range rows {
		tag, err := tx.Exec(ctx, `UPDATE vouchers SET display_symbol = $1
			WHERE chain_id = $2 AND lower(address) = lower($3) AND active
			  AND symbol IS NOT DISTINCT FROM $4 AND display_symbol IS NOT DISTINCT FROM $5`,
			row.DisplaySymbol, row.ChainID, row.Address, row.ExpectedContractSymbol, row.ExpectedDisplaySymbol)
		if err != nil {
			return fmt.Errorf("assign label for %d:%s: %w", row.ChainID, row.Address, err)
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("voucher %d:%s changed after review", row.ChainID, row.Address)
		}
	}
	remaining, err := audit(ctx, tx)
	if err != nil {
		return err
	}
	if len(remaining) != 0 {
		return fmt.Errorf("%d active Voucher labels would still need review", len(remaining))
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Assigned %d reviewed Voucher labels.\n", len(rows))
	return nil
}

func validateAssignments(review []auditRow, rows []assignment) error {
	needed := make(map[resourceKey]auditRow, len(review))
	for _, row := range review {
		needed[resourceKey{row.ChainID, strings.ToLower(row.Address)}] = row
	}
	labels := make(map[string]bool, len(rows))
	for _, row := range rows {
		key := resourceKey{row.ChainID, strings.ToLower(row.Address)}
		original, ok := needed[key]
		if !ok || row.ChainID <= 0 || !addressPattern.MatchString(row.Address) {
			return fmt.Errorf("unexpected or repeated Voucher %d:%s", row.ChainID, row.Address)
		}
		if !symbolPattern.MatchString(row.DisplaySymbol) || labels[row.DisplaySymbol] {
			return fmt.Errorf("invalid or repeated display symbol for %d:%s", row.ChainID, row.Address)
		}
		if original.Reason == "duplicate-or-reserved" {
			legacy := original.DisplaySymbol
			if legacy == nil {
				legacy = original.ContractSymbol
			}
			if legacy != nil && strings.EqualFold(strings.TrimSpace(*legacy), row.DisplaySymbol) {
				return fmt.Errorf("give each affected Voucher a new symbol, including %d:%s", row.ChainID, row.Address)
			}
		}
		if original.Suggested != "" && original.Suggested != row.DisplaySymbol {
			return fmt.Errorf("preserve the unique existing label for %d:%s", row.ChainID, row.Address)
		}
		labels[row.DisplaySymbol] = true
		delete(needed, key)
	}
	if len(needed) != 0 {
		return fmt.Errorf("reviewed file is missing %d affected Vouchers", len(needed))
	}
	return nil
}
