package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A voucher's tags reach the catalog two ways, and the filter has to honour both:
// voucher_tags is curated directly (the deploy pipeline copies deployed_token_tags
// into it, and the admin profile write edits it), while offering_tags is derived
// from a voucher's available offerings.
//
// These are the regression tests for a filter that read only the derived form. A
// voucher published with tags, or re-tagged by an operator, stored correctly and
// read back correctly on its detail endpoint, then matched nothing here.

func seedTaggedVoucher(t *testing.T, tx pgx.Tx, direct []string, viaOffering []string) uint64 {
	t.Helper()
	ctx := context.Background()

	var voucherID uint64
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO vouchers (chain_id, address, name, symbol, decimals, active, geo_lat, geo_lng)
		VALUES (42220, $1, 'Tagged', 'ZQT', 6, TRUE, -1.29, 36.82)
		RETURNING id`, randomAddress(t, tx)).Scan(&voucherID))

	for _, tag := range direct {
		_, err := tx.Exec(ctx,
			`INSERT INTO voucher_tags (voucher_id, tag_value) VALUES ($1, $2)`, voucherID, tag)
		require.NoError(t, err)
	}

	if len(viaOffering) > 0 {
		var offeringID uint64
		require.NoError(t, tx.QueryRow(ctx, `
			INSERT INTO offerings (voucher_id, seller_address, commodity_name, commodity_type,
				description, quantity, available)
			VALUES ($1, $2, 'Maize', 'GOOD', 'a sack', 1, TRUE)
			RETURNING id`, voucherID, randomAddress(t, tx)).Scan(&offeringID))
		for _, tag := range viaOffering {
			_, err := tx.Exec(ctx,
				`INSERT INTO offering_tags (offering_id, tag_value) VALUES ($1, $2)`, offeringID, tag)
			require.NoError(t, err)
		}
	}

	return voucherID
}

func filteredVoucherIDs(t *testing.T, pg *Pg, tx pgx.Tx, filter CatalogFilter) map[uint64]bool {
	t.Helper()
	vouchers, err := pg.ListMarketVouchersFiltered(
		context.Background(), tx, nil, filter, CatalogFirstPage, 0, 5000)
	require.NoError(t, err)

	found := map[uint64]bool{}
	for _, voucher := range vouchers {
		found[voucher.ID] = true
	}
	return found
}

func TestVoucherTagFilterFindsADirectlyTaggedVoucher(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()

	// Exactly the shape a deploy leaves behind: tags on the voucher, no offerings
	// yet. This is the case that matched nothing.
	direct := seedTaggedVoucher(t, tx, []string{"Art"}, nil)

	found := filteredVoucherIDs(t, pg, tx, CatalogFilter{Tags: []string{"Art"}})
	assert.True(t, found[direct],
		"a voucher tagged only in voucher_tags must be findable by that tag")
}

// The union is additive: the derived branch is untouched, so nothing that matched
// before stops matching. That is the whole reason this is a union and not a switch
// to voucher_tags — most of the catalog is tagged solely through its offerings.
func TestVoucherTagFilterStillFindsAnOfferingTaggedVoucher(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()

	derived := seedTaggedVoucher(t, tx, nil, []string{"Art"})

	found := filteredVoucherIDs(t, pg, tx, CatalogFilter{Tags: []string{"Art"}})
	assert.True(t, found[derived],
		"a voucher tagged through its offerings must still be findable")
}

func TestVoucherTagFilterExcludesAnUntaggedVoucher(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()

	untagged := seedTaggedVoucher(t, tx, []string{"Care"}, []string{"Care"})

	found := filteredVoucherIDs(t, pg, tx, CatalogFilter{Tags: []string{"Art"}})
	assert.False(t, found[untagged], "the filter must still exclude a non-matching voucher")
}

// An unavailable offering's tags are not the voucher's, and adding the direct
// branch must not have loosened that.
func TestVoucherTagFilterIgnoresUnavailableOfferings(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	voucherID := seedTaggedVoucher(t, tx, nil, []string{"Art"})
	_, err := tx.Exec(ctx, `UPDATE offerings SET available = FALSE WHERE voucher_id = $1`, voucherID)
	require.NoError(t, err)

	found := filteredVoucherIDs(t, pg, tx, CatalogFilter{Tags: []string{"Art"}})
	assert.False(t, found[voucherID], "an unavailable offering's tags are not the voucher's")
}

// All-tags mode across the two sources: one tag from each satisfies a two-tag
// all-match, because the voucher does carry both.
func TestVoucherTagFilterAllMatchSpansBothSources(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()

	split := seedTaggedVoucher(t, tx, []string{"Art"}, []string{"Care"})
	partial := seedTaggedVoucher(t, tx, []string{"Art"}, nil)

	filter := CatalogFilter{Tags: []string{"Art", "Care"}, TagMatch: TagMatchAll}
	found := filteredVoucherIDs(t, pg, tx, filter)

	assert.True(t, found[split], "all-match must count a tag from either source")
	assert.False(t, found[partial], "all-match must still require every requested tag")
}

// The DISTINCT is what stops repeats reaching cardinality($1). A voucher carrying
// one requested tag twice over — directly and through an offering — must not
// satisfy a two-tag all-match.
func TestVoucherTagFilterAllMatchDoesNotCountOneTagTwice(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()

	doubled := seedTaggedVoucher(t, tx, []string{"Art"}, []string{"Art"})

	filter := CatalogFilter{Tags: []string{"Art", "Care"}, TagMatch: TagMatchAll}
	found := filteredVoucherIDs(t, pg, tx, filter)
	assert.False(t, found[doubled], "one tag arriving twice is still one tag")
}

func TestVoucherTagFacetsCountBothSourcesOncePerVoucher(t *testing.T) {
	pg, tx, cleanup := newMarketTestPg(t)
	defer cleanup()
	ctx := context.Background()

	before := facetCount(t, pg, ctx, tx, "Art")

	seedTaggedVoucher(t, tx, []string{"Art"}, nil)             // direct only
	seedTaggedVoucher(t, tx, nil, []string{"Art"})             // derived only
	seedTaggedVoucher(t, tx, []string{"Art"}, []string{"Art"}) // both — still one

	after := facetCount(t, pg, ctx, tx, "Art")
	assert.Equal(t, before+3, after,
		"each voucher counts once, whichever source carries the tag")
}

func facetCount(t *testing.T, pg *Pg, ctx context.Context, tx pgx.Tx, tag string) int {
	t.Helper()
	facets, err := pg.ListMarketVoucherTagFacets(ctx, tx, nil, CatalogFilter{})
	require.NoError(t, err)
	for _, facet := range facets {
		if facet.Tag == tag {
			return int(facet.Count)
		}
	}
	return 0
}
