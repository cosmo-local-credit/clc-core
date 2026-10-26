package api

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
)

const (
	marketMapCacheTTL      = 15 * time.Second
	marketMapCacheMaxBytes = 64 << 20
	marketMapConcurrency   = 32
	marketMapQueue         = 128
)

var (
	errMarketMapCapacity = errors.New("market map capacity is temporarily full")
)

type (
	marketMapCacheEntry struct {
		key       string
		result    store.MarketMapResult
		loadedAt  time.Time
		sizeBytes int
	}

	marketMapCache struct {
		mu       sync.Mutex
		entries  map[string]*list.Element
		order    *list.List
		bytes    int
		now      func() time.Time
		ttl      time.Duration
		maxBytes int
	}

	marketMapExecutor struct {
		slots   chan struct{}
		pending atomic.Int64
	}
)

func newMarketMapCache(now func() time.Time) *marketMapCache {
	return &marketMapCache{
		entries:  make(map[string]*list.Element),
		order:    list.New(),
		now:      now,
		ttl:      marketMapCacheTTL,
		maxBytes: marketMapCacheMaxBytes,
	}
}

func (c *marketMapCache) get(key string) (store.MarketMapResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[key]
	if !ok {
		return store.MarketMapResult{}, false
	}
	entry, ok := element.Value.(*marketMapCacheEntry)
	if !ok {
		c.removeInvalidElement(key, element)
		return store.MarketMapResult{}, false
	}
	if c.now().Sub(entry.loadedAt) >= c.ttl {
		c.remove(element)
		return store.MarketMapResult{}, false
	}
	c.order.MoveToFront(element)
	return entry.result, true
}

func (c *marketMapCache) set(key string, result store.MarketMapResult) {
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > c.maxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.entries[key]; ok {
		c.remove(existing)
	}
	entry := &marketMapCacheEntry{key: key, result: result, loadedAt: c.now(), sizeBytes: len(encoded)}
	element := c.order.PushFront(entry)
	c.entries[key] = element
	c.bytes += entry.sizeBytes
	for c.bytes > c.maxBytes && c.order.Len() > 0 {
		c.remove(c.order.Back())
	}
}

func (c *marketMapCache) remove(element *list.Element) {
	entry, ok := element.Value.(*marketMapCacheEntry)
	if !ok {
		c.order.Remove(element)
		return
	}
	delete(c.entries, entry.key)
	c.order.Remove(element)
	c.bytes -= entry.sizeBytes
}

func (c *marketMapCache) removeInvalidElement(key string, element *list.Element) {
	delete(c.entries, key)
	c.order.Remove(element)
}

func newMarketMapExecutor() *marketMapExecutor {
	return &marketMapExecutor{slots: make(chan struct{}, marketMapConcurrency)}
}

func (e *marketMapExecutor) run(
	ctx context.Context,
	load func(context.Context) (store.MarketMapResult, error),
) (store.MarketMapResult, error) {
	if e.pending.Add(1) > marketMapConcurrency+marketMapQueue {
		e.pending.Add(-1)
		return store.MarketMapResult{}, errMarketMapCapacity
	}
	defer e.pending.Add(-1)
	select {
	case e.slots <- struct{}{}:
		defer func() { <-e.slots }()
	case <-ctx.Done():
		return store.MarketMapResult{}, ctx.Err()
	}
	return load(ctx)
}
