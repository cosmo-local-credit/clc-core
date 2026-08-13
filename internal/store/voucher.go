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
