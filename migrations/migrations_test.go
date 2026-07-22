package migrations

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBaselineMigrationExists(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	found := false
	for _, e := range entries {
		if e.Name() == "001_init.sql" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected a 001_init.sql baseline migration in migrations/")
}

func TestMigrationFiles(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	var sqlFiles []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			sqlFiles = append(sqlFiles, e.Name())
		}
	}

	assert.Contains(t, sqlFiles, "001_init.sql")
	assert.Contains(t, sqlFiles, "002_unify_deploy_registry.sql")
	assert.Contains(t, sqlFiles, "003_constraint_hardening.sql")
	assert.Contains(t, sqlFiles, "004_timestamptz.sql")
	for _, name := range sqlFiles {
		assert.True(t,
			name == "001_init.sql" ||
				name == "002_unify_deploy_registry.sql" ||
				name == "003_constraint_hardening.sql" ||
				name == "004_timestamptz.sql",
			"unexpected migration file %s", name,
		)
	}
}

func TestBaselineMigrationContainsLegacySchema(t *testing.T) {
	content, err := os.ReadFile("001_init.sql")
	require.NoError(t, err)

	s := string(content)
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS deployed_tokens")
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS token_deploy_meta")
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS pool_addresses")
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS pool_deploy_meta")
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS deployed_pools")
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS oracle_feeds")
	assert.Contains(t, s, "POOL_DEPLOY_STEP")
	assert.Contains(t, s, "POOL_PROXY_DEPLOY")
	assert.Contains(t, s, "TOKEN_UNIQUE_SYMBOL_INDEX")
	assert.Contains(t, s, "LIMITER")
	assert.Contains(t, s, "FEE_POLICY")
	assert.Contains(t, s, "ORACLE_QUOTER")
	assert.Contains(t, s, "RELATIVE_QUOTER")
	assert.Contains(t, s, "token_name")
	assert.Contains(t, s, "token_symbol")
	assert.Contains(t, s, "admin")
	assert.Contains(t, s, "UNIQUE (otx_id)")
	assert.Contains(t, s, "REFERENCES otx(id)")
	assert.Contains(t, s, "REFERENCES platform_contracts(id)")
	assert.Contains(t, s, "protocol_fee_controller")
	assert.Contains(t, s, "decimal_quoter")
	assert.Contains(t, s, "fees_decoupled")
	assert.Contains(t, s, "quoter_address")
	assert.Contains(t, s, "limiter_address")
	assert.Contains(t, s, "token_registry_address")
	assert.Contains(t, s, "fee_policy_address")
	assert.Contains(t, s, "SWAPPOOL")
	assert.Contains(t, s, "name       TEXT NOT NULL UNIQUE")
	assert.Contains(t, s, "address    TEXT NOT NULL")
	assert.Contains(t, s, "created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP")
	assert.Contains(t, s, "updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP")
	assert.Contains(t, s, "CREATE TRIGGER update_oracle_feeds_timestamp")
	assert.Contains(t, s, "BEFORE UPDATE ON oracle_feeds")
	assert.Contains(t, s, "update_timestamp()")
	assert.Contains(t, s, "INSERT INTO oracle_feeds (name, address) VALUES")
	assert.Contains(t, s, "('USDT', '0x5e37AF40A7A344ec9b03CCD34a250F3dA9a20B02')")
	assert.Contains(t, s, "('KES',  '0x85d4Ec34339478F73c153710B19f2D5C402dce6F')")
	assert.Contains(t, s, "ON CONFLICT (name) DO UPDATE SET")
	assert.Contains(t, s, "updated_at = CURRENT_TIMESTAMP")
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS upgrade_meta")
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS token_tag")
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS token_deploy_meta_tags")
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS deployed_token_tags")
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS pool_deploy_meta_tags")
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS deployed_pool_tags")
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS webauthn_credentials")
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS users")
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS user_deployments")
	assert.Contains(t, s, "CONSTRAINT keystore_public_key_key UNIQUE (public_key)")
	assert.Contains(t, s, "RELAY_DELEGATE")
	assert.Contains(t, s, "RELAY_EXEC")
	assert.Contains(t, s, "RELAY_DELEGATE_EXEC")
	assert.Contains(t, s, "ACCOUNT_REGISTER")
	assert.Contains(t, s, "blockchain_address")
	assert.Contains(t, s, "CONSTRAINT users_email_unique UNIQUE (email)")
	assert.Contains(t, s, "CONSTRAINT users_phone_unique UNIQUE (phone)")
	assert.Contains(t, s, "CONSTRAINT users_at_least_one_contact CHECK (email IS NOT NULL OR phone IS NOT NULL)")
	assert.Contains(t, s, "user_role")
	assert.Contains(t, s, "user_status")
	assert.Contains(t, s, "SUPERADMIN")
	// Seed superadmin is stored in canonical EIP-55 checksummed form.
	assert.Contains(t, s, "0x33a573149db22e759fB9A38Bcc461C12855c2645")
	assert.NotContains(t, s, "0x33a573149db22e759fb9a38bcc461c12855c2645")
}

func TestUnifyDeployRegistryMigration(t *testing.T) {
	content, err := os.ReadFile("002_unify_deploy_registry.sql")
	require.NoError(t, err)

	s := string(content)
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS deploy_status_type")
	assert.Contains(t, s, "('PENDING')")
	assert.Contains(t, s, "('CONFIRMED')")
	assert.Contains(t, s, "('FAILED')")
	assert.Contains(t, s, "SET status = 'CONFIRMED'")
	assert.Contains(t, s, "ADD COLUMN IF NOT EXISTS decimals")
	assert.Contains(t, s, "ALTER COLUMN address DROP NOT NULL")
	assert.Contains(t, s, "FROM token_deploy_meta m")
	assert.Contains(t, s, "FROM pool_deploy_meta m")
	assert.Contains(t, s, "INSERT INTO deployed_token_tags")
	assert.Contains(t, s, "INSERT INTO deployed_pool_tags")
	assert.Contains(t, s, "DROP TABLE IF EXISTS token_deploy_meta")
	assert.Contains(t, s, "DROP TABLE IF EXISTS pool_deploy_meta")
	assert.Contains(t, s, "m.pool_decimals")
}

func TestConstraintHardeningMigration(t *testing.T) {
	content, err := os.ReadFile("003_constraint_hardening.sql")
	require.NoError(t, err)

	s := string(content)
	// 1a: one mutable dispatch row per OTX.
	assert.Contains(t, s, "ALTER COLUMN otx_id SET NOT NULL")
	assert.Contains(t, s, "ADD CONSTRAINT dispatch_otx_id_unique UNIQUE (otx_id)")
	// 1b: master key FK + DB-enforced singleton.
	assert.Contains(t, s, "ADD CONSTRAINT master_key_id_fkey FOREIGN KEY (id) REFERENCES keystore(id)")
	assert.Contains(t, s, "CREATE UNIQUE INDEX IF NOT EXISTS master_key_singleton_idx")
	// 1c: nonce hardening.
	assert.Contains(t, s, "ALTER COLUMN next_nonce TYPE BIGINT")
	assert.Contains(t, s, "CHECK (next_nonce >= 0)")
	assert.Contains(t, s, "ALTER COLUMN nonce TYPE BIGINT")
	assert.Contains(t, s, "CHECK (nonce >= 0)")
	// 1d: domain checks on the post-002 unified registries.
	assert.Contains(t, s, "deployed_tokens_decimals_range")
	assert.Contains(t, s, "deployed_pools_decimals_range")
	assert.Contains(t, s, "CHECK (decimals BETWEEN 0 AND 255)")
	assert.Contains(t, s, "users_quotas_nonneg")
	assert.Contains(t, s, "deployed_tokens_geo_valid")
	assert.Contains(t, s, "deployed_pools_geo_valid")
	assert.Contains(t, s, "deployed_tokens_confirmed_has_address")
	assert.Contains(t, s, "deployed_pools_confirmed_has_address")
	assert.NotContains(t, s, "token_deploy_meta")
	assert.NotContains(t, s, "pool_deploy_meta")
	// 1e: indexes duplicated by unique constraints.
	assert.Contains(t, s, "DROP INDEX IF EXISTS public_key_idx")
	assert.Contains(t, s, "DROP INDEX IF EXISTS users_blockchain_address_idx")
	assert.Contains(t, s, "DROP INDEX IF EXISTS user_deployments_tracking_id_idx")
	// 2a: seeded superadmin canonicalized to EIP-55.
	assert.Contains(t, s, "SET blockchain_address = '0x33a573149db22e759fB9A38Bcc461C12855c2645'")
	// 2b: case-insensitive uniqueness backstops + shape checks.
	assert.Contains(t, s, "ON keystore (lower(public_key))")
	assert.Contains(t, s, "ON users (lower(blockchain_address))")
	assert.Contains(t, s, "ON deployed_tokens (lower(address))")
	assert.Contains(t, s, "ON deployed_pools (lower(address))")
	for _, name := range []string{
		"keystore_public_key_format",
		"users_blockchain_address_format",
		"deployed_tokens_address_format",
		"deployed_pools_address_format",
		"upgrade_meta_address_format",
		"platform_contracts_address_format",
		"pool_addresses_address_format",
		"oracle_feeds_address_format",
		"webauthn_address_format",
	} {
		assert.Contains(t, s, name)
	}
	assert.Contains(t, s, `~ '^0x[0-9a-fA-F]{40}$'`)
	// Stored values stay EIP-55 checksummed — never force lowercase storage.
	assert.NotContains(t, s, "= lower(address)")
	assert.NotContains(t, s, "= lower(blockchain_address)")
}

func TestTimestamptzMigration(t *testing.T) {
	content, err := os.ReadFile("004_timestamptz.sql")
	require.NoError(t, err)

	s := string(content)
	assert.Contains(t, s, "SET LOCAL TIME ZONE 'UTC'")
	assert.Contains(t, s, "TYPE TIMESTAMPTZ")
	// An explicit USING clause forces a full table rewrite even at UTC.
	assert.NotContains(t, s, "USING")
	// SET LOCAL depends on tern's default per-migration transaction.
	assert.NotContains(t, s, "disable-tx")
	for _, tbl := range []string{
		"keystore", "master_key", "noncestore", "otx", "dispatch",
		"upgrade_meta", "platform_contracts", "deployed_tokens",
		"pool_addresses", "deployed_pools", "oracle_feeds",
		"webauthn_credentials",
	} {
		assert.Contains(t, s, "ALTER TABLE "+tbl)
	}
}

// The baseline carries the hardened constraints inline (same names as 003)
// so fresh databases get them from 001 and 003's guarded ALTERs no-op.
func TestBaselineCarriesHardenedConstraints(t *testing.T) {
	content, err := os.ReadFile("001_init.sql")
	require.NoError(t, err)

	s := string(content)
	for _, want := range []string{
		"CONSTRAINT dispatch_otx_id_unique UNIQUE (otx_id)",
		"CONSTRAINT master_key_id_fkey FOREIGN KEY (id) REFERENCES keystore(id)",
		"master_key_singleton_idx",
		"BIGINT NOT NULL DEFAULT 0",
		"CONSTRAINT noncestore_next_nonce_nonneg CHECK (next_nonce >= 0)",
		"CONSTRAINT otx_nonce_nonneg CHECK (nonce >= 0)",
		"keystore_public_key_format",
		"users_blockchain_address_format",
		"users_quotas_nonneg",
		"deployed_tokens_address_format",
		"deployed_tokens_geo_valid",
		"deployed_pools_address_format",
		"deployed_pools_geo_valid",
		"upgrade_meta_address_format",
		"platform_contracts_address_format",
		"pool_addresses_address_format",
		"oracle_feeds_address_format",
		"webauthn_address_format",
		"keystore_public_key_lower_key",
		"users_blockchain_address_lower_key",
		"deployed_tokens_address_lower_key",
		"deployed_pools_address_lower_key",
	} {
		assert.Contains(t, s, want)
	}
	// Indexes duplicated by unique constraints must not come back.
	assert.NotContains(t, s, "public_key_idx ON keystore")
	assert.NotContains(t, s, "users_blockchain_address_idx")
	assert.NotContains(t, s, "user_deployments_tracking_id_idx")
}
