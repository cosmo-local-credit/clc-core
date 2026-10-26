package api

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"golang.org/x/sync/singleflight"
	"golang.org/x/time/rate"
)

func TestMarketMapCacheExpiresAndPromotesRecentEntries(t *testing.T) {
	clock := newFakeClock()
	cache := newMarketMapCache(clock.Now)

	result := func(id string) store.MarketMapResult {
		kind := "voucher"
		name := "A voucher with enough text to make eviction deterministic"
		return store.MarketMapResult{Features: []store.MarketMapFeature{{
			Type: "marker", ID: id, Kind: &kind, ResourceID: &id, Name: &name,
		}}}
	}

	encoded, err := json.Marshal(result("one"))
	if err != nil {
		t.Fatal(err)
	}
	cache.maxBytes = len(encoded) * 2

	cache.set("first", result("one"))
	cache.set("second", result("two"))
	if _, ok := cache.get("first"); !ok {
		t.Fatal("first entry was not cached")
	}
	cache.set("third", result("six"))
	if _, ok := cache.get("second"); ok {
		t.Fatal("least-recently-used entry survived the byte limit")
	}
	if _, ok := cache.get("first"); !ok {
		t.Fatal("recently read entry was evicted")
	}

	clock.Advance(marketMapCacheTTL)
	if _, ok := cache.get("first"); ok {
		t.Fatal("expired entry was returned")
	}
}

func TestMarketMapExecutorRejectsBeyondQueueCapacity(t *testing.T) {
	executor := newMarketMapExecutor()
	executor.pending.Store(marketMapConcurrency + marketMapQueue)
	_, err := executor.run(context.Background(), func(context.Context) (store.MarketMapResult, error) {
		t.Fatal("capacity-rejected load ran")
		return store.MarketMapResult{}, nil
	})
	if !errors.Is(err, errMarketMapCapacity) {
		t.Fatalf("error = %v, want errMarketMapCapacity", err)
	}
}

func TestMarketMapExecutorHonorsCancelledQueuedRequest(t *testing.T) {
	executor := newMarketMapExecutor()
	for range marketMapConcurrency {
		executor.slots <- struct{}{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := executor.run(ctx, func(context.Context) (store.MarketMapResult, error) {
		t.Fatal("cancelled load ran")
		return store.MarketMapResult{}, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestMarketMapCacheSkipsEntryLargerThanCapacity(t *testing.T) {
	cache := newMarketMapCache(time.Now)
	cache.maxBytes = 1
	cache.set("large", store.MarketMapResult{Features: []store.MarketMapFeature{{Type: "cluster", ID: "large"}}})
	if _, ok := cache.get("large"); ok {
		t.Fatal("oversized entry was cached")
	}
}

func TestMarketMapLoadsCoalesceAndCacheHitsDoNotSpendTokens(t *testing.T) {
	api := &API{
		marketMapCache:    newMarketMapCache(time.Now),
		marketMapExecutor: newMarketMapExecutor(),
	}
	limiter := newMarketRateLimiterWithCapacity(rate.Limit(0), 2, 10, true, time.Now)

	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	var loads atomic.Int64
	load := func(context.Context) (store.MarketMapResult, error) {
		loads.Add(1)
		startedOnce.Do(func() { close(started) })
		<-release
		return store.MarketMapResult{Features: []store.MarketMapFeature{{Type: "cluster", ID: "shared"}}}, nil
	}

	const waiters = 10_000
	results := make([]<-chan singleflight.Result, 0, waiters)
	for range waiters {
		results = append(results, api.loadMarketMap("shared", limiter, "guest-ip", load))
	}
	<-started
	close(release)
	for _, resultChannel := range results {
		result := <-resultChannel
		if result.Err != nil {
			t.Fatal(result.Err)
		}
	}
	if got := loads.Load(); got != 1 {
		t.Fatalf("loads = %d, want 1", got)
	}

	var cachedLoadCalls atomic.Int64
	cached := <-api.loadMarketMap("shared", limiter, "guest-ip", func(context.Context) (store.MarketMapResult, error) {
		cachedLoadCalls.Add(1)
		return store.MarketMapResult{}, nil
	})
	if cached.Err != nil {
		t.Fatal(cached.Err)
	}
	if cachedLoadCalls.Load() != 0 {
		t.Fatal("cache hit invoked the loader")
	}

	second := <-api.loadMarketMap("second", limiter, "guest-ip", func(context.Context) (store.MarketMapResult, error) {
		return store.MarketMapResult{}, nil
	})
	if second.Err != nil {
		t.Fatalf("cache hit spent the second limiter token: %v", second.Err)
	}

	third := <-api.loadMarketMap("third", limiter, "guest-ip", func(context.Context) (store.MarketMapResult, error) {
		return store.MarketMapResult{}, nil
	})
	var limitErr marketMapRateLimitError
	if !errors.As(third.Err, &limitErr) {
		t.Fatalf("third unique load error = %v, want marketMapRateLimitError", third.Err)
	}
}
