package store

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests exercise the real market SQL against a live database. They are
// skipped unless CORE_TEST_DSN points at one, so `go test ./...` stays
// hermetic on machines without Postgres.
//
//	CORE_TEST_DSN=postgres://postgres:postgres@127.0.0.1:5432/clc_core go test ./internal/store/
func newMarketTestPg(t *testing.T) (*Pg, pgx.Tx, func()) {
	t.Helper()

	dsn := os.Getenv("CORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CORE_TEST_DSN not set; skipping live market voucher SQL tests")
	}

	queries, err := loadQueries("../../core_queries.sql")
	if err != nil {
		t.Fatalf("load core queries: %v", err)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		pool.Close()
		t.Fatalf("begin: %v", err)
	}

	return &Pg{queries: queries}, tx, func() {
		// Always roll back: these tests insert fixtures and must not persist.
		_ = tx.Rollback(ctx)
		pool.Close()
	}
}

// seedMarketVoucher inserts a voucher plus its offerings inside the test
// transaction and returns the new voucher ID.
func seedMarketVoucher(t *testing.T, tx pgx.Tx, lat, lng float64, active bool, commodityTypes ...string) uint64 {
	t.Helper()
	ctx := context.Background()

	var id uint64
	// A unique per-row address keeps the (chain_id, address) constraint happy.
	err := tx.QueryRow(ctx, `
		INSERT INTO vouchers (chain_id, address, name, symbol, decimals, active, geo_lat, geo_lng, location_name)
		VALUES (42220, '0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'),
		        'Market Fixture', 'FIX' || gen_random_uuid()::TEXT, 6, $1, $2, $3, 'Fixture Location')
		RETURNING id`, active, lat, lng).Scan(&id)
	if err != nil {
		t.Fatalf("seed voucher: %v", err)
	}

	for _, commodityType := range commodityTypes {
		if _, err := tx.Exec(ctx, `
			INSERT INTO offerings (voucher_id, seller_address, commodity_name, commodity_type, description, quantity, available)
			VALUES ($1, '0x0000000000000000000000000000000000000001', 'Fixture', $2, '', 1, TRUE)`,
			id, commodityType); err != nil {
			t.Fatalf("seed offering: %v", err)
		}
	}
	return id
}

func seedUnavailableOffering(t *testing.T, tx pgx.Tx, voucherID uint64, commodityType string) {
	t.Helper()
	if _, err := tx.Exec(context.Background(), `
		INSERT INTO offerings (voucher_id, seller_address, commodity_name, commodity_type, description, quantity, available)
		VALUES ($1, '0x0000000000000000000000000000000000000002', 'Unavailable', $2, '', 1, FALSE)`,
		voucherID, commodityType); err != nil {
		t.Fatalf("seed unavailable offering: %v", err)
	}
}

func kindOf(t *testing.T, pg *Pg, tx pgx.Tx, id uint64) string {
	t.Helper()
	// A one-row window around the fixture ID isolates it from the real catalog.
	vouchers, err := pg.ListMarketVouchersNext(context.Background(), tx, nil, id+1, 1)
	if err != nil {
		t.Fatalf("list market vouchers: %v", err)
	}
	if len(vouchers) != 1 || vouchers[0].ID != id {
		t.Fatalf("expected exactly the fixture voucher %d, got %+v", id, vouchers)
	}
	return vouchers[0].Kind
}

func TestMarketVoucherKindDerivation(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()

	t.Run("no offerings falls back to voucher", func(t *testing.T) {
		id := seedMarketVoucher(t, tx, -1.29, 36.82, true)
		if got := kindOf(t, pg, tx, id); got != "voucher" {
			t.Errorf("kind = %q, want voucher", got)
		}
	})

	t.Run("dominant GOOD becomes goods", func(t *testing.T) {
		id := seedMarketVoucher(t, tx, -1.29, 36.82, true, "GOOD", "GOOD", "SERVICE")
		if got := kindOf(t, pg, tx, id); got != "goods" {
			t.Errorf("kind = %q, want goods", got)
		}
	})

	t.Run("dominant SERVICE becomes services", func(t *testing.T) {
		id := seedMarketVoucher(t, tx, -1.29, 36.82, true, "SERVICE", "SERVICE", "GOOD")
		if got := kindOf(t, pg, tx, id); got != "services" {
			t.Errorf("kind = %q, want services", got)
		}
	})

	t.Run("a GOOD SERVICE tie resolves to goods", func(t *testing.T) {
		id := seedMarketVoucher(t, tx, -1.29, 36.82, true, "GOOD", "SERVICE")
		if got := kindOf(t, pg, tx, id); got != "goods" {
			t.Errorf("kind = %q, want goods on a tie", got)
		}
	})

	t.Run("unavailable offerings do not affect kind", func(t *testing.T) {
		id := seedMarketVoucher(t, tx, -1.29, 36.82, true, "GOOD")
		// Ten unavailable SERVICE rows must not outvote one available GOOD.
		for range 10 {
			seedUnavailableOffering(t, tx, id, "SERVICE")
		}
		if got := kindOf(t, pg, tx, id); got != "goods" {
			t.Errorf("kind = %q, want goods; unavailable offerings must be ignored", got)
		}
	})

	t.Run("only unavailable offerings falls back to voucher", func(t *testing.T) {
		id := seedMarketVoucher(t, tx, -1.29, 36.82, true)
		seedUnavailableOffering(t, tx, id, "GOOD")
		if got := kindOf(t, pg, tx, id); got != "voucher" {
			t.Errorf("kind = %q, want voucher", got)
		}
	})
}

func TestMarketVoucherFiltersInactiveAndUnlocated(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	inactive := seedMarketVoucher(t, tx, -1.29, 36.82, false, "GOOD")
	if rows, err := pg.ListMarketVouchersNext(ctx, tx, nil, inactive+1, 1); err != nil {
		t.Fatalf("list: %v", err)
	} else if len(rows) > 0 && rows[0].ID == inactive {
		t.Error("an inactive voucher must not appear in the market catalog")
	}

	var unlocated uint64
	if err := tx.QueryRow(ctx, `
		INSERT INTO vouchers (chain_id, address, name, symbol, decimals, active)
		VALUES (42220, '0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'),
		        'No Coordinates', 'NOGEO', 6, TRUE)
		RETURNING id`).Scan(&unlocated); err != nil {
		t.Fatalf("seed unlocated voucher: %v", err)
	}
	if rows, err := pg.ListMarketVouchersNext(ctx, tx, nil, unlocated+1, 1); err != nil {
		t.Fatalf("list: %v", err)
	} else if len(rows) > 0 && rows[0].ID == unlocated {
		t.Error("a voucher without coordinates must not appear in the market catalog")
	}
}

func TestMarketVoucherPagesAreDescendingAndKeysetStable(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	// Two full pages of its own. This paged the imported catalog before, which meant it asserted
	// nothing on a database that has none — the first page came back empty on CI's fresh one.
	for range 20 {
		seedMarketVoucher(t, tx, -1.29, 36.82, true)
	}

	first, err := pg.ListMarketVouchers(ctx, tx, nil, 10)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first) != 10 {
		t.Fatalf("first page returned %d rows, want 10", len(first))
	}
	assertDescending(t, "first", first)

	next, err := pg.ListMarketVouchersNext(ctx, tx, nil, first[len(first)-1].ID, 10)
	if err != nil {
		t.Fatalf("next page: %v", err)
	}
	if len(next) != 10 {
		t.Fatalf("next page returned %d rows, want 10", len(next))
	}
	assertDescending(t, "next", next)
	if next[0].ID >= first[len(first)-1].ID {
		t.Errorf("next page must start strictly below the previous cursor: %d >= %d", next[0].ID, first[len(first)-1].ID)
	}

	// Paging back from the next page's head must restore the first page.
	prev, err := pg.ListMarketVouchersPrevious(ctx, tx, nil, next[0].ID, 10)
	if err != nil {
		t.Fatalf("previous page: %v", err)
	}
	assertDescending(t, "previous", prev)
	if len(prev) != len(first) {
		t.Fatalf("previous page returned %d rows, want %d", len(prev), len(first))
	}
	for i := range prev {
		if prev[i].ID != first[i].ID {
			t.Fatalf("previous page row %d = %d, want %d", i, prev[i].ID, first[i].ID)
		}
	}
}

func TestMarketVoucherBoundedPagesComposeWithTheCursor(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	// Roughly Kenya.
	bounds := &MarketVoucherBounds{MinLng: 33.9, MinLat: -4.7, MaxLng: 41.9, MaxLat: 5.1}

	first, err := pg.ListMarketVouchers(ctx, tx, bounds, 25)
	if err != nil {
		t.Fatalf("bounded first page: %v", err)
	}
	if len(first) == 0 {
		t.Skip("no vouchers inside the test viewport")
	}
	assertDescending(t, "bounded first", first)
	assertInsideBounds(t, first, bounds)

	next, err := pg.ListMarketVouchersNext(ctx, tx, bounds, first[len(first)-1].ID, 25)
	if err != nil {
		t.Fatalf("bounded next page: %v", err)
	}
	assertDescending(t, "bounded next", next)
	assertInsideBounds(t, next, bounds)
	for _, row := range next {
		if row.ID >= first[len(first)-1].ID {
			t.Errorf("bounded next page leaked row %d at or above the cursor", row.ID)
		}
	}

	// A viewport far from any fixture yields an empty page, never an error.
	empty, err := pg.ListMarketVouchers(ctx, tx, &MarketVoucherBounds{
		MinLng: -179.9, MinLat: -89.9, MaxLng: -179.8, MaxLat: -89.8,
	}, 25)
	if err != nil {
		t.Fatalf("empty bounded page: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("expected an empty page in open ocean, got %d rows", len(empty))
	}
}

func assertDescending(t *testing.T, label string, vouchers []MarketVoucher) {
	t.Helper()
	for i := 1; i < len(vouchers); i++ {
		if vouchers[i-1].ID <= vouchers[i].ID {
			t.Errorf("%s page is not strictly descending at %d: %d <= %d",
				label, i, vouchers[i-1].ID, vouchers[i].ID)
		}
	}
	for _, v := range vouchers {
		if v.Kind != "goods" && v.Kind != "services" && v.Kind != "voucher" {
			t.Errorf("%s page carried unexpected kind %q", label, v.Kind)
		}
	}
}

func assertInsideBounds(t *testing.T, vouchers []MarketVoucher, bounds *MarketVoucherBounds) {
	t.Helper()
	for _, v := range vouchers {
		if v.GeoLng < bounds.MinLng || v.GeoLng > bounds.MaxLng ||
			v.GeoLat < bounds.MinLat || v.GeoLat > bounds.MaxLat {
			t.Errorf("voucher %d at (%f, %f) is outside the requested box", v.ID, v.GeoLng, v.GeoLat)
		}
	}
}
