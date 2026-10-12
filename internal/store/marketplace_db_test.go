package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// Executes the full static union against a migrated PostgreSQL database. The
// shared helper skips this test when CORE_TEST_DSN is not configured.
func TestMarketplaceDiscoveryQueryExecutesDB(t *testing.T) {
	pg, tx, cleanup := newCatalogSearchTestPg(t)
	defer cleanup()
	ctx := context.Background()

	rows, err := pg.ListMarketplace(
		ctx,
		tx,
		CatalogFilter{},
		nil,
		nil,
		MarketplaceCursor{},
		2,
		true,
	)
	require.NoError(t, err)
	if len(rows) > 0 {
		var facets []TagFacet
		require.NoError(t, json.Unmarshal(rows[0].TagFacets, &facets))
	}

	origin := &CatalogLocation{Lat: 0, Lng: 0}
	_, err = pg.ListMarketplace(
		ctx,
		tx,
		CatalogFilter{Nearby: origin},
		[]string{"pool", "voucher"},
		origin,
		MarketplaceCursor{},
		2,
		false,
	)
	require.NoError(t, err)
}
