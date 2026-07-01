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

func TestOnlyBaselineMigrationExists(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			assert.Equal(t, "001_init.sql", e.Name(), "expected the squashed baseline migration to be the only SQL migration file")
		}
	}
}

func TestBaselineMigrationContainsCurrentSchema(t *testing.T) {
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
	assert.Contains(t, s, "created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP")
	assert.Contains(t, s, "updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP")
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
	assert.Contains(t, s, "0x33a573149db22e759fb9a38bcc461c12855c2645")
}
