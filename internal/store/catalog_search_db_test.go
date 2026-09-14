package store

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Live-database coverage for catalog search and tag filtering. Skipped unless
// CORE_TEST_DSN points at a database:
//
//	CORE_TEST_DSN=postgres://postgres:postgres@127.0.0.1:5432/clc_core go test ./internal/store/
//
// Every test runs inside a transaction that is always rolled back, so fixtures
// never persist. Migrations are applied first (they commit, and are idempotent)
// because the plan assertions need migration 011's trigram indexes to exist.

// searchToken is a nonsense string no imported voucher or pool name contains.
// Fixture names embed it so a search matches only this test's rows and never the
// surrounding real catalog; each test asserts the token starts with zero matches.
//
// It must not be a substring of the api package's token, and vice versa. Those
// tests commit their fixtures, `go test ./...` runs the two packages
// concurrently, and search is a substring match — so an overlapping token makes
// one package's fixtures visible to the other's assertions.
const (
	searchToken = "Zqvxstore"
)

// newCatalogTestPool connects and applies migrations, which is where the trigram
// and reverse-tag indexes these tests read come from.
func newCatalogTestPool(t *testing.T) (*Pg, *pgxpool.Pool) {
	t.Helper()

	dsn := os.Getenv("CORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CORE_TEST_DSN not set; skipping live catalog search SQL tests")
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

	if err := runMigrations(pool, "../../migrations"); err != nil {
		pool.Close()
		t.Fatalf("run migrations: %v", err)
	}

	return &Pg{queries: queries}, pool
}

func beginCatalogTestTx(t *testing.T, pool *pgxpool.Pool) (pgx.Tx, func()) {
	t.Helper()

	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		pool.Close()
		t.Fatalf("begin: %v", err)
	}

	return tx, func() {
		_ = tx.Rollback(ctx)
		pool.Close()
	}
}

func newCatalogSearchTestPg(t *testing.T) (*Pg, pgx.Tx, func()) {
	t.Helper()

	pg, pool := newCatalogTestPool(t)
	tx, cleanup := beginCatalogTestTx(t, pool)
	return pg, tx, cleanup
}

// dropIndexesForScaleSeeding removes the named indexes inside the test
// transaction, returning a function that recreates them from their captured
// definitions.
//
// The plan tests need PostgreSQL's view of an index to match the data they
// seeded, and seeding inside a rolled-back transaction cannot achieve that any
// other way. Two things go wrong if the migration-created index is left in place
// while 11,000 rows are inserted into it:
//
//   - pg_class.relpages and reltuples for an index are maintained by VACUUM, not
//     by ANALYZE, so they keep describing the pre-seed index. The planner
//     believed a 111-page index was 46 pages and mispriced every GIN scan.
//   - New GIN entries land in the pending list, whose size the cost estimator
//     reads from the index metapage. A bulk insert left hundreds of unflushed
//     pages, making a GIN scan look expensive enough to lose to a sequential
//     scan. Whether it did depended on how much autovacuum had flushed since the
//     last run, so the same assertion passed or failed run to run.
//
// Dropping first and rebuilding after the insert gives the accurate statistics
// and empty pending list that a production index has once autovacuum has run.
// It also keeps the test database usable: the rebuilt index is discarded by the
// rollback, and the bulk insert never reaches the original, so its pages do not
// accumulate. Left in place, the trigram indexes grew from 368 kB to 9,200 kB
// across a handful of runs — on a 1,168 kB table. With this, a full suite run
// adds about 57 kB, which is ordinary churn from the api package's
// committed-then-deleted fixtures.
//
// Definitions are read back from pg_indexes rather than restated here, so the
// tests always rebuild exactly what migration 011 created.
func dropIndexesForScaleSeeding(t *testing.T, tx pgx.Tx, indexes ...string) func() {
	t.Helper()
	ctx := context.Background()

	definitions := make([]string, 0, len(indexes))
	for _, index := range indexes {
		var definition string
		if err := tx.QueryRow(ctx,
			"SELECT indexdef FROM pg_indexes WHERE indexname = $1", index).Scan(&definition); err != nil {
			t.Fatalf("read definition of %s (has migration 011 run?): %v", index, err)
		}
		definitions = append(definitions, definition)

		// The name comes from the caller's own constant list, never from data.
		if _, err := tx.Exec(ctx, "DROP INDEX "+index); err != nil {
			t.Fatalf("drop %s: %v", index, err)
		}
	}

	return func() {
		t.Helper()
		for _, definition := range definitions {
			if _, err := tx.Exec(ctx, definition); err != nil {
				t.Fatalf("rebuild index: %v", err)
			}
		}
	}
}

// seedSearchVoucher inserts an active, geolocated voucher with the given name
// and symbol and returns its ID.
func seedSearchVoucher(t *testing.T, tx pgx.Tx, name, symbol string) uint64 {
	t.Helper()

	var id uint64
	err := tx.QueryRow(context.Background(), `
		INSERT INTO vouchers (chain_id, address, name, symbol, decimals, active, geo_lat, geo_lng, location_name)
		VALUES (42220, '0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'),
		        $1, $2, 6, TRUE, -1.29, 36.82, 'Search Fixture')
		RETURNING id`, name, symbol).Scan(&id)
	if err != nil {
		t.Fatalf("seed voucher %q: %v", name, err)
	}
	return id
}

// seedTaggedOffering attaches an offering carrying the given tags to a voucher.
// `available` is explicit because derived voucher tags must only come from
// available offerings.
func seedTaggedOffering(t *testing.T, tx pgx.Tx, voucherID uint64, available bool, tags ...string) uint64 {
	t.Helper()
	ctx := context.Background()

	var offeringID uint64
	if err := tx.QueryRow(ctx, `
		INSERT INTO offerings (voucher_id, seller_address, commodity_name, commodity_type, description, quantity, available)
		VALUES ($1, '0x0000000000000000000000000000000000000001', 'Search Fixture', 'GOOD', '', 1, $2)
		RETURNING id`, voucherID, available).Scan(&offeringID); err != nil {
		t.Fatalf("seed offering: %v", err)
	}

	for _, tag := range tags {
		if _, err := tx.Exec(ctx,
			`INSERT INTO offering_tags (offering_id, tag_value) VALUES ($1, $2)`,
			offeringID, tag); err != nil {
			t.Fatalf("seed offering tag %q: %v", tag, err)
		}
	}
	return offeringID
}

func mustFilter(t *testing.T, term string, tags []string, match TagMatch) CatalogFilter {
	t.Helper()
	filter, err := NewCatalogFilter(term, tags, match)
	if err != nil {
		t.Fatalf("NewCatalogFilter(%q, %v): %v", term, tags, err)
	}
	return filter
}

// listAllFiltered pages through every match so a test can assert on the whole
// filtered set rather than one page.
func listAllFiltered(t *testing.T, pg *Pg, tx pgx.Tx, filter CatalogFilter) []MarketVoucher {
	t.Helper()

	vouchers, err := pg.ListMarketVouchersFiltered(
		context.Background(), tx, nil, filter, CatalogFirstPage, 0, 500)
	if err != nil {
		t.Fatalf("list filtered: %v", err)
	}
	return vouchers
}

func voucherIDs(vouchers []MarketVoucher) []uint64 {
	ids := make([]uint64, 0, len(vouchers))
	for _, v := range vouchers {
		ids = append(ids, v.ID)
	}
	return ids
}

// assertTokenIsUnseeded proves the search token matches nothing in the real
// catalog, so every later assertion is about this test's fixtures alone.
func assertTokenIsUnseeded(t *testing.T, pg *Pg, tx pgx.Tx) {
	t.Helper()
	if got := listAllFiltered(t, pg, tx, mustFilter(t, searchToken, nil, TagMatchAny)); len(got) != 0 {
		t.Fatalf("search token %q already matches %d catalog rows; pick another", searchToken, len(got))
	}
}

func TestCatalogSearchMatchesNamesAndSymbols(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()

	assertTokenIsUnseeded(t, pg, tx)

	alpha := seedSearchVoucher(t, tx, searchToken+" Alpha Farm", "AAA")
	beta := seedSearchVoucher(t, tx, "Beta Collective", searchToken+"BETA")
	// Name and symbol both match: the row must appear once, not twice. The two
	// LIKE predicates are OR'd, so a bitmap plan could otherwise double it.
	both := seedSearchVoucher(t, tx, searchToken+" Both", searchToken+"BOTH")

	t.Run("matches the name", func(t *testing.T) {
		got := voucherIDs(listAllFiltered(t, pg, tx, mustFilter(t, searchToken+" Alpha", nil, TagMatchAny)))
		if !slices.Equal(got, []uint64{alpha}) {
			t.Errorf("ids = %v, want [%d]", got, alpha)
		}
	})

	t.Run("matches the symbol", func(t *testing.T) {
		got := voucherIDs(listAllFiltered(t, pg, tx, mustFilter(t, searchToken+"BETA", nil, TagMatchAny)))
		if !slices.Equal(got, []uint64{beta}) {
			t.Errorf("ids = %v, want [%d]", got, beta)
		}
	})

	t.Run("an equal name and symbol yields one row", func(t *testing.T) {
		got := voucherIDs(listAllFiltered(t, pg, tx, mustFilter(t, searchToken+"BOTH", nil, TagMatchAny)))
		if !slices.Equal(got, []uint64{both}) {
			t.Errorf("ids = %v, want exactly [%d] with no duplicate", got, both)
		}
	})

	t.Run("is case insensitive", func(t *testing.T) {
		lower := voucherIDs(listAllFiltered(t, pg, tx, mustFilter(t, strings.ToLower(searchToken)+" alpha", nil, TagMatchAny)))
		upper := voucherIDs(listAllFiltered(t, pg, tx, mustFilter(t, strings.ToUpper(searchToken)+" ALPHA", nil, TagMatchAny)))
		if !slices.Equal(lower, []uint64{alpha}) || !slices.Equal(upper, []uint64{alpha}) {
			t.Errorf("case changed the result: lower=%v upper=%v, want [%d]", lower, upper, alpha)
		}
	})

	t.Run("a common term returns every match", func(t *testing.T) {
		got := voucherIDs(listAllFiltered(t, pg, tx, mustFilter(t, searchToken, nil, TagMatchAny)))
		slices.Sort(got)
		want := []uint64{alpha, beta, both}
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("ids = %v, want %v", got, want)
		}
	})

	t.Run("a no-match term returns an empty page, not an error", func(t *testing.T) {
		got := listAllFiltered(t, pg, tx, mustFilter(t, searchToken+"NOSUCHTHING", nil, TagMatchAny))
		if len(got) != 0 {
			t.Errorf("expected no matches, got %v", voucherIDs(got))
		}
	})

	t.Run("whitespace is normalized, not searched", func(t *testing.T) {
		got := voucherIDs(listAllFiltered(t, pg, tx, mustFilter(t, "  "+searchToken+"   Alpha  ", nil, TagMatchAny)))
		if !slices.Equal(got, []uint64{alpha}) {
			t.Errorf("ids = %v, want [%d]", got, alpha)
		}
	})
}

// A two-character term matches as a prefix, so it must not match the same string
// in the middle of a name.
func TestCatalogSearchTwoCharacterTermIsPrefixOnly(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()

	// Symbols are distinctive so the prefix cannot match through them.
	leading := seedSearchVoucher(t, tx, "Zq "+searchToken+" leading", searchToken+"L")
	seedSearchVoucher(t, tx, searchToken+" trailing Zq", searchToken+"T")

	got := voucherIDs(listAllFiltered(t, pg, tx, mustFilter(t, "Zq", nil, TagMatchAny)))
	if !slices.Contains(got, leading) {
		t.Errorf("a two-character term must match a name prefix; ids = %v, want to contain %d", got, leading)
	}

	// The trailing fixture holds "Zq" only in the middle of its name, so a
	// prefix match must exclude it.
	trailingMatches := voucherIDs(listAllFiltered(t, pg, tx, mustFilter(t, "Zq "+searchToken+" leading", nil, TagMatchAny)))
	if !slices.Equal(trailingMatches, []uint64{leading}) {
		t.Errorf("ids = %v, want [%d]", trailingMatches, leading)
	}
}

// LIKE metacharacters in the term must be literals. Without escaping, "%" would
// match everything and "_" any single character, widening the query silently.
func TestCatalogSearchTreatsLikeMetacharactersAsLiterals(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()

	assertTokenIsUnseeded(t, pg, tx)

	percent := seedSearchVoucher(t, tx, searchToken+" 50% Discount", "PCT")
	underscore := seedSearchVoucher(t, tx, searchToken+" a_b Group", "USC")
	plain := seedSearchVoucher(t, tx, searchToken+" axb Group", "PLN")

	t.Run("percent is a literal", func(t *testing.T) {
		got := voucherIDs(listAllFiltered(t, pg, tx, mustFilter(t, "50%", nil, TagMatchAny)))
		if !slices.Equal(got, []uint64{percent}) {
			t.Errorf("ids = %v, want [%d]; an unescaped %% would match the whole catalog", got, percent)
		}
	})

	t.Run("underscore is a literal", func(t *testing.T) {
		got := voucherIDs(listAllFiltered(t, pg, tx, mustFilter(t, "a_b", nil, TagMatchAny)))
		if !slices.Equal(got, []uint64{underscore}) {
			t.Errorf("ids = %v, want [%d]; an unescaped _ would also match %d (axb)", got, underscore, plain)
		}
	})

	t.Run("a lone percent matches nothing rather than everything", func(t *testing.T) {
		got := listAllFiltered(t, pg, tx, mustFilter(t, "%"+searchToken, nil, TagMatchAny))
		if len(got) != 0 {
			t.Errorf("expected no literal match for %q, got %v", "%"+searchToken, voucherIDs(got))
		}
	})

	t.Run("a backslash is a literal", func(t *testing.T) {
		escaped := seedSearchVoucher(t, tx, searchToken+` back\slash`, "BSL")
		got := voucherIDs(listAllFiltered(t, pg, tx, mustFilter(t, `back\slash`, nil, TagMatchAny)))
		if !slices.Equal(got, []uint64{escaped}) {
			t.Errorf("ids = %v, want [%d]", got, escaped)
		}
	})
}

func TestCatalogVoucherTagSemantics(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()

	assertTokenIsUnseeded(t, pg, tx)

	artOnly := seedSearchVoucher(t, tx, searchToken+" ArtOnly", "AO")
	seedTaggedOffering(t, tx, artOnly, true, "Art")

	cultureOnly := seedSearchVoucher(t, tx, searchToken+" CultureOnly", "CO")
	seedTaggedOffering(t, tx, cultureOnly, true, "Culture")

	both := seedSearchVoucher(t, tx, searchToken+" Both", "BO")
	seedTaggedOffering(t, tx, both, true, "Art", "Culture")

	// Both tags, but split across two offerings: an all-tags query must still
	// match, because the semantics are "this voucher offers both", not "one
	// offering carries both".
	split := seedSearchVoucher(t, tx, searchToken+" Split", "SP")
	seedTaggedOffering(t, tx, split, true, "Art")
	seedTaggedOffering(t, tx, split, true, "Culture")

	// Tags on an unavailable offering are not the voucher's tags.
	hidden := seedSearchVoucher(t, tx, searchToken+" Hidden", "HI")
	seedTaggedOffering(t, tx, hidden, false, "Art", "Culture")

	// Repeated tag across several available offerings must not satisfy an
	// all-tags query for two distinct tags: that is what COUNT(DISTINCT) guards.
	repeated := seedSearchVoucher(t, tx, searchToken+" Repeated", "RE")
	seedTaggedOffering(t, tx, repeated, true, "Art")
	seedTaggedOffering(t, tx, repeated, true, "Art")
	seedTaggedOffering(t, tx, repeated, true, "Art")

	untagged := seedSearchVoucher(t, tx, searchToken+" Untagged", "UN")
	seedTaggedOffering(t, tx, untagged, true)

	matched := func(tags []string, match TagMatch) []uint64 {
		ids := voucherIDs(listAllFiltered(t, pg, tx, mustFilter(t, searchToken, tags, match)))
		slices.Sort(ids)
		return ids
	}
	sorted := func(ids ...uint64) []uint64 {
		slices.Sort(ids)
		return ids
	}

	t.Run("any matches at least one tag", func(t *testing.T) {
		got := matched([]string{"Art", "Culture"}, TagMatchAny)
		want := sorted(artOnly, cultureOnly, both, split, repeated)
		if !slices.Equal(got, want) {
			t.Errorf("any: ids = %v, want %v", got, want)
		}
	})

	t.Run("all requires every tag", func(t *testing.T) {
		got := matched([]string{"Art", "Culture"}, TagMatchAll)
		want := sorted(both, split)
		if !slices.Equal(got, want) {
			t.Errorf("all: ids = %v, want %v", got, want)
		}
	})

	t.Run("a repeated tag does not satisfy all", func(t *testing.T) {
		if got := matched([]string{"Art", "Culture"}, TagMatchAll); slices.Contains(got, repeated) {
			t.Errorf("a voucher carrying only Art on three offerings must not match all[Art,Culture]: %v", got)
		}
	})

	t.Run("unavailable offerings do not contribute tags", func(t *testing.T) {
		for _, match := range []TagMatch{TagMatchAny, TagMatchAll} {
			if got := matched([]string{"Art", "Culture"}, match); slices.Contains(got, hidden) {
				t.Errorf("tags on an unavailable offering must not match: %v", got)
			}
		}
	})

	t.Run("a single tag matches identically under any and all", func(t *testing.T) {
		matchAny := matched([]string{"Art"}, TagMatchAny)
		matchAll := matched([]string{"Art"}, TagMatchAll)
		if !slices.Equal(matchAny, matchAll) {
			t.Errorf("one tag: any = %v, all = %v; they must agree", matchAny, matchAll)
		}
	})

	t.Run("an untagged voucher never matches a tag filter", func(t *testing.T) {
		if got := matched([]string{"Art"}, TagMatchAny); slices.Contains(got, untagged) {
			t.Errorf("an untagged voucher matched a tag filter: %v", got)
		}
	})

	t.Run("search and tags compose as AND", func(t *testing.T) {
		// Same tag, but a term that only one fixture's name satisfies.
		got := voucherIDs(listAllFiltered(t, pg, tx,
			mustFilter(t, searchToken+" ArtOnly", []string{"Art"}, TagMatchAny)))
		if !slices.Equal(got, []uint64{artOnly}) {
			t.Errorf("ids = %v, want [%d]", got, artOnly)
		}
		// The term matches but the tag does not: no rows.
		none := listAllFiltered(t, pg, tx,
			mustFilter(t, searchToken+" CultureOnly", []string{"Art"}, TagMatchAny))
		if len(none) != 0 {
			t.Errorf("search and tags must both apply; got %v", voucherIDs(none))
		}
	})
}

// Every paging direction must apply every active predicate. A direction that
// fell through to an unfiltered statement would silently widen the result.
func TestCatalogSearchPagesApplyEveryFilterInEveryDirection(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()

	assertTokenIsUnseeded(t, pg, tx)

	// Nine tagged fixtures, plus decoys that match the term but not the tag and
	// the tag but not the term. Neither decoy may ever appear.
	var tagged []uint64
	for i := range 9 {
		id := seedSearchVoucher(t, tx, fmt.Sprintf("%s Page %d", searchToken, i), fmt.Sprintf("PG%d", i))
		seedTaggedOffering(t, tx, id, true, "Art")
		tagged = append(tagged, id)
	}
	termOnlyDecoy := seedSearchVoucher(t, tx, searchToken+" Untagged Decoy", "DEC1")
	seedTaggedOffering(t, tx, termOnlyDecoy, true, "Culture")
	tagOnlyDecoy := seedSearchVoucher(t, tx, "Unrelated Name Decoy", "DEC2")
	seedTaggedOffering(t, tx, tagOnlyDecoy, true, "Art")

	filter := mustFilter(t, searchToken, []string{"Art"}, TagMatchAny)
	allowed := make(map[uint64]bool, len(tagged))
	for _, id := range tagged {
		allowed[id] = true
	}

	assertOnlyAllowed := func(label string, rows []MarketVoucher) {
		for _, row := range rows {
			if !allowed[row.ID] {
				t.Errorf("%s leaked voucher %d, which does not satisfy both the term and the tag", label, row.ID)
			}
		}
	}

	// Walk forward in pages of four and collect everything.
	const perPage = 4
	var walked []uint64
	page, err := pg.ListMarketVouchersFiltered(ctx, tx, nil, filter, CatalogFirstPage, 0, perPage)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	assertOnlyAllowed("first page", page)
	if len(page) != perPage {
		t.Fatalf("first page returned %d rows, want %d", len(page), perPage)
	}
	walked = append(walked, voucherIDs(page)...)

	for len(page) == perPage {
		next, nErr := pg.ListMarketVouchersFiltered(ctx, tx, nil, filter, CatalogNextPage, page[len(page)-1].ID, perPage)
		if nErr != nil {
			t.Fatalf("next page: %v", nErr)
		}
		assertOnlyAllowed("next page", next)
		for _, row := range next {
			if row.ID >= page[len(page)-1].ID {
				t.Errorf("next page row %d is at or above the cursor %d", row.ID, page[len(page)-1].ID)
			}
		}
		walked = append(walked, voucherIDs(next)...)
		page = next
	}

	// No duplicates and no skips across the walk.
	if len(walked) != len(tagged) {
		t.Errorf("walked %d rows across pages, want %d (%v vs %v)", len(walked), len(tagged), walked, tagged)
	}
	deduped := slices.Clone(walked)
	slices.Sort(deduped)
	deduped = slices.Compact(deduped)
	if len(deduped) != len(walked) {
		t.Errorf("paging returned duplicate rows: %v", walked)
	}
	wantAll := slices.Clone(tagged)
	slices.Sort(wantAll)
	if !slices.Equal(deduped, wantAll) {
		t.Errorf("paged set = %v, want %v", deduped, wantAll)
	}
	if !slices.IsSortedFunc(walked, func(a, b uint64) int {
		switch {
		case a > b:
			return -1
		case a < b:
			return 1
		default:
			return 0
		}
	}) {
		t.Errorf("paged rows are not in descending ID order: %v", walked)
	}

	// Paging back from the second page's head must restore the first page
	// exactly, with the filters still applied.
	second, err := pg.ListMarketVouchersFiltered(ctx, tx, nil, filter, CatalogNextPage, walked[perPage-1], perPage)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	previous, err := pg.ListMarketVouchersFiltered(ctx, tx, nil, filter, CatalogPrevPage, second[0].ID, perPage)
	if err != nil {
		t.Fatalf("previous page: %v", err)
	}
	assertOnlyAllowed("previous page", previous)
	if got := voucherIDs(previous); !slices.Equal(got, walked[:perPage]) {
		t.Errorf("previous page = %v, want the first page %v", got, walked[:perPage])
	}
}

// With no filter, the composed path must not be used at all: the request goes to
// the existing static statements, so the map's measured plans are untouched.
func TestUnfilteredRequestsUseTheStaticStatements(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()

	empty := CatalogFilter{}
	if empty.Active() {
		t.Fatal("an empty filter must not be active")
	}

	bounds := &MarketVoucherBounds{MinLng: 33.9, MinLat: -4.7, MaxLng: 41.9, MaxLat: 5.1}
	for label, tc := range map[string]struct {
		bounds *MarketVoucherBounds
		page   CatalogPage
		cursor uint64
	}{
		"unbounded first": {bounds: nil, page: CatalogFirstPage},
		"unbounded next":  {bounds: nil, page: CatalogNextPage, cursor: 2000},
		"unbounded prev":  {bounds: nil, page: CatalogPrevPage, cursor: 100},
		"bounded first":   {bounds: bounds, page: CatalogFirstPage},
		"bounded next":    {bounds: bounds, page: CatalogNextPage, cursor: 2000},
		"bounded prev":    {bounds: bounds, page: CatalogPrevPage, cursor: 100},
	} {
		viaFilter, err := pg.ListMarketVouchersFiltered(ctx, tx, tc.bounds, empty, tc.page, tc.cursor, 25)
		if err != nil {
			t.Fatalf("%s via filter: %v", label, err)
		}

		var direct []MarketVoucher
		switch tc.page {
		case CatalogFirstPage:
			direct, err = pg.ListMarketVouchers(ctx, tx, tc.bounds, 25)
		case CatalogNextPage:
			direct, err = pg.ListMarketVouchersNext(ctx, tx, tc.bounds, tc.cursor, 25)
		case CatalogPrevPage:
			direct, err = pg.ListMarketVouchersPrevious(ctx, tx, tc.bounds, tc.cursor, 25)
		}
		if err != nil {
			t.Fatalf("%s direct: %v", label, err)
		}

		if !slices.Equal(voucherIDs(viaFilter), voucherIDs(direct)) {
			t.Errorf("%s: filtered entry point returned %v, static statement returned %v",
				label, voucherIDs(viaFilter), voucherIDs(direct))
		}
	}
}

// A facet count must equal the number of rows the same filter actually returns,
// or the UI promises a result set the query will not produce.
func TestVoucherTagFacetCountsMatchResultQueries(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()

	assertTokenIsUnseeded(t, pg, tx)

	// Art: 3 vouchers (one of them via two offerings, which must count once).
	// Culture: 1. Mobility: 0 among the fixtures.
	for _, tags := range [][]string{{"Art"}, {"Art"}, {"Art", "Culture"}} {
		id := seedSearchVoucher(t, tx, searchToken+" Facet", "FCT")
		seedTaggedOffering(t, tx, id, true, tags...)
	}
	fanout := seedSearchVoucher(t, tx, searchToken+" Fanout", "FAN")
	seedTaggedOffering(t, tx, fanout, true, "Art")
	seedTaggedOffering(t, tx, fanout, true, "Art")

	// Mobility must be *in the vocabulary* for a zero count to be expressible, and the vocabulary
	// is the tags on available offerings of visible vouchers. Its carrier is deliberately named
	// without the search token, so the term-scoped count is 0 while the value still exists.
	// Leaving this to the imported catalog passed locally and made the assertion unreachable on a
	// fresh database.
	unmatched := seedSearchVoucher(t, tx, "Outside The Facet Term", "OFT")
	seedTaggedOffering(t, tx, unmatched, true, "Mobility")

	// The facet is scoped by the search term, so only fixtures are counted.
	filter := mustFilter(t, searchToken, nil, TagMatchAny)
	facets, err := pg.ListMarketVoucherTagFacets(ctx, tx, nil, filter)
	if err != nil {
		t.Fatalf("facets: %v", err)
	}
	if len(facets) == 0 {
		t.Fatal("expected a non-empty facet vocabulary")
	}

	counts := map[string]uint64{}
	for _, facet := range facets {
		counts[facet.Tag] = facet.Count
	}
	if counts["Art"] != 4 {
		t.Errorf("Art count = %d, want 4 (the fan-out voucher counts once)", counts["Art"])
	}
	if counts["Culture"] != 1 {
		t.Errorf("Culture count = %d, want 1", counts["Culture"])
	}

	// Every facet count must equal the row count the same single-tag filter
	// returns.
	for _, facet := range facets {
		single := mustFilter(t, searchToken, []string{facet.Tag}, TagMatchAny)
		rows := listAllFiltered(t, pg, tx, single)
		if uint64(len(rows)) != facet.Count {
			t.Errorf("facet %q promised %d rows but the filter returned %d",
				facet.Tag, facet.Count, len(rows))
		}
	}

	// Zero-count values are represented rather than dropped, so the UI can grey
	// them out instead of hiding a filter that exists.
	zeroes := 0
	for _, facet := range facets {
		if facet.Count == 0 {
			zeroes++
		}
	}
	if zeroes == 0 {
		t.Error("expected at least one zero-count facet value to be represented")
	}
}

// Counts must be disjunctive: selecting one tag may not zero the others, or the
// user can never broaden their selection.
func TestVoucherTagFacetIsDisjunctiveAndKeepsSelectedValues(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()

	assertTokenIsUnseeded(t, pg, tx)

	art := seedSearchVoucher(t, tx, searchToken+" Art", "FA")
	seedTaggedOffering(t, tx, art, true, "Art")
	culture := seedSearchVoucher(t, tx, searchToken+" Culture", "FC")
	seedTaggedOffering(t, tx, culture, true, "Culture")

	unselected, err := pg.ListMarketVoucherTagFacets(ctx, tx, nil,
		mustFilter(t, searchToken, nil, TagMatchAny))
	if err != nil {
		t.Fatalf("facets: %v", err)
	}
	selected, err := pg.ListMarketVoucherTagFacets(ctx, tx, nil,
		mustFilter(t, searchToken, []string{"Art"}, TagMatchAny))
	if err != nil {
		t.Fatalf("facets with a selection: %v", err)
	}

	countsOf := func(facets []TagFacet) map[string]uint64 {
		counts := map[string]uint64{}
		for _, facet := range facets {
			counts[facet.Tag] = facet.Count
		}
		return counts
	}
	before, after := countsOf(unselected), countsOf(selected)

	if after["Culture"] != before["Culture"] {
		t.Errorf("selecting Art changed the Culture count from %d to %d; counts must be disjunctive",
			before["Culture"], after["Culture"])
	}
	if after["Culture"] == 0 {
		t.Error("Culture must keep a non-zero count while Art is selected, so the filter can be broadened")
	}

	// A valid selected value stays present even when nothing matches it, so the
	// control that set it does not vanish.
	withUnmatched, err := pg.ListMarketVoucherTagFacets(ctx, tx, nil,
		mustFilter(t, searchToken+"NOSUCHTHING", []string{"Mobility"}, TagMatchAny))
	if err != nil {
		t.Fatalf("facets with an unmatched selection: %v", err)
	}
	found := false
	for _, facet := range withUnmatched {
		if facet.Tag == "Mobility" {
			found = true
			if facet.Count != 0 {
				t.Errorf("Mobility count = %d, want 0", facet.Count)
			}
		}
	}
	if !found {
		t.Error("a selected tag must stay in the facet with a zero count")
	}
}

func TestPoolSearchAndTagFilters(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()

	seedPool := func(name, symbol string, tags ...string) uint64 {
		t.Helper()
		var id uint64
		if err := tx.QueryRow(ctx, `
			INSERT INTO pools (chain_id, address, name, symbol, decimals)
			VALUES (42220, '0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'), $1, $2, 6)
			RETURNING id`, name, symbol).Scan(&id); err != nil {
			t.Fatalf("seed pool %q: %v", name, err)
		}
		for _, tag := range tags {
			if _, err := tx.Exec(ctx,
				`INSERT INTO pool_tags (pool_id, tag_value) VALUES ($1, $2)`, id, tag); err != nil {
				t.Fatalf("seed pool tag %q: %v", tag, err)
			}
		}
		return id
	}

	poolIDs := func(pools []Pool) []uint64 {
		ids := make([]uint64, 0, len(pools))
		for _, p := range pools {
			ids = append(ids, p.ID)
		}
		slices.Sort(ids)
		return ids
	}
	list := func(filter CatalogFilter) []Pool {
		t.Helper()
		pools, err := pg.ListPoolsFiltered(ctx, tx, filter, CatalogFirstPage, 0, 100)
		if err != nil {
			t.Fatalf("list pools filtered: %v", err)
		}
		return pools
	}

	if got := list(mustFilter(t, searchToken, nil, TagMatchAny)); len(got) != 0 {
		t.Fatalf("search token %q already matches %d pools", searchToken, len(got))
	}

	art := seedPool(searchToken+" Art Pool", "QAP", "Art")
	culture := seedPool(searchToken+" Culture Pool", "QCP", "Culture")
	both := seedPool(searchToken+" Both Pool", "QBP", "Art", "Culture")
	untagged := seedPool(searchToken+" Plain Pool", "QPP")
	bySymbol := seedPool("Unrelated Pool", searchToken+"SYM")
	renamed := seedPool("Immutable Contract Pool", "RNP")
	if _, err := tx.Exec(ctx, `UPDATE pools SET display_name = $2 WHERE id = $1`,
		renamed, searchToken+" Renamed Pool"); err != nil {
		t.Fatalf("set pool display name: %v", err)
	}

	t.Run("matches names and symbols", func(t *testing.T) {
		got := poolIDs(list(mustFilter(t, searchToken, nil, TagMatchAny)))
		want := []uint64{art, culture, both, untagged, bySymbol, renamed}
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("ids = %v, want %v", got, want)
		}
	})

	t.Run("matches and returns the effective display name", func(t *testing.T) {
		pools := list(mustFilter(t, searchToken+" Renamed", nil, TagMatchAny))
		if len(pools) != 1 || pools[0].ID != renamed {
			t.Fatalf("display-name search returned %#v, want pool %d", poolIDs(pools), renamed)
		}
		if pools[0].Name == nil || *pools[0].Name != searchToken+" Renamed Pool" {
			t.Errorf("effective name = %#v, want %q", pools[0].Name, searchToken+" Renamed Pool")
		}
		if pools[0].ContractName == nil || *pools[0].ContractName != "Immutable Contract Pool" {
			t.Errorf("contract name = %#v, want Immutable Contract Pool", pools[0].ContractName)
		}
	})

	t.Run("direct tags filter with any and all", func(t *testing.T) {
		matchAny := poolIDs(list(mustFilter(t, searchToken, []string{"Art", "Culture"}, TagMatchAny)))
		wantAny := []uint64{art, culture, both}
		slices.Sort(wantAny)
		if !slices.Equal(matchAny, wantAny) {
			t.Errorf("any: ids = %v, want %v", matchAny, wantAny)
		}

		all := poolIDs(list(mustFilter(t, searchToken, []string{"Art", "Culture"}, TagMatchAll)))
		if !slices.Equal(all, []uint64{both}) {
			t.Errorf("all: ids = %v, want [%d]", all, both)
		}
	})

	t.Run("rows carry voucherCount and their own tags", func(t *testing.T) {
		pools := list(mustFilter(t, searchToken, []string{"Art"}, TagMatchAll))
		if len(pools) == 0 {
			t.Fatal("expected at least one tagged pool")
		}
		for _, pool := range pools {
			if !slices.Contains(pool.Tags, "Art") {
				t.Errorf("pool %d matched the Art filter but returned tags %v", pool.ID, pool.Tags)
			}
		}
	})

	t.Run("paging applies the filter in every direction", func(t *testing.T) {
		filter := mustFilter(t, searchToken, nil, TagMatchAny)
		allowed := map[uint64]bool{
			art: true, culture: true, both: true, untagged: true, bySymbol: true, renamed: true,
		}

		first, err := pg.ListPoolsFiltered(ctx, tx, filter, CatalogFirstPage, 0, 2)
		if err != nil {
			t.Fatalf("first page: %v", err)
		}
		if len(first) != 2 {
			t.Fatalf("first page returned %d rows, want 2", len(first))
		}
		next, err := pg.ListPoolsFiltered(ctx, tx, filter, CatalogNextPage, first[len(first)-1].ID, 2)
		if err != nil {
			t.Fatalf("next page: %v", err)
		}
		previous, err := pg.ListPoolsFiltered(ctx, tx, filter, CatalogPrevPage, next[0].ID, 2)
		if err != nil {
			t.Fatalf("previous page: %v", err)
		}

		for label, rows := range map[string][]Pool{"first": first, "next": next, "previous": previous} {
			for _, pool := range rows {
				if !allowed[pool.ID] {
					t.Errorf("%s page leaked unfiltered pool %d", label, pool.ID)
				}
			}
		}
		if got := poolIDs(previous); !slices.Equal(got, poolIDs(first)) {
			t.Errorf("previous page = %v, want the first page %v", got, poolIDs(first))
		}
	})

	t.Run("facet counts match the result queries", func(t *testing.T) {
		filter := mustFilter(t, searchToken, nil, TagMatchAny)
		facets, err := pg.ListPoolTagFacets(ctx, tx, filter)
		if err != nil {
			t.Fatalf("pool facets: %v", err)
		}
		for _, facet := range facets {
			rows := list(mustFilter(t, searchToken, []string{facet.Tag}, TagMatchAny))
			if uint64(len(rows)) != facet.Count {
				t.Errorf("pool facet %q promised %d rows but the filter returned %d",
					facet.Tag, facet.Count, len(rows))
			}
		}
	})

	t.Run("an unfiltered request matches the static statement", func(t *testing.T) {
		viaFilter, err := pg.ListPoolsFiltered(ctx, tx, CatalogFilter{}, CatalogFirstPage, 0, 10)
		if err != nil {
			t.Fatalf("via filter: %v", err)
		}
		direct, err := pg.ListPools(ctx, tx, 10)
		if err != nil {
			t.Fatalf("direct: %v", err)
		}
		if !slices.Equal(poolIDs(viaFilter), poolIDs(direct)) {
			t.Errorf("filtered entry point returned %v, static statement returned %v",
				poolIDs(viaFilter), poolIDs(direct))
		}
	})
}

func TestUnknownTokenTags(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()

	if unknown, err := pg.UnknownTokenTags(ctx, tx, nil); err != nil || len(unknown) != 0 {
		t.Errorf("no tags: unknown = %v, err = %v", unknown, err)
	}
	if unknown, err := pg.UnknownTokenTags(ctx, tx, []string{"Art", "Culture"}); err != nil || len(unknown) != 0 {
		t.Errorf("known tags: unknown = %v, err = %v", unknown, err)
	}

	unknown, err := pg.UnknownTokenTags(ctx, tx, []string{"Art", "NotATag", "art"})
	if err != nil {
		t.Fatalf("UnknownTokenTags: %v", err)
	}
	slices.Sort(unknown)
	// Comparison is exact: the vocabulary is served to clients verbatim, so a
	// case variant is a different value and not silently accepted.
	if !slices.Equal(unknown, []string{"NotATag", "art"}) {
		t.Errorf("unknown = %v, want [NotATag art]", unknown)
	}
}

// The filter-free facet skips the vocabulary statement, so the skip has to be
// proven equivalent to running it: same values, same order, and no zero counts,
// because a value only enters the vocabulary by being carried by something
// visible.
//
// This is the guard on that optimisation. If either half's visibility predicates
// are ever changed without the other's, the two sides stop describing the same
// set and this fails rather than silently truncating the tag row every signed-out
// client bootstraps from.
func TestFilterFreeFacetMatchesTheVocabularyItSkips(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()

	// A tag reachable only through the fixture, so the comparison covers a value
	// this test controls rather than only imported ones.
	visible := seedSearchVoucher(t, tx, searchToken+" FreeFacet", "FFC")
	seedTaggedOffering(t, tx, visible, true, "Art")

	facets, err := pg.ListMarketVoucherTagFacets(ctx, tx, nil, CatalogFilter{})
	if err != nil {
		t.Fatalf("filter-free facets: %v", err)
	}

	var vocabulary []string
	if err := pgxscan.Select(ctx, tx, &vocabulary, pg.queries.ListVoucherTagVocabulary); err != nil {
		t.Fatalf("vocabulary: %v", err)
	}

	got := make([]string, 0, len(facets))
	for _, facet := range facets {
		got = append(got, facet.Tag)
	}
	if !slices.Equal(got, vocabulary) {
		t.Errorf("filter-free facet values = %v, want the vocabulary %v", got, vocabulary)
	}

	for _, facet := range facets {
		if facet.Count == 0 {
			t.Errorf("filter-free facet carries a zero count for %q; a vocabulary value "+
				"with no visible carrier should not exist", facet.Tag)
		}
	}
	if len(facets) == 0 {
		t.Fatal("expected a non-empty filter-free facet")
	}
}

// A viewport narrows, so a bounded request is not filter-free even with no term
// and no tags: its counts describe the tags inside the camera. The vocabulary must
// still be complete, or panning the map would make chips disappear.
func TestBoundedFacetKeepsTheCompleteVocabulary(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()

	// Fixtures sit at (-1.29, 36.82); this box is nowhere near them, so every
	// count inside it is zero while the vocabulary is unaffected.
	elsewhere := &MarketVoucherBounds{MinLng: 100, MinLat: 40, MaxLng: 101, MaxLat: 41}

	visible := seedSearchVoucher(t, tx, searchToken+" Bounded", "BND")
	seedTaggedOffering(t, tx, visible, true, "Art")

	facets, err := pg.ListMarketVoucherTagFacets(ctx, tx, elsewhere, CatalogFilter{})
	if err != nil {
		t.Fatalf("bounded facets: %v", err)
	}

	var vocabulary []string
	if err := pgxscan.Select(ctx, tx, &vocabulary, pg.queries.ListVoucherTagVocabulary); err != nil {
		t.Fatalf("vocabulary: %v", err)
	}

	got := make([]string, 0, len(facets))
	for _, facet := range facets {
		got = append(got, facet.Tag)
		if facet.Count != 0 {
			t.Errorf("facet %q counts %d inside a box containing no vouchers",
				facet.Tag, facet.Count)
		}
	}
	if !slices.Equal(got, vocabulary) {
		t.Errorf("bounded facet values = %v, want the complete vocabulary %v", got, vocabulary)
	}
}

func TestCatalogStatementTimeoutIsTransactionLocal(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()

	var before string
	if err := tx.QueryRow(ctx, "SHOW statement_timeout").Scan(&before); err != nil {
		t.Fatalf("read statement_timeout: %v", err)
	}

	if err := pg.SetCatalogStatementTimeout(ctx, tx); err != nil {
		t.Fatalf("SetCatalogStatementTimeout: %v", err)
	}

	var after string
	if err := tx.QueryRow(ctx, "SHOW statement_timeout").Scan(&after); err != nil {
		t.Fatalf("read statement_timeout: %v", err)
	}
	if after == before {
		t.Errorf("statement_timeout unchanged (%q); the catalog read is unbounded", after)
	}
	if after != catalogStatementTimeout {
		t.Errorf("statement_timeout = %q, want %q", after, catalogStatementTimeout)
	}

	// is_local = true: the setting must not survive into the next transaction
	// that reuses this pooled connection.
	if _, err := tx.Exec(ctx, "SAVEPOINT probe"); err != nil {
		t.Fatalf("savepoint: %v", err)
	}
}

// seedSearchReport inserts an approved report with the given title and description, plus its
// direct tags, and returns its id. Distinct from report_db_test.go's seedReport, which fixes the
// title and varies the status and timestamp instead.
func seedSearchReport(t *testing.T, tx pgx.Tx, title, description string, tags ...string) uint64 {
	t.Helper()
	ctx := context.Background()

	var id uint64
	if err := tx.QueryRow(ctx, `
		INSERT INTO reports (title, description, status, author_address, created_at, updated_at)
		VALUES ($1, $2, 'APPROVED', '0x0000000000000000000000000000000000000001', now(), now())
		RETURNING id`, title, description).Scan(&id); err != nil {
		t.Fatalf("seed report %q: %v", title, err)
	}

	for _, tag := range tags {
		if _, err := tx.Exec(ctx,
			`INSERT INTO report_tags (report_id, tag_value) VALUES ($1, $2)`, id, tag); err != nil {
			t.Fatalf("seed report tag %q: %v", tag, err)
		}
	}
	return id
}

func TestVoucherRelationshipFiltersForReportsAndPools(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()
	marker := searchToken + " Relationships"

	firstVoucher := seedSearchVoucher(t, tx, marker+" First Voucher", "RV1")
	secondVoucher := seedSearchVoucher(t, tx, marker+" Second Voucher", "RV2")

	var firstAddress, secondAddress string
	if err := tx.QueryRow(ctx, `SELECT address FROM vouchers WHERE id = $1`, firstVoucher).Scan(&firstAddress); err != nil {
		t.Fatalf("read first voucher address: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT address FROM vouchers WHERE id = $1`, secondVoucher).Scan(&secondAddress); err != nil {
		t.Fatalf("read second voucher address: %v", err)
	}
	resolved, err := pg.ResolveVoucherRefs(ctx, tx, []VoucherRef{
		{ChainID: 42220, Address: secondAddress},
		{ChainID: 42220, Address: firstAddress},
	})
	if err != nil {
		t.Fatalf("resolve voucher refs: %v", err)
	}
	wantResolved := []uint64{firstVoucher, secondVoucher}
	slices.Sort(wantResolved)
	if !slices.Equal(resolved, wantResolved) {
		t.Fatalf("resolved IDs = %v, want %v", resolved, wantResolved)
	}

	firstReport := seedSearchReport(t, tx, marker+" First Report", "x")
	bothReport := seedSearchReport(t, tx, marker+" Both Report", "x")
	secondReport := seedSearchReport(t, tx, marker+" Second Report", "x")
	for _, relation := range [][2]uint64{
		{firstReport, firstVoucher},
		{bothReport, firstVoucher},
		{bothReport, secondVoucher},
		{secondReport, secondVoucher},
	} {
		if _, err := tx.Exec(ctx,
			`INSERT INTO report_vouchers (report_id, voucher_id) VALUES ($1, $2)`,
			relation[0], relation[1]); err != nil {
			t.Fatalf("seed report voucher: %v", err)
		}
	}

	base := mustFilter(t, marker, nil, TagMatchAny)
	listReportIDs := func(ids []uint64, match TagMatch) []uint64 {
		t.Helper()
		filter, filterErr := base.WithVoucherIDs(ids, match)
		if filterErr != nil {
			t.Fatalf("report voucher filter: %v", filterErr)
		}
		reports, listErr := pg.ListReportsFiltered(ctx, tx, filter, ReportCursor{}, false, 100)
		if listErr != nil {
			t.Fatalf("list reports: %v", listErr)
		}
		got := make([]uint64, 0, len(reports))
		for _, report := range reports {
			got = append(got, report.ID)
		}
		slices.Sort(got)
		return got
	}
	if got, want := listReportIDs([]uint64{firstVoucher, secondVoucher}, TagMatchAny),
		[]uint64{firstReport, bothReport, secondReport}; !slices.Equal(got, want) {
		t.Errorf("report any IDs = %v, want %v", got, want)
	}
	if got := listReportIDs([]uint64{firstVoucher, secondVoucher}, TagMatchAll); !slices.Equal(got, []uint64{bothReport}) {
		t.Errorf("report all IDs = %v, want [%d]", got, bothReport)
	}

	reportFacets, err := pg.ListReportVoucherFacets(ctx, tx, base)
	if err != nil {
		t.Fatalf("report voucher facets: %v", err)
	}
	reportCounts := make(map[uint64]uint64, len(reportFacets))
	for _, facet := range reportFacets {
		reportCounts[facet.ID] = facet.Count
	}
	if reportCounts[firstVoucher] != 2 || reportCounts[secondVoucher] != 2 {
		t.Errorf("report voucher counts = %v, want both vouchers at 2", reportCounts)
	}

	seedPool := func(name string) uint64 {
		t.Helper()
		var id uint64
		if err := tx.QueryRow(ctx, `
			INSERT INTO pools (chain_id, address, name, symbol, decimals)
			VALUES (42220, '0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'), $1, 'RLP', 6)
			RETURNING id`, name).Scan(&id); err != nil {
			t.Fatalf("seed relationship pool: %v", err)
		}
		return id
	}
	firstPool := seedPool(marker + " First Pool")
	bothPool := seedPool(marker + " Both Pool")
	disallowedPool := seedPool(marker + " Disallowed Pool")
	for _, relation := range []struct {
		poolID, voucherID uint64
		allowed           bool
	}{
		{firstPool, firstVoucher, true},
		{bothPool, firstVoucher, true},
		{bothPool, secondVoucher, true},
		{disallowedPool, secondVoucher, false},
	} {
		if _, err := tx.Exec(ctx, `
			INSERT INTO pool_tokens (pool_id, voucher_id, is_allowed)
			VALUES ($1, $2, $3)`, relation.poolID, relation.voucherID, relation.allowed); err != nil {
			t.Fatalf("seed pool voucher: %v", err)
		}
	}

	listPoolIDs := func(ids []uint64, match TagMatch) []uint64 {
		t.Helper()
		filter, filterErr := base.WithVoucherIDs(ids, match)
		if filterErr != nil {
			t.Fatalf("pool voucher filter: %v", filterErr)
		}
		pools, listErr := pg.ListPoolsFiltered(ctx, tx, filter, CatalogFirstPage, 0, 100)
		if listErr != nil {
			t.Fatalf("list pools: %v", listErr)
		}
		got := make([]uint64, 0, len(pools))
		for _, pool := range pools {
			got = append(got, pool.ID)
		}
		slices.Sort(got)
		return got
	}
	if got := listPoolIDs([]uint64{secondVoucher}, TagMatchAny); !slices.Equal(got, []uint64{bothPool}) {
		t.Errorf("pool second-voucher IDs = %v, want allowed pool [%d]", got, bothPool)
	}
	if got := listPoolIDs([]uint64{firstVoucher, secondVoucher}, TagMatchAll); !slices.Equal(got, []uint64{bothPool}) {
		t.Errorf("pool all IDs = %v, want [%d]", got, bothPool)
	}

	poolFacets, err := pg.ListPoolVoucherFacets(ctx, tx, base)
	if err != nil {
		t.Fatalf("pool voucher facets: %v", err)
	}
	poolCounts := make(map[uint64]uint64, len(poolFacets))
	for _, facet := range poolFacets {
		poolCounts[facet.ID] = facet.Count
	}
	if poolCounts[firstVoucher] != 2 || poolCounts[secondVoucher] != 1 {
		t.Errorf("pool voucher counts = %v, want first=2 second=1", poolCounts)
	}
}

// Report search matches title and description, and nothing else. The body is deliberately not
// searched: it is normalized into a separate structure and needs a different index shape.
func TestReportSearchMatchesTitleAndDescription(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()

	byTitle := seedSearchReport(t, tx, searchToken+" Harvest", "unrelated body text")
	byDescription := seedSearchReport(t, tx, "Unrelated title", "a "+searchToken+" description")
	seedSearchReport(t, tx, "Neither", "nor")

	filter := mustFilter(t, searchToken, nil, TagMatchAny)
	reports, err := pg.ListReportsFiltered(ctx, tx, filter, ReportCursor{}, false, 100)
	if err != nil {
		t.Fatalf("filtered reports: %v", err)
	}

	found := map[uint64]bool{}
	for _, report := range reports {
		found[report.ID] = true
	}
	if !found[byTitle] || !found[byDescription] {
		t.Errorf("expected both the title and description matches, got %v", found)
	}
	if len(reports) != 2 {
		t.Errorf("matched %d reports, want exactly the two fixtures", len(reports))
	}
}

// An unapproved report is invisible to the feed, and search does not change that.
func TestReportSearchKeepsFeedVisibility(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()

	seedSearchReport(t, tx, searchToken+" Approved", "visible")

	// SUBMITTED, not DRAFT: a submitted report is the closest thing to visible that is still
	// not approved, so it is the case most likely to leak through a widened predicate.
	var draft uint64
	if err := tx.QueryRow(ctx, `
		INSERT INTO reports (title, description, status, author_address, created_at, updated_at)
		VALUES ($1, 'hidden', 'SUBMITTED', '0x0000000000000000000000000000000000000001', now(), now())
		RETURNING id`, searchToken+" Draft").Scan(&draft); err != nil {
		t.Fatalf("seed draft: %v", err)
	}

	filter := mustFilter(t, searchToken, nil, TagMatchAny)
	reports, err := pg.ListReportsFiltered(ctx, tx, filter, ReportCursor{}, false, 100)
	if err != nil {
		t.Fatalf("filtered reports: %v", err)
	}

	for _, report := range reports {
		if report.ID == draft {
			t.Error("an unapproved report was returned by a search; visibility must not widen")
		}
	}
	if len(reports) != 1 {
		t.Errorf("matched %d reports, want only the approved fixture", len(reports))
	}
}

// Report tags are direct, so `all` means the report carries every requested value.
func TestReportTagFilterMatchModes(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()

	both := seedSearchReport(t, tx, searchToken+" Both", "x", "Art", "Culture")
	onlyArt := seedSearchReport(t, tx, searchToken+" OnlyArt", "x", "Art")

	list := func(tags []string, match TagMatch) []uint64 {
		t.Helper()
		filter := mustFilter(t, searchToken, tags, match)
		reports, err := pg.ListReportsFiltered(ctx, tx, filter, ReportCursor{}, false, 100)
		if err != nil {
			t.Fatalf("filtered reports: %v", err)
		}
		ids := make([]uint64, 0, len(reports))
		for _, report := range reports {
			ids = append(ids, report.ID)
		}
		slices.Sort(ids)
		return ids
	}

	anyMatch := list([]string{"Art", "Culture"}, TagMatchAny)
	wantAny := []uint64{both, onlyArt}
	slices.Sort(wantAny)
	if !slices.Equal(anyMatch, wantAny) {
		t.Errorf("any-match = %v, want both fixtures %v", anyMatch, wantAny)
	}

	allMatch := list([]string{"Art", "Culture"}, TagMatchAll)
	if !slices.Equal(allMatch, []uint64{both}) {
		t.Errorf("all-match = %v, want only the report carrying both tags", allMatch)
	}
}

// The feed's keyset is the (updated_at, id) pair, so a filtered walk must page through the
// whole match set without skipping or repeating a row.
func TestFilteredReportPagingIsExhaustiveAndUnique(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()

	// Identical timestamps on purpose: comparing only `updated_at` would drop or repeat every
	// row sharing one, which is exactly what the row comparison prevents.
	seeded := make([]uint64, 0, 5)
	for index := range 5 {
		seeded = append(seeded, seedSearchReport(t, tx, fmt.Sprintf("%s Page %d", searchToken, index), "x"))
	}
	if _, err := tx.Exec(ctx,
		`UPDATE reports SET updated_at = now() WHERE title LIKE $1`, searchToken+" Page%"); err != nil {
		t.Fatalf("flatten timestamps: %v", err)
	}

	filter := mustFilter(t, searchToken, nil, TagMatchAny)
	var walked []uint64
	cursor := ReportCursor{}
	hasCursor := false
	for range 10 {
		page, err := pg.ListReportsFiltered(ctx, tx, filter, cursor, hasCursor, 2)
		if err != nil {
			t.Fatalf("filtered page: %v", err)
		}
		if len(page) == 0 {
			break
		}
		for _, report := range page {
			walked = append(walked, report.ID)
		}
		last := page[len(page)-1]
		cursor = ReportCursor{UpdatedAt: last.UpdatedAt, ID: last.ID}
		hasCursor = true
		if len(page) < 2 {
			break
		}
	}

	deduped := slices.Clone(walked)
	slices.Sort(deduped)
	deduped = slices.Compact(deduped)
	if len(deduped) != len(walked) {
		t.Errorf("paging returned duplicate rows: %v", walked)
	}
	want := slices.Clone(seeded)
	slices.Sort(want)
	if !slices.Equal(deduped, want) {
		t.Errorf("paged set = %v, want the five fixtures %v", deduped, want)
	}
}

// The report facet is the direct form, and every count must equal what the same single-tag
// filter returns.
func TestReportTagFacetCountsMatchResultQueries(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()

	seedSearchReport(t, tx, searchToken+" A", "x", "Art")
	seedSearchReport(t, tx, searchToken+" B", "x", "Art")
	seedSearchReport(t, tx, searchToken+" C", "x", "Culture")

	filter := mustFilter(t, searchToken, nil, TagMatchAny)
	facets, err := pg.ListReportTagFacets(ctx, tx, filter)
	if err != nil {
		t.Fatalf("report facets: %v", err)
	}
	if len(facets) == 0 {
		t.Fatal("expected a non-empty report facet vocabulary")
	}

	counts := map[string]uint64{}
	for _, facet := range facets {
		counts[facet.Tag] = facet.Count
	}
	if counts["Art"] != 2 {
		t.Errorf("Art count = %d, want 2", counts["Art"])
	}
	if counts["Culture"] != 1 {
		t.Errorf("Culture count = %d, want 1", counts["Culture"])
	}

	for _, facet := range facets {
		single := mustFilter(t, searchToken, []string{facet.Tag}, TagMatchAny)
		rows, err := pg.ListReportsFiltered(ctx, tx, single, ReportCursor{}, false, 500)
		if err != nil {
			t.Fatalf("single-tag filter: %v", err)
		}
		if uint64(len(rows)) != facet.Count {
			t.Errorf("report facet %q promised %d rows but the filter returned %d",
				facet.Tag, facet.Count, len(rows))
		}
	}
}

// The filter-free report facet skips the vocabulary statement, so the skip has to be proven
// equivalent to running it — the same guard the voucher facet carries.
func TestFilterFreeReportFacetMatchesTheVocabularyItSkips(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()

	seedSearchReport(t, tx, searchToken+" Facet", "x", "Art")

	facets, err := pg.ListReportTagFacets(ctx, tx, CatalogFilter{})
	if err != nil {
		t.Fatalf("filter-free report facets: %v", err)
	}

	var vocabulary []string
	if err := pgxscan.Select(ctx, tx, &vocabulary, pg.queries.ListReportTagVocabulary); err != nil {
		t.Fatalf("vocabulary: %v", err)
	}

	got := make([]string, 0, len(facets))
	for _, facet := range facets {
		got = append(got, facet.Tag)
		if facet.Count == 0 {
			t.Errorf("filter-free report facet carries a zero count for %q", facet.Tag)
		}
	}
	if !slices.Equal(got, vocabulary) {
		t.Errorf("filter-free report facet = %v, want the vocabulary %v", got, vocabulary)
	}
}
