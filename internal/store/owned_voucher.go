package store

import (
	"context"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

// OwnedVoucher is the wallet summary for a voucher whose catalog owner is the
// authenticated user. Unlike market summaries, it deliberately includes
// retired and unlocated vouchers and therefore uses nullable profile fields.
type (
	OwnedVoucher struct {
		ID           uint64  `db:"id"            json:"id"`
		ChainID      int64   `db:"chain_id"      json:"chainId"`
		Address      string  `db:"address"       json:"address"`
		Name         string  `db:"name"          json:"name"`
		Symbol       string  `db:"symbol"        json:"symbol"`
		Decimals     *int    `db:"decimals"      json:"decimals"`
		IconURL      *string `db:"icon_url"      json:"iconUrl"`
		OwnerAddress string  `db:"owner_address" json:"ownerAddress"`
		Active       bool    `db:"active"        json:"active"`
	}
)

func (pg *Pg) ListOwnedVouchers(ctx context.Context, tx pgx.Tx, userID uint64, limit uint64) ([]OwnedVoucher, error) {
	var vouchers []OwnedVoucher
	if err := pgxscan.Select(ctx, tx, &vouchers, pg.queries.ListOwnedVouchers, userID, limit); err != nil {
		return nil, err
	}
	return vouchers, nil
}

func (pg *Pg) ListOwnedVouchersNext(ctx context.Context, tx pgx.Tx, userID uint64, cursor uint64, limit uint64) ([]OwnedVoucher, error) {
	var vouchers []OwnedVoucher
	if err := pgxscan.Select(ctx, tx, &vouchers, pg.queries.ListOwnedVouchersNext, userID, cursor, limit); err != nil {
		return nil, err
	}
	return vouchers, nil
}

func (pg *Pg) ListOwnedVouchersPrevious(ctx context.Context, tx pgx.Tx, userID uint64, cursor uint64, limit uint64) ([]OwnedVoucher, error) {
	var vouchers []OwnedVoucher
	if err := pgxscan.Select(ctx, tx, &vouchers, pg.queries.ListOwnedVouchersPrev, userID, cursor, limit); err != nil {
		return nil, err
	}
	return vouchers, nil
}
