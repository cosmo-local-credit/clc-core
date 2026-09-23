package store

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Name occupancy is what the deploy handlers consult before enqueueing, so these
// cover the predicate the 409 is decided from. They run against a live database
// and are skipped unless CORE_TEST_DSN points at one:
//
//	CORE_TEST_DSN=postgres://postgres:postgres@127.0.0.1:5432/clc_core go test ./internal/store/
//
// Every fixture lives inside the harness transaction and is rolled back.
func newNameTestPg(t *testing.T) (*Pg, pgx.Tx, func()) {
	t.Helper()

	dsn := os.Getenv("CORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CORE_TEST_DSN not set; skipping live name occupancy SQL tests")
	}

	queries, err := loadQueries("../../core_queries.sql")
	require.NoError(t, err, "load core queries")

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err, "connect")

	tx, err := pool.Begin(ctx)
	if err != nil {
		pool.Close()
		t.Fatalf("begin: %v", err)
	}

	return &Pg{queries: queries}, tx, func() {
		_ = tx.Rollback(ctx)
		pool.Close()
	}
}

func TestNormalizeDeployName(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Community Market", "community market"},
		{"  Community Market  ", "community market"},
		{"Community   Market", "community market"},
		{"COMMUNITY\tMARKET", "community market"},
		{"Community\nMarket", "community market"},
		{"   ", ""},
		{"", ""},
	} {
		assert.Equal(t, tc.want, NormalizeDeployName(tc.in), "input %q", tc.in)
	}
}

func TestVoucherDisplayNameTakenSeesCatalogAndOpenDeploys(t *testing.T) {
	pg, tx, cleanup := newNameTestPg(t)
	defer cleanup()
	ctx := context.Background()

	var catalogAddress string
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO vouchers (chain_id, address, name, display_name, symbol, decimals, active)
		VALUES (42220, '0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'),
		        'Zqname Contract', 'Zqname Catalog', 'ZQN', 6, true)
		RETURNING address`).Scan(&catalogAddress), "seed catalog voucher")

	// A retired voucher keeps its label: the address stays resolvable and reusing
	// the name would make existing references ambiguous.
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO vouchers (chain_id, address, name, symbol, decimals, active)
		VALUES (42220, '0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'),
		        'Zqname Retired', 'ZQNR', 6, false)
		RETURNING address`).Scan(new(string)), "seed retired voucher")

	platformID := platformContractID(t, tx, "ERC20")
	seedDeploy := func(name, status string) {
		t.Helper()
		otxID := seedDeployOTX(t, tx, "TOKEN_PROXY_DEPLOY")
		_, err := tx.Exec(ctx, `
			INSERT INTO deployed_tokens
			    (otx_id, platform_contract_id, status, symbol, name, decimals, admin)
			VALUES ($1, $2, $3, 'ZQN', $4, 6, '0x33c573149db22e759fB9A38Bcc461C12855c2633')`,
			otxID, platformID, status, name)
		require.NoError(t, err, "seed %s deploy %q", status, name)
	}
	seedDeploy("Zqname Pending", "PENDING")
	seedDeploy("Zqname Failed", "FAILED")

	for _, tc := range []struct {
		label string
		name  string
		want  bool
	}{
		{"catalog label", "Zqname Catalog", true},
		{"catalog label folded", "  zqname   CATALOG ", true},
		// The override shadows the contract name, so only the visible label is taken.
		{"shadowed contract name", "Zqname Contract", false},
		{"retired voucher", "Zqname Retired", true},
		{"open deploy", "Zqname Pending", true},
		// FAILED deploys are deliberately reusable, as with symbol occupancy.
		{"failed deploy", "Zqname Failed", false},
		{"unused", "Zqname Unused", false},
	} {
		taken, err := pg.VoucherDisplayNameTaken(ctx, tx, tc.name, nil)
		require.NoError(t, err, tc.label)
		assert.Equal(t, tc.want, taken, tc.label)
	}

	// Excluding a row releases its own label, so an owner can re-save it.
	taken, err := pg.VoucherDisplayNameTaken(ctx, tx, "Zqname Catalog", &catalogAddress)
	require.NoError(t, err)
	assert.False(t, taken, "a voucher must not collide with itself")

	// The exclusion is by address, not by name: it must not free somebody else's.
	taken, err = pg.VoucherDisplayNameTaken(ctx, tx, "Zqname Pending", &catalogAddress)
	require.NoError(t, err)
	assert.True(t, taken, "excluding one address must not release another row's label")
}

func TestPoolDisplayNameTakenSeesCatalogAndOpenDeploys(t *testing.T) {
	pg, tx, cleanup := newNameTestPg(t)
	defer cleanup()
	ctx := context.Background()

	var catalogAddress string
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO pools (chain_id, address, name, symbol, decimals)
		VALUES (42220, '0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'),
		        'Zqpoolname Catalog', 'ZQPN', 6)
		RETURNING address`).Scan(&catalogAddress), "seed catalog pool")

	platformID := platformContractID(t, tx, "SWAPPOOL")
	seedDeploy := func(name, status string) {
		t.Helper()
		otxID := seedDeployOTX(t, tx, "POOL_PROXY_DEPLOY")
		addr := "0x33c573149db22e759fB9A38Bcc461C12855c2633"
		_, err := tx.Exec(ctx, `
			INSERT INTO deployed_pools (
			    otx_id, platform_contract_id, status, name, symbol, decimals,
			    token_registry_address, limiter_address, fee_policy_address, quoter_address,
			    protocol_fee_controller_address, fees_decoupled, admin
			) VALUES ($1, $2, $3, $4, 'ZQPN', 6, $5, $5, $5, $5, $5, false, $5)`,
			otxID, platformID, status, name, addr)
		require.NoError(t, err, "seed %s pool deploy %q", status, name)
	}
	seedDeploy("Zqpoolname Pending", "PENDING")
	seedDeploy("Zqpoolname Failed", "FAILED")

	for _, tc := range []struct {
		label string
		name  string
		want  bool
	}{
		{"catalog label", "  ZQPOOLNAME   catalog  ", true},
		{"open deploy", "Zqpoolname Pending", true},
		{"failed deploy", "Zqpoolname Failed", false},
		{"unused", "Zqpoolname Unused", false},
	} {
		taken, err := pg.PoolDisplayNameTaken(ctx, tx, tc.name, nil)
		require.NoError(t, err, tc.label)
		assert.Equal(t, tc.want, taken, tc.label)
	}

	taken, err := pg.PoolDisplayNameTaken(ctx, tx, "Zqpoolname Catalog", &catalogAddress)
	require.NoError(t, err)
	assert.False(t, taken, "a pool must not collide with itself")
}

// Voucher and pool labels are separate namespaces, as symbols are.
func TestDisplayNameOccupancyIsPerKind(t *testing.T) {
	pg, tx, cleanup := newNameTestPg(t)
	defer cleanup()
	ctx := context.Background()

	_, err := tx.Exec(ctx, `
		INSERT INTO vouchers (chain_id, address, name, symbol, decimals, active)
		VALUES (42220, '0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'),
		        'Zqshared Label', 'ZQS', 6, true)`)
	require.NoError(t, err, "seed voucher")

	taken, err := pg.VoucherDisplayNameTaken(ctx, tx, "Zqshared Label", nil)
	require.NoError(t, err)
	assert.True(t, taken)

	taken, err = pg.PoolDisplayNameTaken(ctx, tx, "Zqshared Label", nil)
	require.NoError(t, err)
	assert.False(t, taken, "a voucher label must not occupy the pool namespace")
}
