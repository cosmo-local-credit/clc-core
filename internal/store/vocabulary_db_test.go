package store

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
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
		INSERT INTO offerings(voucher_id,seller_address,commodity_name,quantity)
		VALUES ($1,$2,'Vocabulary',1) RETURNING id`, voucherID, address).Scan(&offeringID))
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
		INSERT INTO offerings(voucher_id,seller_address,commodity_name,quantity,unit)
		VALUES ($1,$2,'Legacy unit',1,'Zqlegacy unknown unit') RETURNING id`,
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

func TestUnitOfAccountVocabularyCountsAndCascadesCurrentMetadataOnly(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()
	value, replacement := "Zqstore Community Hours", "Zqstore Crédit Temps"
	_, err := tx.Exec(ctx, `INSERT INTO unit_of_account(value,is_custom) VALUES ($1,true)`, value)
	require.NoError(t, err)

	address := randomAddress(t, tx)
	var userID uint64
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO users(blockchain_address,name,email,status,role,preferred_currency)
		VALUES($1,'UoA user',gen_random_uuid()::text||'@uoa.test','APPROVED','USER',$2)
		RETURNING id`, address, value).Scan(&userID))
	poolID, voucherID, registry, limiter, quoter, _, _ := seedComposablePool(t, tx)
	_, err = tx.Exec(ctx, `UPDATE vouchers SET voucher_uoa=$1 WHERE id=$2`, value, voucherID)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `UPDATE pools SET unit_of_account=$1 WHERE id=$2`, value, poolID)
	require.NoError(t, err)

	tokenID, err := pg.InsertDeployedToken(ctx, tx, DeployedToken{
		OTXID: seedDeployOTX(t, tx, TOKEN_PROXY_DEPLOY), PlatformContractID: platformContractID(t, tx, "ERC20"),
		Name: "UoA Token", Symbol: "ZQUOAT", Decimals: 6, Admin: address, VoucherUoa: &value,
	})
	require.NoError(t, err)
	poolDeployID, err := pg.InsertDeployedPool(ctx, tx, DeployedPool{
		OTXID: seedDeployOTX(t, tx, POOL_PROXY_DEPLOY), PlatformContractID: platformContractID(t, tx, "SWAPPOOL"),
		Name: "UoA Pool", Symbol: "ZQUOAP", Decimals: 6, Admin: address,
		TokenRegistryAddress: registry, LimiterAddress: limiter, QuoterAddress: quoter,
		FeePolicyAddress: address, ProtocolFeeControllerAddress: address, UnitOfAccount: &value,
	})
	require.NoError(t, err)

	trackingID := uuid.NewString()
	_, err = tx.Exec(ctx, `
		INSERT INTO transfer_valuation_snapshots(tracking_id,token_address,voucher_value,voucher_uoa)
		VALUES($1,$2,1,$3)`, trackingID, strings.ToLower(address), value)
	require.NoError(t, err)

	vocabulary, err := pg.ListVocabulary(ctx, tx)
	require.NoError(t, err)
	var row *UnitOfAccountVocabulary
	for i := range vocabulary.UnitsOfAccount {
		if vocabulary.UnitsOfAccount[i].Value == value {
			row = &vocabulary.UnitsOfAccount[i]
			break
		}
	}
	require.NotNil(t, row)
	require.Equal(t, int64(1), row.Profiles)
	require.Equal(t, int64(1), row.Vouchers)
	require.Equal(t, int64(1), row.Pools)
	require.Equal(t, int64(1), row.VoucherDeployments)
	require.Equal(t, int64(1), row.PoolDeployments)
	require.Equal(t, int64(5), row.Total())

	renamed, err := pg.RenameUnitOfAccount(ctx, tx, value, replacement)
	require.NoError(t, err)
	require.Equal(t, replacement, renamed)
	for _, query := range []string{
		`SELECT count(*) FROM users WHERE id=$1 AND preferred_currency=$2`,
		`SELECT count(*) FROM vouchers WHERE id=$1 AND voucher_uoa=$2`,
		`SELECT count(*) FROM pools WHERE id=$1 AND unit_of_account=$2`,
		`SELECT count(*) FROM deployed_tokens WHERE id=$1 AND voucher_uoa=$2`,
		`SELECT count(*) FROM deployed_pools WHERE id=$1 AND unit_of_account=$2`,
	} {
		var count int
		var id uint64
		switch query {
		case `SELECT count(*) FROM users WHERE id=$1 AND preferred_currency=$2`:
			id = userID
		case `SELECT count(*) FROM vouchers WHERE id=$1 AND voucher_uoa=$2`:
			id = voucherID
		case `SELECT count(*) FROM pools WHERE id=$1 AND unit_of_account=$2`:
			id = poolID
		case `SELECT count(*) FROM deployed_tokens WHERE id=$1 AND voucher_uoa=$2`:
			id = tokenID
		default:
			id = poolDeployID
		}
		require.NoError(t, tx.QueryRow(ctx, query, id, replacement).Scan(&count))
		require.Equal(t, 1, count)
	}

	var historical string
	require.NoError(t, tx.QueryRow(ctx,
		`SELECT voucher_uoa FROM transfer_valuation_snapshots WHERE tracking_id=$1 AND token_address=$2`,
		trackingID, strings.ToLower(address)).Scan(&historical))
	require.Equal(t, value, historical, "historical claim snapshots must not cascade")

	usage, err := pg.DeleteUnusedCustomUnitOfAccount(ctx, tx, replacement)
	require.NoError(t, err)
	require.False(t, usage.Deleted)
	require.Equal(t, int64(5), usage.Total())
}
