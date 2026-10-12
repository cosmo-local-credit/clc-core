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
