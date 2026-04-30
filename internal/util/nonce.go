package util

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// NonceStore is a thread-safe in-memory store for SIWE challenge nonces.
// Each nonce has a 5-minute TTL after which it is considered expired.
type NonceStore struct {
	mu     sync.Mutex
	nonces map[string]time.Time
}

// NewNonceStore creates a new, empty NonceStore.
func NewNonceStore() *NonceStore {
	return &NonceStore{
		nonces: make(map[string]time.Time),
	}
}

// Generate creates a cryptographically random 32-hex-character nonce, records
// it with a 5-minute expiry, and returns the nonce string.
func (n *NonceStore) Generate() string {
	b := make([]byte, 16) // 16 bytes → 32 hex characters
	if _, err := rand.Read(b); err != nil {
		// crypto/rand.Read only fails on catastrophic OS-level errors; panic is
		// appropriate here as the application cannot safely continue.
		panic("util: nonce: crypto/rand.Read failed: " + err.Error())
	}

	nonce := hex.EncodeToString(b)

	n.mu.Lock()
	n.nonces[nonce] = time.Now().Add(5 * time.Minute)
	n.mu.Unlock()

	return nonce
}

// Consume checks that the nonce exists and has not yet expired, then deletes
// it (one-time use). Returns true when the nonce is valid, false otherwise.
func (n *NonceStore) Consume(nonce string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()

	expiry, ok := n.nonces[nonce]
	if !ok {
		return false
	}

	// Always delete so a replayed or expired nonce cannot be tried again.
	delete(n.nonces, nonce)

	return time.Now().Before(expiry)
}

// StartCleanup launches a background goroutine that periodically removes
// expired nonces from the store. Call once at application startup, e.g.:
//
//	store.StartCleanup(time.Minute)
func (n *NonceStore) StartCleanup(interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for range ticker.C {
			now := time.Now()

			n.mu.Lock()
			for k, expiry := range n.nonces {
				if now.After(expiry) {
					delete(n.nonces, k)
				}
			}
			n.mu.Unlock()
		}
	}()
}
