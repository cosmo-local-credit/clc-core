package api

import (
	"strings"
	"sync"
	"time"
)

// Owners recently read as not a multisig.
//
// actingOwner discovers an unknown owner Safe with head reads inside the
// handler's transaction, sometimes under a row lock. Every non-owner write on
// an EOA-owned asset would otherwise repeat those reads, since an EOA is never
// stored. Entries expire because a counterfactual Safe address can gain code
// after an asset is handed to it; explicit inspection (GET /owners/:address)
// always reads through.

const (
	safeMissTTL = 10 * time.Minute
	// safeMissMax bounds memory. A full map drops expired entries first and
	// starts over if that frees nothing, which costs only repeated reads.
	safeMissMax = 10_000
	// safeReadTimeout bounds discovery reads: up to four sequential calls.
	safeReadTimeout = 5 * time.Second
)

type (
	safeMissCache struct {
		mu      sync.Mutex
		entries map[string]time.Time
		now     func() time.Time
	}
)

func newSafeMissCache(now func() time.Time) *safeMissCache {
	return &safeMissCache{entries: make(map[string]time.Time), now: now}
}

// recent reports a fresh miss. A nil cache (tests building API directly) never
// remembers anything.
func (c *safeMissCache) recent(address string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	at, ok := c.entries[strings.ToLower(address)]
	return ok && c.now().Sub(at) < safeMissTTL
}

func (c *safeMissCache) add(address string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if len(c.entries) >= safeMissMax {
		for a, at := range c.entries {
			if now.Sub(at) >= safeMissTTL {
				delete(c.entries, a)
			}
		}
		if len(c.entries) >= safeMissMax {
			c.entries = make(map[string]time.Time)
		}
	}
	c.entries[strings.ToLower(address)] = now
}
