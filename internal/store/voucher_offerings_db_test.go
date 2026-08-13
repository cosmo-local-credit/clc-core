package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

func marketVoucherAddress(t *testing.T, tx pgx.Tx, id uint64) string {
	t.Helper()

	var address string
	if err := tx.QueryRow(context.Background(), "SELECT address FROM vouchers WHERE id = $1", id).Scan(&address); err != nil {
		t.Fatalf("read voucher address: %v", err)
	}
	return address
}

func TestVoucherOfferingsExcludeUnavailableAndInactiveRows(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	activeID := seedMarketVoucher(t, tx, -1.29, 36.82, true, "GOOD")
	seedUnavailableOffering(t, tx, activeID, "SERVICE")
	activeAddress := marketVoucherAddress(t, tx, activeID)

	activeRows, err := pg.ListVoucherOfferings(ctx, tx, activeAddress, 20)
	if err != nil {
		t.Fatalf("list active voucher offerings: %v", err)
	}
	if len(activeRows) != 1 {
		t.Fatalf("active voucher returned %d offerings, want only its one available row", len(activeRows))
	}
	if !activeRows[0].Available {
		t.Fatal("public voucher offerings returned an unavailable row")
	}

	inactiveID := seedMarketVoucher(t, tx, -1.29, 36.82, false, "GOOD")
	inactiveAddress := marketVoucherAddress(t, tx, inactiveID)
	inactiveRows, err := pg.ListVoucherOfferings(ctx, tx, inactiveAddress, 20)
	if err != nil {
		t.Fatalf("list inactive voucher offerings: %v", err)
	}
	if len(inactiveRows) != 0 {
		t.Fatalf("inactive voucher returned %d offerings, want none", len(inactiveRows))
	}
}

func TestVoucherKindAgreesWithVoucherOfferingsForSeededFixtures(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	fixtures := []uint64{
		seedMarketVoucher(t, tx, -1.29, 36.82, true),
		seedMarketVoucher(t, tx, -1.29, 36.82, true, "GOOD"),
	}
	unavailableOnly := seedMarketVoucher(t, tx, -1.29, 36.82, true)
	seedUnavailableOffering(t, tx, unavailableOnly, "SERVICE")
	fixtures = append(fixtures, unavailableOnly)

	for _, id := range fixtures {
		address := marketVoucherAddress(t, tx, id)
		offerings, err := pg.ListVoucherOfferings(ctx, tx, address, 20)
		if err != nil {
			t.Fatalf("voucher %d offerings: %v", id, err)
		}
		isVoucherKind := kindOf(t, pg, tx, id) == "voucher"
		if isVoucherKind != (len(offerings) == 0) {
			t.Errorf("voucher %d: kind voucher = %t, offering count = %d", id, isVoucherKind, len(offerings))
		}
	}
}

func seedPoolWithVoucher(t *testing.T, tx pgx.Tx, voucherID uint64, isAllowed bool) string {
	t.Helper()
	ctx := context.Background()

	var poolID uint64
	var address string
	err := tx.QueryRow(ctx, `
		INSERT INTO pools (chain_id, address, name, symbol, decimals)
		VALUES (42220, '0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'),
		        'Pool Fixture', 'PFIX', 6)
		RETURNING id, address`).Scan(&poolID, &address)
	if err != nil {
		t.Fatalf("seed pool: %v", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO pool_tokens (pool_id, voucher_id, is_allowed) VALUES ($1, $2, $3)`,
		poolID, voucherID, isAllowed); err != nil {
		t.Fatalf("seed pool token: %v", err)
	}
	return address
}

// A pool's offerings are the union of its allowed vouchers' offerings, and must
// answer identically to each voucher's own page. Anything else means the same
// offering reads differently depending on how the user reached it.
func TestPoolOfferingsMatchTheirVouchersAndExcludeWithdrawnRows(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	voucherID := seedMarketVoucher(t, tx, -1.29, 36.82, true, "GOOD", "SERVICE")
	seedUnavailableOffering(t, tx, voucherID, "SERVICE")
	poolAddress := seedPoolWithVoucher(t, tx, voucherID, true)

	poolRows, err := pg.ListPoolOfferings(ctx, tx, poolAddress, 20)
	if err != nil {
		t.Fatalf("list pool offerings: %v", err)
	}
	if len(poolRows) != 2 {
		t.Fatalf("pool returned %d offerings, want its two available rows", len(poolRows))
	}
	for _, row := range poolRows {
		if !row.Available {
			t.Fatal("pool offerings returned an unavailable row")
		}
		if row.VoucherSymbol == nil || *row.VoucherSymbol == "" {
			t.Fatal("pool offerings must name the voucher each row belongs to")
		}
	}

	voucherRows, err := pg.ListVoucherOfferings(ctx, tx, marketVoucherAddress(t, tx, voucherID), 20)
	if err != nil {
		t.Fatalf("list voucher offerings: %v", err)
	}
	if len(voucherRows) != len(poolRows) {
		t.Fatalf("pool returned %d offerings but the voucher page returns %d", len(poolRows), len(voucherRows))
	}
}

func TestPoolOfferingsExcludeDisallowedAndInactiveVouchers(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	disallowed := seedPoolWithVoucher(t, tx, seedMarketVoucher(t, tx, -1.29, 36.82, true, "GOOD"), false)
	rows, err := pg.ListPoolOfferings(ctx, tx, disallowed, 20)
	if err != nil {
		t.Fatalf("list disallowed pool offerings: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("a pool returned %d offerings for a voucher it no longer allows", len(rows))
	}

	inactive := seedPoolWithVoucher(t, tx, seedMarketVoucher(t, tx, -1.29, 36.82, false, "GOOD"), true)
	rows, err = pg.ListPoolOfferings(ctx, tx, inactive, 20)
	if err != nil {
		t.Fatalf("list inactive pool offerings: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("a pool returned %d offerings for an inactive voucher", len(rows))
	}
}

func TestPoolOfferingsPageDescendingAcrossCursors(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	voucherID := seedMarketVoucher(t, tx, -1.29, 36.82, true, "GOOD", "SERVICE", "GOOD")
	poolAddress := seedPoolWithVoucher(t, tx, voucherID, true)

	first, err := pg.ListPoolOfferings(ctx, tx, poolAddress, 2)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first) != 2 || first[0].ID <= first[1].ID {
		t.Fatalf("first page is not two descending rows: %+v", first)
	}

	next, err := pg.ListPoolOfferingsNext(ctx, tx, poolAddress, first[1].ID, 2)
	if err != nil {
		t.Fatalf("next page: %v", err)
	}
	if len(next) != 1 || next[0].ID >= first[1].ID {
		t.Fatalf("next page did not continue below the cursor: %+v", next)
	}

	prev, err := pg.ListPoolOfferingsPrevious(ctx, tx, poolAddress, next[0].ID, 2)
	if err != nil {
		t.Fatalf("previous page: %v", err)
	}
	if len(prev) != 2 || prev[0].ID <= prev[1].ID {
		t.Fatalf("previous page did not restore descending order: %+v", prev)
	}
}
