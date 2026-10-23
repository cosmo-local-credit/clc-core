package store

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

// Generic-plan gates for catalog search.
//
// EXPLAIN (GENERIC_PLAN) rather than EXPLAIN ANALYZE with literals: pgx runs
// these statements under QueryExecModeCacheStatement, so PostgreSQL may execute
// them from a cached generic plan where the search term is an unknown
// parameter. A plan measured with a literal proves nothing about that path --
// it is exactly how the original text_pattern_ops proposal looked correct and
// was not.
//
// Plans are asserted only at representative scale. At the current 2,603 active
// vouchers a sequential scan is a legitimate choice, so an assertion there
// would encode the wrong expectation; these tests seed 5x first and ANALYZE,
// per the plan's instruction to evaluate plans only after seeding.

// The scale every plan gate asserts against, and the floor each fixture must reach
// on its own.
//
// scaleFixtureRows is what a database that already holds an imported catalog needs
// added to get there; minScaleRows is the floor itself. A fixture that only ever
// inserted scaleFixtureRows passed locally, where the import supplies the
// remainder, and failed on a fresh database with exactly scaleFixtureRows rows --
// which is what CI creates. See scaleSeedCount.
// The statements each fixture counts with, and each gate asserts on afterwards.
const (
	scaleFixtureRows = 11_000
	minScaleRows     = 13_000

	countActiveVouchers  = "SELECT count(*) FROM vouchers WHERE active AND geo_lat IS NOT NULL"
	countApprovedReports = "SELECT count(*) FROM reports WHERE status = 'APPROVED'"
)

// scaleSeedCount is how many rows this fixture must insert to reach minScaleRows,
// given what the database already holds.
//
// Never fewer than scaleFixtureRows, so a populated database seeds exactly what it
// seeded before and the measured plans are unchanged; enough to reach the floor on
// an empty one, so the fixture does not depend on an imported catalog being there.
func scaleSeedCount(t *testing.T, tx pgx.Tx, countExisting string) int {
	t.Helper()

	var existing int
	if err := tx.QueryRow(context.Background(), countExisting).Scan(&existing); err != nil {
		t.Fatalf("count existing rows: %v", err)
	}
	return max(scaleFixtureRows, minScaleRows-existing)
}

// seedFivefoldVouchers inserts active geolocated vouchers with a realistic trigram
// spread, plus available offerings and skewed tags, then ANALYZEs. Everything
// happens inside the caller's transaction and is rolled back, including the
// statistics.
func seedFivefoldVouchers(t *testing.T, tx pgx.Tx) {
	t.Helper()
	ctx := context.Background()

	rows := scaleSeedCount(t, tx, countActiveVouchers)

	// Dropped before the insert and rebuilt after it, so the planner's view of
	// each index matches the seeded data. See dropIndexesForScaleSeeding.
	//
	// The offering indexes are here for the same reason as the trigram ones, not
	// only for the GIN pending list: relpages and reltuples for *any* index are
	// maintained by VACUUM, so 11,000 offerings and ~6,000 tag rows bulk-inserted
	// into a live B-tree leave it described by its pre-seed size. The facet gate
	// then picked an index scan or a hash join depending on how much autovacuum
	// had done since the last run — it failed 4 runs in 10 in isolation.
	rebuild := dropIndexesForScaleSeeding(t, tx,
		"vouchers_active_display_name_trgm_idx", "vouchers_active_symbol_trgm_idx",
		"offerings_voucher_id_idx", "offering_tags_tag_offering_idx")

	// Names combine a 20-word and a 10-word vocabulary plus the row number, so
	// common terms, rare terms, and no-match terms all exist.
	if _, err := tx.Exec(ctx, `
		INSERT INTO vouchers (chain_id, address, name, symbol, decimals, active, geo_lat, geo_lng, location_name)
		SELECT 42220,
		    '0x' || lpad(to_hex(3000000000 + g), 40, '0'),
		    (ARRAY['Sarafu','Kijani','Mkulima','Chama','Ubuntu','Nafaka','Maziwa','Jembe','Shamba','Tosha',
		           'Grassroots','Community','Regen','Soil','Harvest','Weaver','Basket','Honey','Cassava','Maize'])[1 + (g % 20)]
		        || ' ' ||
		    (ARRAY['Network','Coop','Group','Collective','Trust','Circle','Fund','Union','Hub','Farm'])[1 + ((g / 20) % 10)]
		        || ' ' || g,
		    'SV' || g::TEXT, 6, TRUE,
		    -4.7 + (g % 900) * 0.01, 33.9 + (g % 800) * 0.01, 'Scale Fixture'
		FROM generate_series(1, $1) g`, rows); err != nil {
		t.Fatalf("seed vouchers at scale: %v", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO offerings (voucher_id, seller_address, commodity_name, description, quantity, available)
		SELECT v.id, '0x0000000000000000000000000000000000000001', 'Scale Fixture', '', 1, TRUE
		FROM vouchers v WHERE v.location_name = 'Scale Fixture'`); err != nil {
		t.Fatalf("seed offerings at scale: %v", err)
	}

	// Skewed tags: one on ~half the rows, one on ~5%, one on ~0.2%.
	for _, seed := range []struct {
		tag    string
		modulo int
	}{
		{tag: "Food production", modulo: 2},
		{tag: "Permaculture", modulo: 20},
		{tag: "Storytelling", modulo: 500},
	} {
		if _, err := tx.Exec(ctx, `
			INSERT INTO offering_tags (offering_id, tag_value)
			SELECT o.id, $1 FROM offerings o
			WHERE o.commodity_name = 'Scale Fixture' AND o.id % $2 = 0`,
			seed.tag, seed.modulo); err != nil {
			t.Fatalf("seed tag %q at scale: %v", seed.tag, err)
		}
	}

	rebuild()

	// Expression-index statistics only exist once ANALYZE has run against the
	// index, and without them the planner cannot cost lower(name) at all.
	for _, table := range []string{"vouchers", "offerings", "offering_tags"} {
		if _, err := tx.Exec(ctx, "ANALYZE "+table); err != nil {
			t.Fatalf("analyze %s: %v", table, err)
		}
	}

	var active int
	if err := tx.QueryRow(ctx, countActiveVouchers).Scan(&active); err != nil {
		t.Fatalf("count active vouchers: %v", err)
	}
	if active < minScaleRows {
		t.Fatalf("seeded scale is only %d active vouchers; expected at least %d",
			active, minScaleRows)
	}
	t.Logf("plan assertions run against %d active geolocated vouchers", active)
}

// explainGenericPlan returns the generic plan for a composed statement.
//
// This goes through pgconn's Exec — the raw simple-query protocol — rather
// than tx.Query. The statement carries $1..$N placeholders belonging to the
// inner query, and GENERIC_PLAN is precisely what lets PostgreSQL plan them as
// unknown parameters, so no arguments are supplied. Both of pgx's own paths
// reject that: the extended protocol would try to infer and bind the
// parameters, and QueryExecModeSimpleProtocol interpolates client-side and so
// still requires one argument per placeholder.
func explainGenericPlan(t *testing.T, tx pgx.Tx, sql string) string {
	t.Helper()

	results, err := tx.Conn().PgConn().
		Exec(context.Background(), "EXPLAIN (GENERIC_PLAN, COSTS OFF) "+sql).
		ReadAll()
	if err != nil {
		t.Fatalf("EXPLAIN (GENERIC_PLAN): %v\nstatement:\n%s", err, sql)
	}
	if len(results) != 1 {
		t.Fatalf("EXPLAIN returned %d result sets, want 1", len(results))
	}

	lines := make([]string, 0, len(results[0].Rows))
	for _, row := range results[0].Rows {
		if len(row) != 1 {
			t.Fatalf("EXPLAIN row has %d columns, want 1", len(row))
		}
		lines = append(lines, string(row[0]))
	}
	return strings.Join(lines, "\n")
}

// indexScanPattern finds an index whose scan carries an Index Cond, which is
// what "the index drives this query" means. An index named only in a Filter or
// Recheck line does not count.
var (
	indexScanPattern = regexp.MustCompile(`Index Scan on (\S+)`)
)

func assertIndexDrives(t *testing.T, label, plan string, wantIndexes ...string) {
	t.Helper()

	driving := map[string]bool{}
	for _, match := range indexScanPattern.FindAllStringSubmatch(plan, -1) {
		driving[match[1]] = true
	}

	for _, want := range wantIndexes {
		if !driving[want] {
			t.Errorf("%s: %s does not drive the generic plan.\nplan:\n%s", label, want, plan)
		}
	}
	if !strings.Contains(plan, "Index Cond:") {
		t.Errorf("%s: generic plan carries no index condition at all.\nplan:\n%s", label, plan)
	}
}

// The headline gate: a name/symbol search must keep the trigram index driving
// under a generic plan, in every page direction and with tags combined.
//
// This is a regression test for the query shape, not for the index definition.
// The two shapes differ only on cursor pages and when a tag reducer is present:
//
//	composition                direct shape        materialized match set
//	substring, first page      trigram             trigram
//	substring, next page       backward pkey walk  trigram
//	substring, previous page   forward pkey walk   trigram
//	substring + tags           backward pkey walk  trigram
//
// Adding `v.id < $cursor` to a query already ordered by `v.id DESC LIMIT n` makes
// vouchers_pkey look like it satisfies the filter, the ordering, and the early
// abort all at once, so the planner takes it and applies the search as a filter.
// Measured on a cursor page at this scale:
//
//	term                      backward pkey walk        match set
//	common (551 matches)      1.147 ms (1,881 removed)  0.673 ms
//	rare (1 match)            8.706 ms (13,710 removed) 0.085 ms
//	no match                  7.749 ms (13,711 removed) 0.075 ms
//
// The pkey walk degrades linearly with the catalog for exactly the terms a search
// box produces most — rare ones, and the no-match prefixes typed on the way to
// them — while the match set costs what the match set costs. On a first page,
// where there is no cursor, both shapes plan and measure identically (0.55 ms).
func TestVoucherSearchRetainsTrigramIndexUnderGenericPlan(t *testing.T) {
	_, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()

	seedFivefoldVouchers(t, tx)

	for label, tc := range map[string]struct {
		filter CatalogFilter
		page   CatalogPage
	}{
		"substring first page": {filter: CatalogFilter{Term: "sarafu"}, page: CatalogFirstPage},
		"substring next page":  {filter: CatalogFilter{Term: "sarafu"}, page: CatalogNextPage},
		"substring prev page":  {filter: CatalogFilter{Term: "sarafu"}, page: CatalogPrevPage},
		"prefix first page":    {filter: CatalogFilter{Term: "sa"}, page: CatalogFirstPage},
		"prefix next page":     {filter: CatalogFilter{Term: "sa"}, page: CatalogNextPage},
		"substring with tags any": {
			filter: CatalogFilter{Term: "sarafu", Tags: []string{"Permaculture"}, TagMatch: TagMatchAny},
			page:   CatalogFirstPage,
		},
		"substring with tags all": {
			filter: CatalogFilter{Term: "sarafu", Tags: []string{"Permaculture"}, TagMatch: TagMatchAll},
			page:   CatalogFirstPage,
		},
		"substring with tags, next page": {
			filter: CatalogFilter{Term: "sarafu", Tags: []string{"Permaculture"}, TagMatch: TagMatchAny},
			page:   CatalogNextPage,
		},
	} {
		query := composeMarketVoucherPage(nil, tc.filter, tc.page, 5000, 100)
		plan := explainGenericPlan(t, tx, query.sql)

		assertIndexDrives(t, label, plan, "vouchers_active_display_name_trgm_idx")

		// The backward primary-key walk is the plan this shape exists to avoid.
		if strings.Contains(plan, "Index Scan Backward using vouchers_pkey") {
			t.Errorf("%s: planner fell back to a backward primary-key walk, which filters the whole catalog.\nplan:\n%s",
				label, plan)
		}
		if strings.Contains(plan, "Seq Scan on vouchers") {
			t.Errorf("%s: search fell back to a sequential scan of vouchers.\nplan:\n%s", label, plan)
		}
	}
}

// With a viewport the GiST point index is the correct driving predicate: it is
// far more selective than any trigram match, and the search becomes a filter
// over its candidates. The gate is that an index still drives the query.
func TestBoundedVoucherSearchRetainsGeoIndexUnderGenericPlan(t *testing.T) {
	_, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()

	seedFivefoldVouchers(t, tx)

	bounds := &MarketVoucherBounds{MinLng: 36.6, MinLat: -1.5, MaxLng: 37.1, MaxLat: -1.1}
	for label, page := range map[string]CatalogPage{
		"first": CatalogFirstPage,
		"next":  CatalogNextPage,
		"prev":  CatalogPrevPage,
	} {
		query := composeMarketVoucherPage(bounds, CatalogFilter{Term: "sarafu"}, page, 5000, 100)
		plan := explainGenericPlan(t, tx, query.sql)

		assertIndexDrives(t, "bounded search "+label, plan, "vouchers_geo_gist_idx")
		if strings.Contains(plan, "Seq Scan on vouchers") {
			t.Errorf("bounded search %s: fell back to a sequential scan.\nplan:\n%s", label, plan)
		}
	}
}

// The nearby filter's bounding box must let the GiST point index drive the
// voucher page, with and without a search term. The exact distance expression
// alone cannot use an index.
func TestNearbyVoucherPageUsesGeoIndexUnderGenericPlan(t *testing.T) {
	_, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()

	seedFivefoldVouchers(t, tx)

	nearby := &CatalogLocation{Lat: -1.29, Lng: 36.82}
	for label, term := range map[string]string{"reducer only": "", "search": "sarafu"} {
		query := composeMarketVoucherPage(nil, CatalogFilter{Term: term, Nearby: nearby}, CatalogFirstPage, 0, 100)
		plan := explainGenericPlan(t, tx, query.sql)

		assertIndexDrives(t, "nearby "+label, plan, "vouchers_geo_gist_idx")
	}
}

// The unfiltered map pages must keep the plans migration 008 measured: the
// primary key drives the unbounded page, the GiST index the bounded one. This
// guards against the search work regressing the endpoint's hot path.
func TestUnfilteredVoucherPagesKeepTheirMeasuredPlans(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()

	seedFivefoldVouchers(t, tx)

	unbounded := explainGenericPlan(t, tx, pg.queries.ListMarketVouchers)
	if !strings.Contains(unbounded, "vouchers_pkey") {
		t.Errorf("the unbounded map page must still walk the primary key.\nplan:\n%s", unbounded)
	}

	bounded := explainGenericPlan(t, tx, pg.queries.ListMarketVouchersBounded)
	assertIndexDrives(t, "unfiltered bounded page", bounded, "vouchers_geo_gist_idx")
}

// A tags-only query is deliberately not asserted to use an index: the junction
// tables are small and a sequential scan over them can be the planner's correct
// choice. What must hold is that the query is planned and driven by an index on
// vouchers rather than degenerating into a sequential scan of the catalog.
func TestTagOnlyFilterIsPlannedWithoutRequiringAJunctionIndex(t *testing.T) {
	_, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()

	seedFivefoldVouchers(t, tx)

	for label, filter := range map[string]CatalogFilter{
		"common tag": {Tags: []string{"Food production"}, TagMatch: TagMatchAny},
		"rare tag":   {Tags: []string{"Storytelling"}, TagMatch: TagMatchAny},
		"two tags":   {Tags: []string{"Permaculture", "Storytelling"}, TagMatch: TagMatchAll},
	} {
		query := composeMarketVoucherPage(nil, filter, CatalogFirstPage, 0, 100)
		plan := explainGenericPlan(t, tx, query.sql)

		if !strings.Contains(plan, "Index Scan Backward using vouchers_pkey") {
			t.Errorf("tags-only %s: voucher candidates must use the ordered primary-key scan.\nplan:\n%s", label, plan)
		}
	}
}

// Pool search must keep its own trigram indexes driving, in every direction, at a
// representative pool count. The local catalog holds two pools, which cannot
// produce a meaningful plan, so this seeds 5,000 first: pool plans mean nothing
// until representative data is loaded.
//
// The junction lookups in the projection are deliberately not asserted. pool_tags
// and pool_tokens hold 6 and 16 rows here, and a sequential scan of a table that
// size is the planner's correct choice; the plan document says as much. Those
// lookups are worth re-measuring alongside representative membership data, which
// this fixture does not create.
func TestPoolSearchRetainsTrigramIndexUnderGenericPlan(t *testing.T) {
	_, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()

	// The local catalog holds two pools, which cannot produce a representative
	// plan, so seed a representative pool count before asserting.
	rebuild := dropIndexesForScaleSeeding(t, tx, "pools_display_name_trgm_idx", "pools_symbol_trgm_idx")
	if _, err := tx.Exec(ctx, `
		INSERT INTO pools (chain_id, address, name, symbol, decimals)
		SELECT 42220,
		    '0x' || lpad(to_hex(4000000000 + g), 40, '0'),
		    (ARRAY['Sarafu','Kijani','Mkulima','Chama','Ubuntu','Nafaka','Maziwa','Jembe','Shamba','Tosha'])[1 + (g % 10)]
		        || ' Pool ' || g,
		    'SP' || g::TEXT, 6
		FROM generate_series(1, 5000) g`); err != nil {
		t.Fatalf("seed pools at scale: %v", err)
	}
	rebuild()
	if _, err := tx.Exec(ctx, "ANALYZE pools"); err != nil {
		t.Fatalf("analyze pools: %v", err)
	}

	for label, page := range map[string]CatalogPage{
		"first": CatalogFirstPage,
		"next":  CatalogNextPage,
		"prev":  CatalogPrevPage,
	} {
		query := composePoolPage(CatalogFilter{Term: "sarafu"}, page, 2500, 100)
		plan := explainGenericPlan(t, tx, query.sql)

		assertIndexDrives(t, "pool search "+label, plan, "pools_display_name_trgm_idx")

		// The paged rows are fetched by primary key, so the wide pool projection
		// is read for one page rather than for every candidate.
		if !strings.Contains(plan, "Index Scan using pools_pkey on pools p") {
			t.Errorf("pool search %s: the page must hydrate by primary key.\nplan:\n%s", label, plan)
		}
		if strings.Contains(plan, "Seq Scan on pools") {
			t.Errorf("pool search %s: fell back to a sequential scan of the pool catalog.\nplan:\n%s", label, plan)
		}
		if strings.Contains(plan, "Index Scan Backward using pools_pkey") {
			t.Errorf("pool search %s: fell back to a backward primary-key walk.\nplan:\n%s", label, plan)
		}
	}
}

// The facet count query runs once per faceted first page, so its plan matters as
// much as a result query's. It must drive from the search and then reach its tags
// through indexes, so its cost tracks the match set rather than the size of the
// junction tables.
//
// This is the test that made the facet two statements rather than one. With the
// request-independent vocabulary as a CTE in here, its junction scans dominated
// the cost estimate and the whole query collapsed into hash joins over all 6,705
// tag rows, sequentially scanning the catalog to build the candidate set. Split,
// the count query keeps the trigram index and the vocabulary is a parameterless
// statement that can be cached: measured 2.25 ms for a common term and 0.09 ms
// for a no-match one, against 6.2 ms for the vocabulary it no longer repeats.
func TestVoucherFacetCountsRetainTrigramIndexUnderGenericPlan(t *testing.T) {
	_, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()

	seedFivefoldVouchers(t, tx)

	query := composeMarketVoucherTagCounts(nil, CatalogFilter{Term: "sarafu"})
	plan := explainGenericPlan(t, tx, query.sql)

	assertIndexDrives(t, "voucher facet counts", plan, "vouchers_active_display_name_trgm_idx")

	// Offerings reach their tags by index. Otherwise this is a scan of the whole
	// junction per facet request, which is the shape that motivated splitting the
	// vocabulary out of this query in the first place.
	if !strings.Contains(plan, "offering_tags_pkey") {
		t.Errorf("facet counts must hydrate through offering_tags_pkey.\nplan:\n%s", plan)
	}
	if strings.Contains(plan, "Seq Scan on offering_tags") {
		t.Errorf("facet counts must not scan the whole tag junction.\nplan:\n%s", plan)
	}
	if strings.Contains(plan, "Seq Scan on vouchers") {
		t.Errorf("facet counts fell back to a sequential scan of the catalog.\nplan:\n%s", plan)
	}

	// How the candidates reach their offerings is deliberately *not* pinned to
	// offerings_voucher_id_idx. Under GENERIC_PLAN the planner cannot know how
	// selective the term is, so an index scan per candidate and one hashed pass
	// over offerings are both sound, and which one wins here is decided by the
	// offerings table's physical page count. That count is a test artifact: each
	// run bulk-inserts 11,000 offerings and rolls back, and the dead pages are
	// still counted until autovacuum reclaims them, so relpages ranged from 242
	// to 1,504 across consecutive runs and flipped the choice 2 times in 10.
	//
	// What must not happen is a *repeated* scan of offerings, once per candidate.
	// A single hashed pass is bounded; a rescan is not.
	if strings.Contains(plan, "Seq Scan on offerings") &&
		!strings.Contains(plan, "Hash Cond: (o.voucher_id = c.id)") {
		t.Errorf("facet counts scan offerings without hashing them once.\nplan:\n%s", plan)
	}
}

// The filter-free facet is the widest shape this endpoint serves and the one every
// signed-out client requests once, to populate its tag vocabulary before it can
// offer a first filter. With no term and no viewport there is nothing to narrow
// the candidate set, so it groups over every available offering of every visible
// voucher and no trigram index can help.
//
// Measured at 5x scale, against the same catalog page this endpoint already serves
// on every cold load:
//
//	catalog first page, perPage=500     1.09 ms
//	facet counts, common term           1.07 ms
//	facet counts, no-match term         0.05 ms
//	facet vocabulary                    5.39 ms
//	facet counts, no filter             9.12 ms
//
// This shape sequentially scans and hash-joins, and that is the correct plan: it
// aggregates every visible row, so there is no selective predicate for an index to
// serve and per-candidate index probes would be 13,181 lookups where one pass over
// each table suffices. Unlike every other query in this file, it cannot be made
// cheaper by indexing — which is why the two mitigations are structural rather
// than an index. listTagFacets skips the vocabulary statement for this shape, since
// the count keys already are the vocabulary (14.5 ms becomes 9.1 ms), and the API
// layer caches this one response for the max-age it already publishes, because it
// depends on nothing in the request.
//
// So what this pins is the degradation the planner could actually choose: a nested
// loop driven off the candidate set, which turns one pass into a probe per
// candidate. Sequential scans here are the expectation, not the regression.
func TestUnfilteredFacetCountsAggregateInOnePass(t *testing.T) {
	_, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()

	seedFivefoldVouchers(t, tx)

	query := composeMarketVoucherTagCounts(nil, CatalogFilter{})
	if len(query.args) != 0 {
		t.Fatalf("the filter-free count query binds %d arguments; it must be "+
			"parameterless to be worth caching as a single entry", len(query.args))
	}
	plan := explainGenericPlan(t, tx, query.sql)

	if strings.Contains(plan, "Nested Loop") {
		t.Errorf("filter-free facet counts fell to a nested loop, which probes once per "+
			"candidate instead of scanning each table once.\nplan:\n%s", plan)
	}
	if !strings.Contains(plan, "Hash Join") {
		t.Errorf("filter-free facet counts no longer hash-join; the full aggregation "+
			"has changed shape and its cost must be re-measured.\nplan:\n%s", plan)
	}
	if !strings.Contains(plan, "Group Key: ot.tag_value") {
		t.Errorf("filter-free facet counts must aggregate by tag value.\nplan:\n%s", plan)
	}
}

// The vocabulary is parameterless, so it has one plan and no generic-plan
// hazard. What matters is that it stays scoped to tags a search could actually
// return: a value reachable only through an unavailable offering or an invisible
// voucher would offer a filter that can never match.
//
// The unused tag values are discovered rather than hard-coded: 41 of token_tag's
// 44 values are already in use in the imported catalog, so naming one would make
// this test depend on which ones happen to be idle.
func TestVoucherTagVocabularyIsVisibilityScoped(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()

	readVocabulary := func() []string {
		t.Helper()
		var vocabulary []string
		if err := pgxscan.Select(ctx, tx, &vocabulary, pg.queries.ListVoucherTagVocabulary); err != nil {
			t.Fatalf("vocabulary: %v", err)
		}
		return vocabulary
	}

	baseline := readVocabulary()

	var everyTag []string
	if err := pgxscan.Select(ctx, tx, &everyTag, pg.queries.ListTokenTags); err != nil {
		t.Fatalf("token tags: %v", err)
	}
	var idle []string
	for _, tag := range everyTag {
		if !slices.Contains(baseline, tag) {
			idle = append(idle, tag)
		}
	}
	if len(idle) < 2 {
		t.Skipf("need two tag values unused by the catalog; only %d are idle", len(idle))
	}

	// Reachable only through an offering that is not available.
	visible := seedSearchVoucher(t, tx, searchToken+" Vocab", "VV")
	seedTaggedOffering(t, tx, visible, false, idle[0])

	// Reachable only through a voucher the catalog does not show.
	var inactive uint64
	if err := tx.QueryRow(ctx, `
		INSERT INTO vouchers (chain_id, address, name, symbol, decimals, active, geo_lat, geo_lng)
		VALUES (42220, '0x' || lpad(to_hex((random() * 1e18)::BIGINT), 40, '0'),
		        'Inactive Vocab', 'IV', 6, FALSE, -1.29, 36.82)
		RETURNING id`).Scan(&inactive); err != nil {
		t.Fatalf("seed inactive voucher: %v", err)
	}
	seedTaggedOffering(t, tx, inactive, true, idle[1])

	after := readVocabulary()
	for _, unreachable := range idle[:2] {
		if slices.Contains(after, unreachable) {
			t.Errorf("%q is only reachable through an unavailable offering or an invisible voucher "+
				"but entered the facet vocabulary", unreachable)
		}
	}
	if !slices.Equal(after, baseline) {
		t.Errorf("hidden fixtures changed the vocabulary from %v to %v", baseline, after)
	}

	// An available offering on a visible voucher does put its tag in.
	reachable := seedSearchVoucher(t, tx, searchToken+" Reachable", "VR")
	seedTaggedOffering(t, tx, reachable, true, idle[0])
	if !slices.Contains(readVocabulary(), idle[0]) {
		t.Errorf("%q is reachable through an available offering but is missing from the vocabulary", idle[0])
	}
}

// seedFivefoldReports inserts ~11,000 approved reports with a realistic trigram spread over
// title and description, plus skewed tags, then ANALYZEs.
//
// Same rationale as seedFivefoldVouchers: at the current 3,730 reports a sequential scan is a
// legitimate plan, so asserting an index there would encode the wrong expectation.
func seedFivefoldReports(t *testing.T, tx pgx.Tx) {
	t.Helper()
	ctx := context.Background()

	rows := scaleSeedCount(t, tx, countApprovedReports)

	rebuild := dropIndexesForScaleSeeding(t, tx,
		"reports_approved_title_trgm_idx", "reports_approved_description_trgm_idx")

	if _, err := tx.Exec(ctx, `
		INSERT INTO reports (title, description, status, author_address, created_at, updated_at)
		SELECT
		    (ARRAY['Harvest','Nursery','Cleanup','Training','Market','Weaving','Honey','Cassava',
		           'Maize','Soil','Water','Solar','Compost','Seedling','Beekeeping','Repair',
		           'Childcare','Cooking','Fencing','Roofing'])[1 + (g % 20)]
		        || ' ' ||
		    (ARRAY['Report','Update','Summary','Log','Note','Review','Record','Digest','Brief',
		           'Roundup'])[1 + ((g / 20) % 10)]
		        || ' ' || g,
		    'Field description for entry ' || g,
		    'APPROVED', '0x0000000000000000000000000000000000000001',
		    now() - (g || ' minutes')::INTERVAL, now() - (g || ' minutes')::INTERVAL
		FROM generate_series(1, $1) g`, rows); err != nil {
		t.Fatalf("seed reports at scale: %v", err)
	}

	for _, seed := range []struct {
		tag    string
		modulo int
	}{
		{tag: "Food production", modulo: 2},
		{tag: "Permaculture", modulo: 20},
		{tag: "Storytelling", modulo: 500},
	} {
		if _, err := tx.Exec(ctx, `
			INSERT INTO report_tags (report_id, tag_value)
			SELECT r.id, $1 FROM reports r
			WHERE r.description LIKE 'Field description for entry %' AND r.id % $2 = 0`,
			seed.tag, seed.modulo); err != nil {
			t.Fatalf("seed report tag %q at scale: %v", seed.tag, err)
		}
	}

	rebuild()

	for _, table := range []string{"reports", "report_tags"} {
		if _, err := tx.Exec(ctx, "ANALYZE "+table); err != nil {
			t.Fatalf("analyze %s: %v", table, err)
		}
	}

	var approved int
	if err := tx.QueryRow(ctx, countApprovedReports).Scan(&approved); err != nil {
		t.Fatalf("count approved reports: %v", err)
	}
	if approved < minScaleRows {
		t.Fatalf("seeded scale is only %d approved reports; expected at least %d",
			approved, minScaleRows)
	}
	t.Logf("report plan assertions run against %d approved reports", approved)
}

// The headline gate for report search: a title or description search must keep a trigram index
// driving under a generic plan, on a cursor page and with tags combined.
//
// Same hazard as the voucher catalog, with a different index in the way. Adding
// `(updated_at, id) < ($1, $2)` to a query already ordered by `updated_at DESC, id DESC LIMIT n`
// makes `reports_status_updated_idx` look like it serves the filter, the ordering, and the early
// abort at once — so the planner takes it and demotes the search to a row filter over the feed.
// Materializing the match set first is what prevents that.
func TestReportSearchRetainsTrigramIndexUnderGenericPlan(t *testing.T) {
	_, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()

	seedFivefoldReports(t, tx)

	cursor := ReportCursor{UpdatedAt: time.Now(), ID: 9_000}

	for _, testCase := range []struct {
		name      string
		filter    CatalogFilter
		hasCursor bool
	}{
		{name: "substring, first page", filter: CatalogFilter{Term: "harvest"}},
		{name: "substring, cursor page", filter: CatalogFilter{Term: "harvest"}, hasCursor: true},
		{
			name:      "substring with tags, cursor page",
			filter:    CatalogFilter{Term: "harvest", Tags: []string{"Permaculture"}},
			hasCursor: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			query := composeReportPage(testCase.filter, cursor, testCase.hasCursor, 20)
			plan := explainGenericPlan(t, tx, query.sql)

			assertIndexDrives(t, "report search", plan, "reports_approved_title_trgm_idx")
			if strings.Contains(plan, "Seq Scan on reports") {
				t.Errorf("report search fell back to a sequential scan of the feed.\nplan:\n%s", plan)
			}
		})
	}
}

// A tag-only report filter has no term for a trigram index to serve, so this asserts only that
// the generic plan keeps *an* index condition — the same assertion the voucher equivalent makes,
// and for a sharper reason than it looks.
//
// Under GENERIC_PLAN this query shows `Seq Scan on reports`, which looks like exactly the
// degradation the term path materializes its match set to avoid. It is not. With
// `tag_value = ANY($1)` the parameter's contents are unknown, so its selectivity is unknown, and
// the planner hedges. Given real parameters it takes the ordered feed walk and aborts early.
// Measured at 5x scale (14,730 approved reports), against a junction-driven alternative that
// collects candidates from `report_tags` first:
//
//	tag                     direct (kept)  junction-driven
//	common (~half the rows) 0.061 ms       6.061 ms
//	rare (~0.2%)            0.102 ms       0.109 ms
//
// So the direct shape is kept, and it is 100x better on the common case. This is the mirror of
// migration 011's warning: there, a plan measured with a literal looked correct and was not;
// here, a *generic* plan looked wrong and was not. Neither one substitutes for measuring.
// Asserting "no Seq Scan" would fail against the correct shape and push someone toward the
// slower one.
func TestReportTagOnlyFilterKeepsTheFeedIndexed(t *testing.T) {
	_, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()

	seedFivefoldReports(t, tx)

	for label, filter := range map[string]CatalogFilter{
		"common tag": {Tags: []string{"Food production"}, TagMatch: TagMatchAny},
		"rare tag":   {Tags: []string{"Storytelling"}, TagMatch: TagMatchAny},
		"two tags":   {Tags: []string{"Permaculture", "Storytelling"}, TagMatch: TagMatchAll},
	} {
		query := composeReportPage(filter, ReportCursor{}, false, 20)
		plan := explainGenericPlan(t, tx, query.sql)

		if !strings.Contains(plan, "Index Cond:") {
			t.Errorf("tags-only %s: generic plan carries no index condition at all.\nplan:\n%s",
				label, plan)
		}
	}
}

// The unfiltered report feed keeps its measured plan: with no search predicate the existing
// (status, updated_at, id) index walk is correct, and the composer must not be reached at all.
func TestUnfilteredReportFeedKeepsItsMeasuredPlan(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()

	seedFivefoldReports(t, tx)

	for name, sql := range map[string]string{
		"first page": pg.queries.ListReports,
		"next page":  pg.queries.ListReportsNext,
	} {
		plan := explainGenericPlan(t, tx, sql)
		if !strings.Contains(plan, "reports_status_updated_idx") {
			t.Errorf("%s: the feed's own index no longer drives it.\nplan:\n%s", name, plan)
		}
	}
}
