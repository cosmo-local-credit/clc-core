package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/tern/v2/migrate"

	"github.com/stretchr/testify/require"
)

func TestReviewedAssignmentsPreserveUniqueLabelsAndRenameEveryDuplicate(t *testing.T) {
	const (
		a = "0x0000000000000000000000000000000000000001"
		b = "0x0000000000000000000000000000000000000002"
		c = "0x0000000000000000000000000000000000000003"
	)
	legacy := "ABC"
	unique := "DEF"
	review := []auditRow{
		{ChainID: 100, Address: a, ContractSymbol: &legacy, Reason: "duplicate-or-reserved"},
		{ChainID: 100, Address: b, ContractSymbol: &legacy, Reason: "duplicate-or-reserved"},
		{ChainID: 100, Address: c, ContractSymbol: &unique, Reason: "missing", Suggested: "DEF"},
	}
	plan := []assignment{
		{ChainID: 100, Address: a, ExpectedContractSymbol: &legacy, DisplaySymbol: "ABC1"},
		{ChainID: 100, Address: b, ExpectedContractSymbol: &legacy, DisplaySymbol: "ABC2"},
		{ChainID: 100, Address: c, ExpectedContractSymbol: &unique, DisplaySymbol: "DEF"},
	}
	require.NoError(t, validateAssignments(review, plan))
	require.ErrorContains(t, validateAssignments(review, plan[:2]), "missing 1")
	plan[0].DisplaySymbol = "ABC"
	require.ErrorContains(t, validateAssignments(review, plan), "each affected Voucher")
	plan[0].DisplaySymbol = "ABC2"
	require.ErrorContains(t, validateAssignments(review, plan), "repeated display symbol")
	plan[0].DisplaySymbol = "ABC1"
	plan[2].DisplaySymbol = "DEF2"
	require.ErrorContains(t, validateAssignments(review, plan), "preserve")
}

func TestApplyReviewedVoucherLabelsDB(t *testing.T) {
	dsn := os.Getenv("CORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CORE_TEST_DSN not set; skipping database backfill test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer admin.Close(ctx)

	name := fmt.Sprintf("clc_symbol_backfill_%d", time.Now().UnixNano())
	dbIdentifier := pgx.Identifier{name}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE DATABASE "+dbIdentifier)
	require.NoError(t, err)
	defer func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+dbIdentifier+" WITH (FORCE)")
	}()
	config, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	config.Database = name
	conn, err := pgx.ConnectConfig(ctx, config)
	require.NoError(t, err)
	defer conn.Close(ctx)
	migrator, err := migrate.NewMigrator(ctx, conn, "schema_version")
	require.NoError(t, err)
	require.NoError(t, migrator.LoadMigrations(os.DirFS("../../migrations")))
	require.NoError(t, migrator.MigrateTo(ctx, 40))

	a := "0x0000000000000000000000000000000000000001"
	b := "0x0000000000000000000000000000000000000002"
	c := "0x0000000000000000000000000000000000000003"
	legacy := "ABC"
	unique := "DEF"
	_, err = conn.Exec(ctx, `INSERT INTO vouchers (chain_id,address,symbol,active)
		VALUES (100,$1,'ABC',true), (100,$2,'ABC',true), (100,$3,'DEF',true)`, a, b, c)
	require.NoError(t, err)
	require.NoError(t, migrator.Migrate(ctx))

	plan := []assignment{
		{ChainID: 100, Address: a, ExpectedContractSymbol: &legacy, DisplaySymbol: "ABC1"},
		{ChainID: 100, Address: b, ExpectedContractSymbol: &legacy, DisplaySymbol: "ABC2"},
		{ChainID: 100, Address: c, ExpectedContractSymbol: &unique, DisplaySymbol: "DEF"},
	}
	require.ErrorContains(t, applyAssignments(ctx, conn, plan[1:]), "missing 1")
	var assigned int
	require.NoError(t, conn.QueryRow(ctx, "SELECT count(display_symbol) FROM vouchers").Scan(&assigned))
	require.Zero(t, assigned)
	require.NoError(t, applyAssignments(ctx, conn, plan))
	var native, display string
	require.NoError(t, conn.QueryRow(ctx, "SELECT symbol, display_symbol FROM vouchers WHERE address=$1", a).Scan(&native, &display))
	require.Equal(t, "ABC", native)
	require.Equal(t, "ABC1", display)
	require.NoError(t, conn.QueryRow(ctx, "SELECT count(display_symbol) FROM vouchers").Scan(&assigned))
	require.Equal(t, 3, assigned)

	// A retired record is outside the active backfill but still needs a label
	// before the catalog field can be made mandatory.
	const retired = "0x0000000000000000000000000000000000000004"
	_, err = conn.Exec(ctx, `INSERT INTO vouchers (chain_id, address, symbol, active)
		VALUES (100, $1, 'REST', false)`, retired)
	require.NoError(t, err)
	require.ErrorContains(t, enforceRequiredSymbols(ctx, conn), "1 Voucher symbol issue(s)")
	var required bool
	require.NoError(t, conn.QueryRow(ctx, `SELECT attnotnull FROM pg_attribute
		WHERE attrelid = 'vouchers'::regclass AND attname = 'display_symbol'`).Scan(&required))
	require.False(t, required)

	// A historical label may coincide with an active label. It does not
	// reserve the symbol, and therefore does not block enforcement.
	_, err = conn.Exec(ctx, "UPDATE vouchers SET display_symbol='DEF' WHERE address=$1", retired)
	require.NoError(t, err)

	reader, err := pgx.ConnectConfig(ctx, config)
	require.NoError(t, err)
	defer reader.Close(ctx)
	readTx, err := reader.Begin(ctx)
	require.NoError(t, err)
	defer readTx.Rollback(ctx)
	_, err = readTx.Exec(ctx, "LOCK TABLE vouchers IN ACCESS SHARE MODE")
	require.NoError(t, err)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, enforceRequiredSymbols(ctx, conn), &pgErr)
	require.Equal(t, "55P03", pgErr.Code, "enforcement must yield to a live Voucher read")
	require.NoError(t, readTx.Rollback(ctx))

	require.NoError(t, enforceRequiredSymbols(ctx, conn))
	require.NoError(t, enforceRequiredSymbols(ctx, conn), "rerunning the operator step is safe")
	require.NoError(t, conn.QueryRow(ctx, `SELECT attnotnull FROM pg_attribute
		WHERE attrelid = 'vouchers'::regclass AND attname = 'display_symbol'`).Scan(&required))
	require.True(t, required)

	_, err = conn.Exec(ctx, `INSERT INTO vouchers (chain_id, address, symbol)
		VALUES (100, '0x0000000000000000000000000000000000000005', 'MISS')`)
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "23502", pgErr.Code)
	_, err = conn.Exec(ctx, "UPDATE vouchers SET display_symbol=NULL WHERE address=$1", retired)
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "23502", pgErr.Code)
	_, err = conn.Exec(ctx, "UPDATE vouchers SET display_symbol=' ' WHERE address=$1", retired)
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "23514", pgErr.Code)
}
