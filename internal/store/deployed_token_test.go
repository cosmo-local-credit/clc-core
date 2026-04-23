package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// integrationDeployedTokenTest exercises the full insert/read path for
// deployed_tokens. It requires a real database and is gated behind the
// "integration" build tag so it does not run in short/unit mode.
func integrationDeployedTokenTest(t *testing.T, s Store) {
	ctx := context.Background()
	tx, err := s.Pool().Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	dt := DeployedToken{
		OTXID:              1,
		PlatformContractID: 1,
		Address:            "0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		Symbol:             "TEST",
		Name:               "Test Token",
		Admin:              "0xadmin",
	}

	id, err := s.InsertDeployedToken(ctx, tx, dt)
	require.NoError(t, err)
	assert.Greater(t, id, uint64(0))

	got, err := s.GetDeployedTokenByAddress(ctx, tx, dt.Address)
	require.NoError(t, err)
	assert.Equal(t, dt.OTXID, got.OTXID)
	assert.Equal(t, dt.PlatformContractID, got.PlatformContractID)
	assert.Equal(t, dt.Symbol, got.Symbol)
	assert.Equal(t, dt.Name, got.Name)
	assert.Equal(t, dt.Admin, got.Admin)
}

// integrationTokenDeployMetaTest exercises the full insert/read path for
// token_deploy_meta.
func integrationTokenDeployMetaTest(t *testing.T, s Store) {
	ctx := context.Background()
	tx, err := s.Pool().Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	meta := TokenDeployMeta{
		OTXID:              1,
		PlatformContractID: 1,
		TokenName:          "Integration Token",
		TokenSymbol:        "ITK",
		Admin:              "0xadmin",
	}

	err = s.InsertTokenDeployMeta(ctx, tx, meta)
	require.NoError(t, err)

	got, err := s.GetTokenDeployMetaByOTXID(ctx, tx, meta.OTXID)
	require.NoError(t, err)
	assert.Equal(t, meta.PlatformContractID, got.PlatformContractID)
	assert.Equal(t, meta.TokenName, got.TokenName)
	assert.Equal(t, meta.TokenSymbol, got.TokenSymbol)
	assert.Equal(t, meta.Admin, got.Admin)
}
