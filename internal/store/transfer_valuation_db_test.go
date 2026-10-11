package store

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestTransferValuationSnapshotPreservesExactSubmittedValue(t *testing.T) {
	pg := recipientTestStore(t)
	ctx := context.Background()
	tx, err := pg.Pool().Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	tokenAddress := randomAddress(t, tx)
	_, err = tx.Exec(ctx, `
		INSERT INTO vouchers (chain_id, address, name, symbol, decimals, voucher_uoa, voucher_value)
		VALUES (100, $1, 'Snapshot Voucher', 'SNAP', 6, 'KES', 1.25)`, tokenAddress)
	require.NoError(t, err)

	trackingID := uuid.NewString()
	require.NoError(t, pg.InsertTransferValuationSnapshot(ctx, tx, trackingID, tokenAddress, "123456789"))

	var signerID uint64
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO keystore (public_key, private_key, active)
		VALUES ($1, 'test-private-key', true)
		RETURNING id`, randomAddress(t, tx)).Scan(&signerID))
	for _, hash := range []string{
		"0x1111111111111111111111111111111111111111111111111111111111111111",
		"0x2222222222222222222222222222222222222222222222222222222222222222",
	} {
		_, err = tx.Exec(ctx, `
			INSERT INTO otx (tracking_id, otx_type, signer_account, raw_tx, tx_hash, nonce)
			VALUES ($1, 'RELAY_EXEC', $2, '0x', $3, 1)`, trackingID, signerID, hash)
		require.NoError(t, err)
	}

	rows, err := pg.ListTransferValuationSnapshotsByTxHashes(ctx, tx, []string{
		"0X1111111111111111111111111111111111111111111111111111111111111111",
		"0x2222222222222222222222222222222222222222222222222222222222222222",
	})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	for _, row := range rows {
		require.Equal(t, tokenAddress, row.TokenAddress)
		require.Equal(t, "154.32098625", row.UOAValue)
		require.Equal(t, "KES", row.UOA)
	}
}

func TestTransferValuationSnapshotSkipsIncompleteVoucherMetadata(t *testing.T) {
	pg := recipientTestStore(t)
	ctx := context.Background()
	tx, err := pg.Pool().Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	trackingIDs := make([]string, 0, 2)
	for _, voucherValue := range []any{nil, 0} {
		tokenAddress := randomAddress(t, tx)
		_, err = tx.Exec(ctx, `
			INSERT INTO vouchers (chain_id, address, name, symbol, decimals, voucher_uoa, voucher_value)
			VALUES (100, $1, 'Incomplete Voucher', 'NONE', 6, 'KES', $2)`, tokenAddress, voucherValue)
		require.NoError(t, err)
		trackingID := uuid.NewString()
		trackingIDs = append(trackingIDs, trackingID)
		require.NoError(t, pg.InsertTransferValuationSnapshot(ctx, tx, trackingID, tokenAddress, "1000000"))
	}

	var count int
	require.NoError(t, tx.QueryRow(ctx,
		"SELECT COUNT(*) FROM transfer_valuation_snapshots WHERE tracking_id::text = ANY($1)", trackingIDs).Scan(&count))
	require.Zero(t, count)
}
