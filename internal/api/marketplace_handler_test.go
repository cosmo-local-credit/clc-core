package api

import (
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/stretchr/testify/require"
)

func TestMarketplaceCursorScopeIncludesDiscoveryModeAndKinds(t *testing.T) {
	filter := store.CatalogFilter{Term: "repair", Tags: []string{"Services"}}
	global := marketplaceOrigin(7, filter, []string{"offering", "voucher"})
	filter.Nearby = &store.CatalogLocation{Lat: 0, Lng: 0}
	nearby := marketplaceOrigin(7, filter, []string{"offering", "voucher"})
	differentKinds := marketplaceOrigin(7, filter, []string{"pool"})
	require.NotEqual(t, global, nearby)
	require.NotEqual(t, nearby, differentKinds)
}

func TestMarketplaceCursorScopeIncludesCanonicalBounds(t *testing.T) {
	left := store.CatalogBounds{MinLng: -180, MinLat: -5, MaxLng: -179, MaxLat: 5}
	right := store.CatalogBounds{MinLng: 179, MinLat: -5, MaxLng: 180, MaxLat: 5}
	global := marketplaceOrigin(7, store.CatalogFilter{}, nil)
	bounded := marketplaceOrigin(7, store.CatalogFilter{Bounds: []store.CatalogBounds{left, right}}, nil)
	reordered := marketplaceOrigin(7, store.CatalogFilter{Bounds: []store.CatalogBounds{right, left}}, nil)

	require.NotEqual(t, global, bounded)
	// Request parsing canonicalizes before the cursor scope is computed. Prove
	// that canonical order itself is stable as a guard against cursor churn.
	normalized, err := store.NormalizeCatalogBounds([]store.CatalogBounds{right, left})
	require.NoError(t, err)
	require.Equal(t, bounded, marketplaceOrigin(7, store.CatalogFilter{Bounds: normalized}, nil))
	require.NotEqual(t, bounded, reordered)
}
