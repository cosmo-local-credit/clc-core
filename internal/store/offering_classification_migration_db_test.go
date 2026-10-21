package store

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRemoveOfferingClassificationMigration(t *testing.T) {
	_, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()
	// Temporary tables reproduce the old shape without changing shared fixtures.
	_, err := tx.Exec(ctx, `
		CREATE TEMP TABLE commodity_type (value TEXT PRIMARY KEY);
		INSERT INTO commodity_type VALUES ('GOOD'), ('SERVICE');
		CREATE TEMP TABLE offerings (
			id BIGINT PRIMARY KEY,
			voucher_id BIGINT NOT NULL,
			commodity_name TEXT NOT NULL,
			commodity_type TEXT NOT NULL REFERENCES commodity_type(value),
			price NUMERIC NOT NULL,
			available BOOLEAN NOT NULL
		);
		CREATE INDEX offerings_type_available_idx ON offerings (commodity_type, available, id);
		CREATE INDEX offerings_available_voucher_type_idx ON offerings (voucher_id, commodity_type) WHERE available;
		INSERT INTO offerings VALUES
			(1, 10, 'Produce', 'GOOD', 12345678901234567890.12345, true),
			(2, 10, 'Training', 'SERVICE', 25, false);`)
	require.NoError(t, err)
	migration, err := os.ReadFile("../../migrations/055_remove_offering_classification.sql")
	require.NoError(t, err)
	_, err = tx.Exec(ctx, string(migration))
	require.NoError(t, err)

	var columnExists, lookupExists, indexExists bool
	err = tx.QueryRow(ctx, `SELECT
		EXISTS (SELECT FROM pg_attribute WHERE attrelid='pg_temp.offerings'::regclass
			AND attname='commodity_type' AND NOT attisdropped),
		to_regclass('pg_temp.commodity_type') IS NOT NULL,
		to_regclass('pg_temp.offerings_available_voucher_type_idx') IS NOT NULL
			OR to_regclass('pg_temp.offerings_type_available_idx') IS NOT NULL`).Scan(
		&columnExists, &lookupExists, &indexExists)
	require.NoError(t, err)
	require.False(t, columnExists)
	require.False(t, lookupExists)
	require.False(t, indexExists)

	var price, name string
	var voucherID int64
	var available bool
	err = tx.QueryRow(ctx, `SELECT voucher_id, commodity_name, price::TEXT, available
		FROM offerings WHERE id=1`).Scan(&voucherID, &name, &price, &available)
	require.NoError(t, err)
	require.Equal(t, int64(10), voucherID)
	require.Equal(t, "Produce", name)
	require.Equal(t, "12345678901234567890.12345", price)
	require.True(t, available)
	err = tx.QueryRow(ctx, `SELECT available FROM offerings WHERE id=2`).Scan(&available)
	require.NoError(t, err)
	require.False(t, available)

	_, err = tx.Exec(ctx, `INSERT INTO offerings (id, voucher_id, commodity_name, price, available)
		VALUES (3, 10, 'New offering', 50, true)`)
	require.NoError(t, err, "new offerings need no classification or default")
}
