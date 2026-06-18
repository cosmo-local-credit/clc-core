package relayer

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/util"
)

const (
	challengeTTL = 5 * time.Minute
	// 32 bytes per WebAuthn spec recommendation; encoded as base64url
	// without padding (43 chars).
	challengeBytes = 32
)

type challengeEntry struct {
	createdAt time.Time
}

// challengeStore holds short-lived WebAuthn challenges. One-time use:
// VerifyAssertion deletes the entry whether or not verification succeeds.
//
// This is an in-process store, fine for a single-instance deployment. Swap
// for Redis when horizontal scaling is needed.
type challengeStore struct {
	mu sync.Mutex
	m  map[string]challengeEntry
}

func newChallengeStore() *challengeStore {
	return &challengeStore{m: make(map[string]challengeEntry)}
}

func (s *challengeStore) put(challenge string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[challenge] = challengeEntry{createdAt: time.Now()}
	// Opportunistic eviction so the map doesn't grow unboundedly under
	// abuse. O(n) but n is small in practice.
	if len(s.m) > 1024 {
		now := time.Now()
		for k, v := range s.m {
			if now.Sub(v.createdAt) > challengeTTL {
				delete(s.m, k)
			}
		}
	}
}

// take returns true if the challenge existed and was within TTL. The entry
// is removed in all cases to make replay impossible.
func (s *challengeStore) take(challenge string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.m[challenge]
	if !ok {
		return false
	}
	delete(s.m, challenge)
	return time.Since(entry.createdAt) <= challengeTTL
}

// IssueChallenge generates a fresh 32-byte challenge, stores it, and returns
// the base64url (no-padding) encoding.
func (r *Relayer) IssueChallenge(ctx context.Context) (string, error) {
	_ = ctx
	buf := make([]byte, challengeBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("webauthn: challenge rand: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(buf)
	r.challenges.put(encoded)
	return encoded, nil
}

// VerifyAssertionArgs collects everything VerifyAssertion needs. PublicKeyX
// and PublicKeyY are decimal-string-encoded P-256 coordinates as stored on
// the credential record.
type VerifyAssertionArgs struct {
	AuthenticatorData string // base64url
	ClientDataJSON    string // base64url
	Signature         string // base64url, DER-encoded ECDSA
	PublicKeyX        string // decimal string
	PublicKeyY        string // decimal string
}

type clientData struct {
	Type        string `json:"type"`
	Challenge   string `json:"challenge"`
	Origin      string `json:"origin"`
	CrossOrigin bool   `json:"crossOrigin"`
}

// VerifyAssertion runs the WebAuthn assertion checks needed to recover a
// wallet: clientDataJSON parsing, origin / type / challenge checks, UP/UV
// flag check, and a P-256 signature verification.
//
// The challenge is consumed (deleted) before signature verification so a
// failed call can't be retried with the same challenge.
func (r *Relayer) VerifyAssertion(ctx context.Context, args VerifyAssertionArgs) error {
	_ = ctx

	authData, err := base64.RawURLEncoding.DecodeString(args.AuthenticatorData)
	if err != nil {
		// Some clients pad — try padded form too.
		authData, err = base64.URLEncoding.DecodeString(args.AuthenticatorData)
		if err != nil {
			return fmt.Errorf("webauthn: decode authenticatorData: %w", err)
		}
	}
	if len(authData) < 37 {
		return errors.New("webauthn: authenticatorData too short")
	}

	clientDataBytes, err := base64.RawURLEncoding.DecodeString(args.ClientDataJSON)
	if err != nil {
		clientDataBytes, err = base64.URLEncoding.DecodeString(args.ClientDataJSON)
		if err != nil {
			return fmt.Errorf("webauthn: decode clientDataJSON: %w", err)
		}
	}

	sigBytes, err := base64.RawURLEncoding.DecodeString(args.Signature)
	if err != nil {
		sigBytes, err = base64.URLEncoding.DecodeString(args.Signature)
		if err != nil {
			return fmt.Errorf("webauthn: decode signature: %w", err)
		}
	}

	var cd clientData
	if err := json.Unmarshal(clientDataBytes, &cd); err != nil {
		return fmt.Errorf("webauthn: parse clientDataJSON: %w", err)
	}

	if cd.Type != "webauthn.get" {
		return fmt.Errorf("webauthn: clientData.type = %q, want webauthn.get", cd.Type)
	}
	if !originAllowed(cd.Origin, r.allowedOrigins) {
		return fmt.Errorf("webauthn: origin %q not allowed", cd.Origin)
	}

	// Consume challenge first so a failed verification cannot be retried.
	if !r.challenges.take(cd.Challenge) {
		return errors.New("webauthn: challenge unknown, expired, or already consumed")
	}

	// authData[32] flags: bit 0 = UP (User Present), bit 2 = UV (User Verified).
	flags := authData[32]
	const upBit = 0x01
	const uvBit = 0x04
	if flags&upBit == 0 {
		return errors.New("webauthn: UP flag not set")
	}
	if flags&uvBit == 0 {
		return errors.New("webauthn: UV flag not set")
	}

	// Verify P-256 signature over sha256(authData || sha256(clientDataJSON)).
	cdHash := sha256.Sum256(clientDataBytes)
	verificationData := make([]byte, 0, len(authData)+len(cdHash))
	verificationData = append(verificationData, authData...)
	verificationData = append(verificationData, cdHash[:]...)
	digest := sha256.Sum256(verificationData)

	x, ok := new(big.Int).SetString(args.PublicKeyX, 10)
	if !ok {
		return errors.New("webauthn: invalid stored publicKeyX")
	}
	y, ok := new(big.Int).SetString(args.PublicKeyY, 10)
	if !ok {
		return errors.New("webauthn: invalid stored publicKeyY")
	}

	pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}
	if !ecdsa.VerifyASN1(pub, digest[:], sigBytes) {
		return errors.New("webauthn: signature verification failed")
	}

	return nil
}

func originAllowed(origin string, allowed []string) bool {
	return util.OriginAllowed(origin, allowed)
}
