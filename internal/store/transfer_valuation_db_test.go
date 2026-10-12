package store

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestTransferValuationSnapshotsPreserveClaimsForEveryToken(t *testing.T) {
	pg := recipientTestStore(t)
	ctx := context.Background()
	tx, err := pg.Pool().Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	tokenAddress := randomAddress(t, tx)
	otherTokenAddress := randomAddress(t, tx)
	_, err = tx.Exec(ctx, `
		INSERT INTO vouchers (chain_id, address, name, symbol, decimals, voucher_uoa, voucher_value)
		VALUES (100, $1, 'Snapshot Voucher', 'SNAP', 6, 'KES', 1.25),
		       (100, $2, 'Other Voucher', 'OTHR', 18, 'HOUR', 2.5)`, tokenAddress, otherTokenAddress)
	require.NoError(t, err)

	trackingID := uuid.NewString()
	snapshots, err := pg.InsertTransferValuationSnapshots(ctx, tx, trackingID, []string{
		tokenAddress, otherTokenAddress, tokenAddress,
	})
	require.NoError(t, err)
	require.ElementsMatch(t, []VoucherClaimSnapshot{
		{TokenAddress: tokenAddress, VoucherValue: "1.25", VoucherUOA: "KES"},
		{TokenAddress: otherTokenAddress, VoucherValue: "2.5", VoucherUOA: "HOUR"},
	}, snapshots)

	_, err = tx.Exec(ctx, `UPDATE vouchers SET voucher_value=999, voucher_uoa='USD' WHERE address=$1`, tokenAddress)
	require.NoError(t, err)

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
	require.Len(t, rows, 4)
	for _, row := range rows {
		if row.TokenAddress == tokenAddress {
			require.Equal(t, "1.25", row.VoucherValue)
			require.Equal(t, "KES", row.VoucherUOA)
		} else {
			require.Equal(t, otherTokenAddress, row.TokenAddress)
			require.Equal(t, "2.5", row.VoucherValue)
			require.Equal(t, "HOUR", row.VoucherUOA)
		}
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
		rows, err := pg.InsertTransferValuationSnapshots(ctx, tx, trackingID, []string{tokenAddress})
		require.NoError(t, err)
		require.Empty(t, rows)
	}

	var count int
	require.NoError(t, tx.QueryRow(ctx,
		"SELECT COUNT(*) FROM transfer_valuation_snapshots WHERE tracking_id::text = ANY($1)", trackingIDs).Scan(&count))
	require.Zero(t, count)
}
