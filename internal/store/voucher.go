package store

import (
	"context"
	"strings"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

type (
	// VoucherMetadata is the catalog slice used to enrich federation chain
	// facts (balances, transfers) with token metadata. Fields are pointers
	// because catalog rows imported from sparse sources may lack them.
	VoucherMetadata struct {
		Address  string  `db:"address"`
		Symbol   *string `db:"symbol"`
		Name     *string `db:"name"`
		Decimals *int32  `db:"decimals"`
		IconURL  *string `db:"icon_url"`
	}

	// VoucherProfile is a voucher's editable off-chain record.
	//
	// Every field is a pointer and every one is assigned, so a caller sends the
	// whole record: nil clears. The exceptions are IconURL and BannerURL, where
	// nil preserves and a pointer to "" clears — see update-voucher-profile.
	//
	// DisplayName is the one name a catalog editor owns: it is an off-chain
	// override for the immutable deployed ERC20 name. Nil preserves it and an
	// empty string clears it, matching the image-field compatibility rule.
	// Symbol, decimals and owner_address remain chain facts.
	VoucherProfile struct {
		DisplayName  *string
		Description  *string
		VoucherUOA   *string
		VoucherValue *float64
		Email        *string
		Website      *string
		Phone        *string
		Terms        *string
		LocationName *string
		GeoLat       *float64
		GeoLng       *float64
		IconURL      *string
		BannerURL    *string
	}
)

// ListVoucherMetadataByAddresses returns catalog metadata for the given token
// addresses. Matching is case-insensitive; callers may pass addresses in any
// casing. Addresses absent from the catalog are simply not returned.
func (pg *Pg) ListVoucherMetadataByAddresses(ctx context.Context, tx pgx.Tx, addresses []string) ([]VoucherMetadata, error) {
	lowered := make([]string, len(addresses))
	for i, a := range addresses {
		lowered[i] = strings.ToLower(a)
	}

	var rows []VoucherMetadata
	if err := pgxscan.Select(ctx, tx, &rows, pg.queries.ListVoucherMetadataByAddresses, lowered); err != nil {
		return nil, err
	}
	return rows, nil
}

// UpdateVoucherProfile writes an operator's edits to a voucher's off-chain
// record and returns the voucher's id so tags can be replaced in the same
// transaction.
//
// Assign-all, like UpdatePoolProfile: an omitted field clears rather than
// preserves, because patch semantics over nullable columns cannot express
// clearing a field at all — JSON gives no way to tell an absent key from a null.
// DisplayName and the image fields are deliberate compatibility exceptions;
// nil preserves their value and an empty string clears it.
func (pg *Pg) UpdateVoucherProfile(
	ctx context.Context,
	tx pgx.Tx,
	address string,
	profile VoucherProfile,
) (uint64, error) {
	var id uint64
	err := tx.QueryRow(
		ctx,
		pg.queries.UpdateVoucherProfile,
		address,
		profile.DisplayName,
		profile.Description,
		profile.VoucherUOA,
		profile.VoucherValue,
		profile.Email,
		profile.Website,
		profile.Phone,
		profile.Terms,
		profile.LocationName,
		profile.GeoLat,
		profile.GeoLng,
		profile.IconURL,
		profile.BannerURL,
	).Scan(&id)
	return id, err
}

// UpdateVoucherDisplayName changes only the catalog label after the caller has
// authorized against a locked voucher row. It cannot mutate any chain-derived
// voucher data, including the immutable deployed name.
func (pg *Pg) UpdateVoucherDisplayName(ctx context.Context, tx pgx.Tx, voucherID uint64, displayName string) error {
	_, err := tx.Exec(ctx, pg.queries.UpdateVoucherDisplayName, voucherID, displayName)
	return err
}

// SetVoucherActive flips a voucher's catalog visibility after the caller has
// authorized against a locked voucher row.
//
// One method for both directions because the write is the same column; retiring
// and restoring differ only in who may ask, which the handlers decide. Nothing
// is deleted: every report and offering that references this voucher keeps its
// foreign key, and restoring returns the voucher to the catalog intact.
func (pg *Pg) SetVoucherActive(ctx context.Context, tx pgx.Tx, voucherID uint64, active bool) error {
	_, err := tx.Exec(ctx, pg.queries.SetVoucherActive, voucherID, active)
	return err
}

// ReplaceVoucherTags swaps a voucher's whole tag set.
//
// Clear-then-insert rather than a diff, for the reason ReplacePoolTags gives: the
// set is small, the caller sends all of it, and a diff would need the current
// rows read back for no gain.
func (pg *Pg) ReplaceVoucherTags(ctx context.Context, tx pgx.Tx, voucherID uint64, tags []string) error {
	if _, err := tx.Exec(ctx, pg.queries.DeleteVoucherTags, voucherID); err != nil {
		return err
	}
	for _, tag := range tags {
		if _, err := tx.Exec(ctx, pg.queries.InsertVoucherTag, voucherID, tag); err != nil {
			return err
		}
	}
	return nil
}
