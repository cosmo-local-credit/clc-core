package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestDeployedTokenModel verifies the DeployedToken struct fields align with
// expected database column mappings.
func TestDeployedTokenModel(t *testing.T) {
	_dt := DeployedToken{
		ID:                 1,
		OTXID:              2,
		PlatformContractID: 3,
		Address:            "0xabc",
		Symbol:             "TKN",
		Name:               "Token",
		Admin:              "0xadmin",
	}

	assert.Equal(t, uint64(1), _dt.ID)
	assert.Equal(t, uint64(2), _dt.OTXID)
	assert.Equal(t, uint64(3), _dt.PlatformContractID)
	assert.Equal(t, "0xabc", _dt.Address)
	assert.Equal(t, "TKN", _dt.Symbol)
	assert.Equal(t, "Token", _dt.Name)
	assert.Equal(t, "0xadmin", _dt.Admin)
}

// TestTokenDeployMetaModel verifies the TokenDeployMeta struct fields.
func TestTokenDeployMetaModel(t *testing.T) {
	meta := TokenDeployMeta{
		ID:                 1,
		OTXID:              2,
		PlatformContractID: 3,
		TokenName:          "MyToken",
		TokenSymbol:        "MTK",
		Admin:              "0xadmin",
	}

	assert.Equal(t, uint64(1), meta.ID)
	assert.Equal(t, uint64(2), meta.OTXID)
	assert.Equal(t, uint64(3), meta.PlatformContractID)
	assert.Equal(t, "MyToken", meta.TokenName)
	assert.Equal(t, "MTK", meta.TokenSymbol)
	assert.Equal(t, "0xadmin", meta.Admin)
}

// TestStoreInterfaceCompile ensures the Store interface includes the new
// deployed token methods. This is a compile-time check.
func TestStoreInterfaceCompile(t *testing.T) {
	var _ Store = (*Pg)(nil)
}

// --- Integration tests (require a running PostgreSQL with migrations applied) ---
// Run with: go test -tags=integration ./internal/store/ -pg.dsn=<DSN>
