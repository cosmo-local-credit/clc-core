package migrations

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigration002Exists(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	found := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "002_") {
			found = true
			break
		}
	}
	assert.True(t, found, "expected a 002_* migration file in migrations/")
}

func TestMigration002CreatesDeployedTokens(t *testing.T) {
	content, err := os.ReadFile("002_deployed_tokens.sql")
	require.NoError(t, err)

	s := string(content)
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS deployed_tokens")
	assert.Contains(t, s, "otx_id")
	assert.Contains(t, s, "platform_contract_id")
	assert.Contains(t, s, "address")
	assert.Contains(t, s, "symbol")
	assert.Contains(t, s, "name")
	assert.Contains(t, s, "admin")
	assert.Contains(t, s, "UNIQUE (otx_id)")
	assert.Contains(t, s, "UNIQUE (address)")
	assert.Contains(t, s, "REFERENCES otx(id)")
	assert.Contains(t, s, "REFERENCES platform_contracts(id)")
	assert.Contains(t, s, "update_timestamp()")
}

func TestMigration002CreatesTokenDeployMeta(t *testing.T) {
	content, err := os.ReadFile("002_deployed_tokens.sql")
	require.NoError(t, err)

	s := string(content)
	assert.Contains(t, s, "CREATE TABLE IF NOT EXISTS token_deploy_meta")
	assert.Contains(t, s, "token_name")
	assert.Contains(t, s, "token_symbol")
	assert.Contains(t, s, "admin")
	assert.Contains(t, s, "UNIQUE (otx_id)")
	assert.Contains(t, s, "REFERENCES otx(id)")
	assert.Contains(t, s, "REFERENCES platform_contracts(id)")
}
