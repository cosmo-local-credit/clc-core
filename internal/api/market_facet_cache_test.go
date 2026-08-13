package api

import (
	"testing"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
)

func TestMarketFacetCacheServesWithinItsTTL(t *testing.T) {
	now := time.Now()
	cache := newMarketFacetCache(func() time.Time { return now })

	if _, ok := cache.get(); ok {
		t.Fatal("an empty cache reported a hit")
	}

	cache.set([]store.TagFacet{{Tag: "Art", Count: 3}})

	got, ok := cache.get()
	if !ok {
		t.Fatal("a freshly populated cache reported a miss")
	}
	if len(got) != 1 || got[0].Tag != "Art" || got[0].Count != 3 {
		t.Errorf("cached facet = %v, want one Art/3 entry", got)
	}

	// One tick short of the TTL is still a hit; the TTL itself is not.
	now = now.Add(marketFacetCacheTTL - time.Millisecond)
	if _, ok := cache.get(); !ok {
		t.Error("cache expired before its TTL elapsed")
	}

	now = now.Add(time.Millisecond)
	if _, ok := cache.get(); ok {
		t.Errorf("cache still served after %s, its full TTL", marketFacetCacheTTL)
	}
}

// The cached slice outlives the request that produced it, so both the value handed
// in and the value handed out have to be copies. A caller that sorted a facet in
// place would otherwise reorder every later response, and the JSON encoder hands
// these straight to callers.
func TestMarketFacetCacheDoesNotShareItsBackingArray(t *testing.T) {
	now := time.Now()
	cache := newMarketFacetCache(func() time.Time { return now })

	source := []store.TagFacet{{Tag: "Art", Count: 3}, {Tag: "Culture", Count: 1}}
	cache.set(source)

	// Mutating the caller's slice must not reach the cache.
	source[0] = store.TagFacet{Tag: "Mutated", Count: 99}

	first, ok := cache.get()
	if !ok {
		t.Fatal("cache reported a miss")
	}
	if first[0].Tag != "Art" {
		t.Errorf("mutating the source slice changed the cached facet to %v", first)
	}

	// Mutating a returned slice must not reach the cache either.
	first[0] = store.TagFacet{Tag: "AlsoMutated", Count: 99}

	second, ok := cache.get()
	if !ok {
		t.Fatal("cache reported a miss on the second read")
	}
	if second[0].Tag != "Art" {
		t.Errorf("mutating a returned facet changed the cache to %v", second)
	}
}
