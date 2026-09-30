package store

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestVocabularyRenameCascadesEveryTagReference(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()
	old, replacement := "Zqstore Vocabulary", "Zqstore Renamed"
	_, err := tx.Exec(ctx, `INSERT INTO token_tag(value,is_custom) VALUES ($1,true)`, old)
	require.NoError(t, err)
	poolID, voucherID, registry, limiter, quoter, _, _ := seedComposablePool(t, tx)
	address := randomAddress(t, tx)
	tokenID, err := pg.InsertDeployedToken(ctx, tx, DeployedToken{
		OTXID: seedDeployOTX(t, tx, TOKEN_PROXY_DEPLOY), PlatformContractID: platformContractID(t, tx, "ERC20"),
		Name: "Vocabulary Token", Symbol: "ZQVT", Decimals: 6, Admin: address,
	})
	require.NoError(t, err)
	deployedPoolID, err := pg.InsertDeployedPool(ctx, tx, DeployedPool{
		OTXID: seedDeployOTX(t, tx, POOL_PROXY_DEPLOY), PlatformContractID: platformContractID(t, tx, "SWAPPOOL"),
		Name: "Vocabulary Pool", Symbol: "ZQVP", Decimals: 6, Admin: address,
		TokenRegistryAddress: registry, LimiterAddress: limiter, QuoterAddress: quoter,
		FeePolicyAddress: address, ProtocolFeeControllerAddress: address,
	})
	require.NoError(t, err)
	var reportID, offeringID uint64
	require.NoError(t, tx.QueryRow(ctx, `INSERT INTO reports(title,author_address) VALUES ('Vocabulary',$1) RETURNING id`, address).Scan(&reportID))
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO offerings(voucher_id,seller_address,commodity_name,commodity_type,quantity)
		VALUES ($1,$2,'Vocabulary','GOOD',1) RETURNING id`, voucherID, address).Scan(&offeringID))
	references := []struct {
		table, column string
		id            uint64
	}{
		{"deployed_token_tags", "deployed_token_id", tokenID},
		{"deployed_pool_tags", "deployed_pool_id", deployedPoolID},
		{"voucher_tags", "voucher_id", voucherID},
		{"pool_tags", "pool_id", poolID},
		{"report_tags", "report_id", reportID},
		{"offering_tags", "offering_id", offeringID},
	}
	for _, ref := range references {
		_, err := tx.Exec(ctx, `INSERT INTO `+ref.table+` (`+ref.column+`,tag_value) VALUES ($1,$2)`, ref.id, old)
		require.NoError(t, err)
	}
	renamed, err := pg.RenameVocabulary(ctx, tx, true, old, replacement)
	require.NoError(t, err)
	require.Equal(t, replacement, renamed)
	for _, ref := range references {
		var values []string
		require.NoError(t, tx.QueryRow(ctx, `SELECT array_agg(tag_value) FROM `+ref.table+` WHERE `+ref.column+`=$1`, ref.id).Scan(&values))
		require.Equal(t, []string{replacement}, values, ref.table)
	}
}

func TestVocabularyMigrationPreservesLegacyOfferingUnits(t *testing.T) {
	_, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()
	voucherID := seedMarketVoucher(t, tx, 0, 0, true)
	// Reproduce the state deliberately grandfathered by migration 023.
	_, err := tx.Exec(ctx, `ALTER TABLE offerings DROP CONSTRAINT offerings_unit_fk`)
	require.NoError(t, err)
	var id uint64
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO offerings(voucher_id,seller_address,commodity_name,commodity_type,quantity,unit)
		VALUES ($1,$2,'Legacy unit','GOOD',1,'Zqlegacy unknown unit') RETURNING id`,
		voucherID, randomAddress(t, tx)).Scan(&id))
	_, err = tx.Exec(ctx, `ALTER TABLE offerings ADD CONSTRAINT offerings_unit_fk
		FOREIGN KEY(unit) REFERENCES offering_unit(value) NOT VALID`)
	require.NoError(t, err)
	migration, err := os.ReadFile("../../migrations/040_editable_vocabulary.sql")
	require.NoError(t, err)
	_, err = tx.Exec(ctx, string(migration))
	require.NoError(t, err, "upgrade must accept existing imported free-text units")
	var unit string
	require.NoError(t, tx.QueryRow(ctx, `SELECT unit FROM offerings WHERE id=$1`, id).Scan(&unit))
	require.Equal(t, "Zqlegacy unknown unit", unit)
	// NOT VALID still constrains new writes.
	_, err = tx.Exec(ctx, `UPDATE offerings SET unit='Zqlegacy another unknown unit' WHERE id=$1`, id)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "23503", pgErr.Code)
}

// A key whose name differs from Postgres's default must still be replaced, not
// left beside the cascading one where it would reject every rename.
func TestVocabularyMigrationReplacesKeysWhateverTheirName(t *testing.T) {
	_, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()
	_, err := tx.Exec(ctx, `ALTER TABLE voucher_tags RENAME CONSTRAINT voucher_tags_tag_value_fkey TO voucher_tags_legacy_fk`)
	require.NoError(t, err)
	migration, err := os.ReadFile("../../migrations/040_editable_vocabulary.sql")
	require.NoError(t, err)
	_, err = tx.Exec(ctx, string(migration))
	require.NoError(t, err)
	var keys []string
	require.NoError(t, tx.QueryRow(ctx, `
		SELECT array_agg(conname || ':' || confupdtype::text ORDER BY conname) FROM pg_constraint
		WHERE contype = 'f' AND conrelid = 'voucher_tags'::regclass AND confrelid = 'token_tag'::regclass`).Scan(&keys))
	require.Equal(t, []string{"voucher_tags_tag_value_fkey:c"}, keys)
}

func TestVocabularyReferencesHoldLockUntilWriterFinishes(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()
	// Use committed seeded values: a second connection must be able to see them.
	var tag string
	require.NoError(t, tx.QueryRow(ctx, `SELECT value FROM token_tag WHERE NOT is_custom LIMIT 1`).Scan(&tag))
	_, err := pg.FindTokenTag(ctx, tx, tag)
	require.NoError(t, err)
	_, err = pg.FindOfferingUnit(ctx, tx, "kg")
	require.NoError(t, err)
	conn, err := pgx.Connect(ctx, os.Getenv("CORE_TEST_DSN"))
	require.NoError(t, err)
	defer conn.Close(ctx)
	for _, tc := range []struct{ table, value string }{{"token_tag", tag}, {"offering_unit", "kg"}} {
		_, err = conn.Exec(ctx, "SET lock_timeout='100ms'")
		require.NoError(t, err)
		_, err = conn.Exec(ctx, `SELECT value FROM `+tc.table+` WHERE value=$1 FOR UPDATE`, tc.value)
		var pgErr *pgconn.PgError
		require.ErrorAs(t, err, &pgErr)
		require.Equal(t, "55P03", pgErr.Code, "rename must wait for an in-flight writer")
	}
	require.NoError(t, tx.Rollback(ctx))
	_, err = conn.Exec(ctx, `SELECT value FROM token_tag WHERE value=$1 FOR UPDATE`, tag)
	require.NoError(t, err, "lock released when writer finishes")
}
