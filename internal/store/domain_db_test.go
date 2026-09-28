package store

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/tern/v2/migrate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests use the existing rolled-back DB harness. The migration round-trip
// test uses its own disposable database because older migrations address public
// explicitly; a search_path-only sandbox would not isolate those migrations.
func TestDomainFoundationSeedAndHostReadiness(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()

	global, err := pg.GetGlobalDomain(ctx, tx)
	require.NoError(t, err)
	assert.Equal(t, "sarafu", global.Slug)
	assert.True(t, global.IsGlobal)
	byID, err := pg.GetDomainByID(ctx, tx, global.ID)
	require.NoError(t, err)
	assert.Equal(t, global, byID)
	_, err = tx.Exec(ctx, `UPDATE domains SET slug = 'sarafu-renamed' WHERE id = $1`, global.ID)
	require.NoError(t, err, "the global designation is independent of its editable slug")
	_, err = tx.Exec(ctx, `INSERT INTO domains (slug, name) VALUES ('sarafu', 'Reused slug')`)
	require.NoError(t, err, "an old slug can be reused without changing global identity")

	seedSQL, err := os.ReadFile("../../migrations/039_domain_foundation.sql")
	require.NoError(t, err)
	_, seed, found := strings.Cut(string(seedSQL), "-- BEGIN SARAFU SEED:")
	require.True(t, found)
	// Discard the remainder of the marker's comment line.
	_, seed, found = strings.Cut(seed, "\n")
	require.True(t, found)
	seed, _, found = strings.Cut(seed, "-- END SARAFU SEED")
	require.True(t, found)
	_, err = tx.Exec(ctx, seed)
	require.NoError(t, err)
	again, err := pg.GetGlobalDomain(ctx, tx)
	require.NoError(t, err)
	assert.Equal(t, global.ID, again.ID)
	assert.Equal(t, "sarafu-renamed", again.Slug, "replaying the seed preserves an edited slug")
	var hostCount int
	require.NoError(t, tx.QueryRow(ctx, `SELECT count(*) FROM domain_hosts WHERE hostname = 'sarafu.network'`).Scan(&hostCount))
	assert.Equal(t, 1, hostCount)

	_, err = pg.GetActiveDomainByHostname(ctx, tx, "sarafu.network")
	require.ErrorIs(t, err, pgx.ErrNoRows, "seeded DNS is not verified DNS")
	expectDomainSQLState(t, tx, "23514", `UPDATE domain_hosts SET status = 'active' WHERE hostname = 'sarafu.network'`)
	_, err = tx.Exec(ctx, `UPDATE domain_hosts SET verified_at = CURRENT_TIMESTAMP, status = 'active' WHERE hostname = 'sarafu.network'`)
	require.NoError(t, err)
	resolved, err := pg.GetActiveDomainByHostname(ctx, tx, "sarafu.network")
	require.NoError(t, err)
	assert.Equal(t, global.ID, resolved.ID)
	_, err = pg.GetActiveDomainByHostname(ctx, tx, "unknown.example")
	require.ErrorIs(t, err, pgx.ErrNoRows)

	expectDomainSQLState(t, tx, "23505", `INSERT INTO domains (slug, name, is_global) VALUES ('second-global', 'Other', true)`)
	for _, statement := range []string{
		`UPDATE domains SET status = 'archived' WHERE is_global`,
		`UPDATE domains SET is_global = false WHERE is_global`,
		`DELETE FROM domains WHERE is_global`,
	} {
		expectDomainSQLState(t, tx, "23514", statement)
	}
}

func TestDomainFoundationHostConstraints(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()
	domainID := insertDomainFixture(t, tx, "kosta")
	otherID := insertDomainFixture(t, tx, "ana")

	for _, hostname := range []string{"Kosta.example", "https://kosta.example", "kosta.example/", "kosta.example.", "-kosta.example", "kosta..example", "kosta.example:443", "*.example"} {
		expectDomainSQLState(t, tx, "23514", `INSERT INTO domain_hosts (domain_id, hostname) VALUES ($1, $2)`, domainID, hostname)
	}
	_, err := tx.Exec(ctx, `INSERT INTO domain_hosts (domain_id, hostname, status, verified_at)
		VALUES ($1, 'kosta.example', 'active', CURRENT_TIMESTAMP)`, domainID)
	require.NoError(t, err)
	expectDomainSQLState(t, tx, "23505", `INSERT INTO domain_hosts (domain_id, hostname) VALUES ($1, 'kosta.example')`, otherID)
	expectDomainSQLState(t, tx, "23505", `INSERT INTO domain_hosts (domain_id, hostname, status, verified_at)
		VALUES ($1, 'second.example', 'active', CURRENT_TIMESTAMP)`, domainID)
	expectDomainSQLState(t, tx, "23514", `UPDATE domain_hosts SET domain_id = $1 WHERE hostname = 'kosta.example'`, otherID)
	expectDomainSQLState(t, tx, "23514", `UPDATE domain_hosts SET hostname = 'changed.example' WHERE hostname = 'kosta.example'`)
	// Rename the space without changing its address or the stable identity.
	_, err = tx.Exec(ctx, `UPDATE domains SET slug = 'renamed-kosta' WHERE id = $1`, domainID)
	require.NoError(t, err)
	alias, err := pg.GetActiveDomainByHostname(ctx, tx, "kosta.example")
	require.NoError(t, err)
	assert.Equal(t, domainID, alias.ID)
	assert.Equal(t, "renamed-kosta", alias.Slug)
	expectDomainSQLState(t, tx, "23505", `UPDATE domains SET slug = 'renamed-kosta' WHERE id = $1`, otherID)
	expectDomainSQLState(t, tx, "23514", `UPDATE domains SET slug = 'Invalid Slug' WHERE id = $1`, domainID)
	_, err = tx.Exec(ctx, `UPDATE domain_hosts SET status = 'disabled' WHERE hostname = 'kosta.example'`)
	require.NoError(t, err)
	_, err = pg.GetActiveDomainByHostname(ctx, tx, "kosta.example")
	require.ErrorIs(t, err, pgx.ErrNoRows)
	_, err = tx.Exec(ctx, `UPDATE domain_hosts SET status = 'active' WHERE hostname = 'kosta.example'`)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `UPDATE domains SET status = 'archived' WHERE id = $1`, domainID)
	require.NoError(t, err)
	_, err = pg.GetActiveDomainByHostname(ctx, tx, "kosta.example")
	require.ErrorIs(t, err, pgx.ErrNoRows)
}

func TestDomainFoundationMembershipAndRegistrationConstraints(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()
	user, err := pg.InsertUser(ctx, tx, User{
		EthereumAddress: randomAddress(t, tx), Name: "Domain test", Email: uuid.NewString() + "@example.test",
		Role: "USER", Status: "APPROVED", MaxPools: 10, MaxTokens: 20,
	})
	require.NoError(t, err)
	state, err := pg.GetUserDomainState(ctx, tx, user.ID)
	require.NoError(t, err)
	assert.Nil(t, state.RegistrationDomainID)
	assert.Zero(t, state.DomainRevision)
	domainID := insertDomainFixture(t, tx, "kosta")
	otherID := insertDomainFixture(t, tx, "ana")
	_, err = tx.Exec(ctx, `INSERT INTO user_domains (user_id, domain_id) VALUES ($1, $2), ($1, $3)`, user.ID, domainID, otherID)
	require.NoError(t, err, "one account may belong to multiple spaces")
	expectDomainSQLState(t, tx, "23505", `INSERT INTO user_domains (user_id, domain_id) VALUES ($1, $2)`, user.ID, domainID)
	expectDomainSQLState(t, tx, "23503", `INSERT INTO user_domains (user_id, domain_id) VALUES ($1, -1)`, user.ID)
	expectDomainSQLState(t, tx, "23514", `UPDATE user_domains SET status = 'unknown' WHERE user_id = $1`, user.ID)
	expectDomainSQLState(t, tx, "23514", `UPDATE user_domains SET status = 'left' WHERE user_id = $1`, user.ID)
	expectDomainSQLState(t, tx, "23514", `UPDATE users SET domain_revision = -1 WHERE id = $1`, user.ID)
	_, err = tx.Exec(ctx, `UPDATE users SET registration_domain_id = $2, domain_revision = 1 WHERE id = $1`, user.ID, domainID)
	require.NoError(t, err)
	state, err = pg.GetUserDomainState(ctx, tx, user.ID)
	require.NoError(t, err)
	require.NotNil(t, state.RegistrationDomainID)
	assert.Equal(t, domainID, *state.RegistrationDomainID)
	assert.EqualValues(t, 1, state.DomainRevision)
	expectDomainSQLState(t, tx, "23514", `UPDATE users SET registration_domain_id = $2 WHERE id = $1`, user.ID, otherID)
	expectDomainSQLState(t, tx, "23514", `UPDATE users SET registration_domain_id = NULL WHERE id = $1`, user.ID)
}

func TestDomainFoundationContentOriginIsWriteOnce(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()
	domainID := insertDomainFixture(t, tx, "kosta")
	otherID := insertDomainFixture(t, tx, "ana")
	address := randomAddress(t, tx)
	rows := make(map[string]uint64)
	for _, table := range []string{"vouchers", "pools"} {
		var id uint64
		require.NoError(t, tx.QueryRow(ctx, `INSERT INTO `+table+` (chain_id, address) VALUES (100, $1) RETURNING id`, address).Scan(&id))
		rows[table] = id
	}
	var reportID uint64
	require.NoError(t, tx.QueryRow(ctx, `INSERT INTO reports (title, author_address) VALUES ('Domain test', $1) RETURNING id`, address).Scan(&reportID))
	rows["reports"] = reportID
	tokenID, err := pg.InsertDeployedToken(ctx, tx, DeployedToken{
		OTXID: seedDeployOTX(t, tx, TOKEN_PROXY_DEPLOY), PlatformContractID: platformContractID(t, tx, "ERC20"),
		Name: "Domain token", Symbol: "DOM", Decimals: 6, Admin: address,
	})
	require.NoError(t, err)
	rows["deployed_tokens"] = tokenID
	poolID, err := pg.InsertDeployedPool(ctx, tx, DeployedPool{
		OTXID: seedDeployOTX(t, tx, POOL_PROXY_DEPLOY), PlatformContractID: platformContractID(t, tx, "SWAPPOOL"),
		Name: "Domain pool", Symbol: "DOMP", Decimals: 6, Admin: address,
		TokenRegistryAddress: address, LimiterAddress: address, FeePolicyAddress: address,
		QuoterAddress: address, ProtocolFeeControllerAddress: address,
	})
	require.NoError(t, err)
	rows["deployed_pools"] = poolID
	for table, id := range rows {
		t.Run(table, func(t *testing.T) {
			var origin *uint64
			require.NoError(t, tx.QueryRow(ctx, `SELECT origin_domain_id FROM `+table+` WHERE id = $1`, id).Scan(&origin))
			assert.Nil(t, origin, "old writers remain compatible")
			statement := `UPDATE ` + table + ` SET origin_domain_id = $2 WHERE id = $1`
			expectDomainSQLState(t, tx, "23503", statement, id, -1)
			_, err := tx.Exec(ctx, statement, id, domainID)
			require.NoError(t, err)
			_, err = tx.Exec(ctx, statement, id, domainID)
			require.NoError(t, err, "same-origin replay is harmless")
			expectDomainSQLState(t, tx, "23514", statement, id, otherID)
			expectDomainSQLState(t, tx, "23514", statement, id, nil)
		})
	}
}

func TestDomainFoundationAuditConstraints(t *testing.T) {
	_, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()
	domainID := insertDomainFixture(t, tx, "kosta")
	_, err := tx.Exec(ctx, `INSERT INTO domain_audit_events (domain_id, actor_key, action, entity_type, entity_key, after_state)
		VALUES ($1, 'user:1', 'join', 'membership', '1', '{"status":"active"}')`, domainID)
	require.NoError(t, err)
	for _, value := range []string{`[]`, `null`, `{"payload":"` + strings.Repeat("x", 16384) + `"}`} {
		expectDomainSQLState(t, tx, "23514", `INSERT INTO domain_audit_events (domain_id, actor_key, action, entity_type, entity_key, after_state)
			VALUES ($1, 'user:1', 'join', 'membership', '1', $2::jsonb)`, domainID, value)
	}
	expectDomainSQLState(t, tx, "23503", `DELETE FROM domains WHERE id = $1`, domainID)
}

func TestDomainFoundationMigrationRoundTrip(t *testing.T) {
	dsn := os.Getenv("CORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CORE_TEST_DSN not set; skipping isolated migration database test")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close(ctx) })
	database := "clc_domain_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{database}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE DATABASE "+identifier)
	require.NoError(t, err, "migration test needs CREATEDB on the disposable Docker test server")
	t.Cleanup(func() {
		_, err := admin.Exec(ctx, "DROP DATABASE "+identifier)
		assert.NoError(t, err)
	})
	config, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	config.Database = database
	conn, err := pgx.ConnectConfig(ctx, config)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(ctx) })
	migrator, err := migrate.NewMigrator(ctx, conn, "schema_version")
	require.NoError(t, err)
	require.NoError(t, migrator.LoadMigrations(os.DirFS("../../migrations")))
	require.NoError(t, migrator.MigrateTo(ctx, 38))
	_, err = conn.Exec(ctx, `INSERT INTO users (blockchain_address, name, email) VALUES ('0x1111111111111111111111111111111111111111', 'Legacy', 'legacy@example.test');
		INSERT INTO vouchers (chain_id, address) VALUES (100, '0x2222222222222222222222222222222222222222')`)
	require.NoError(t, err)
	require.NoError(t, migrator.MigrateTo(ctx, 39))
	var unassigned int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM users WHERE email = 'legacy@example.test' AND registration_domain_id IS NULL AND domain_revision = 0`).Scan(&unassigned))
	assert.Equal(t, 1, unassigned)
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM vouchers WHERE origin_domain_id IS NULL`).Scan(&unassigned))
	assert.Equal(t, 1, unassigned)
	require.NoError(t, migrator.MigrateTo(ctx, 38))
	var domainTable *string
	require.NoError(t, conn.QueryRow(ctx, `SELECT to_regclass('domains')::text`).Scan(&domainTable))
	assert.Nil(t, domainTable)
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM users WHERE email = 'legacy@example.test'`).Scan(&unassigned))
	assert.Equal(t, 1, unassigned, "rollback preserves old account rows")
	require.NoError(t, migrator.MigrateTo(ctx, 39))
	require.NoError(t, migrator.MigrateTo(ctx, 39), "rerunning the migrator is a no-op")
	var globalCount int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM domains WHERE is_global`).Scan(&globalCount))
	assert.Equal(t, 1, globalCount)
}

func insertDomainFixture(t *testing.T, tx pgx.Tx, slug string) uint64 {
	t.Helper()
	var id uint64
	err := tx.QueryRow(context.Background(), `INSERT INTO domains (slug, name) VALUES ($1, $1) RETURNING id`, slug+"-"+uuid.NewString()).Scan(&id)
	require.NoError(t, err)
	return id
}

func expectDomainSQLState(t *testing.T, tx pgx.Tx, code, statement string, args ...any) {
	t.Helper()
	ctx := context.Background()
	savepoint, err := tx.Begin(ctx)
	require.NoError(t, err)
	defer savepoint.Rollback(ctx)
	_, err = savepoint.Exec(ctx, statement, args...)
	var postgresError *pgconn.PgError
	require.ErrorAs(t, err, &postgresError, "expected SQLSTATE %s for %s", code, statement)
	assert.Equal(t, code, postgresError.Code)
}
