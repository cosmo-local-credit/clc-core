// Package testsign builds and signs Calibur batches the way the dapp does,
// for use in backend tests. It produces the exact wire fields the
// /relayer/relay endpoint expects (signedBatch, signature, signatureType) for
// both a root secp256k1 key and a WebAuthn-P256 passkey, so tests can verify
// our relay integration end to end without a browser.
package testsign

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"math/big"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	gocalibur "github.com/grassrootseconomics/go-calibur"
)

// SignedRelay is the signed payload, ready to drop into apiresp.RelayRequest.
type SignedRelay struct {
	SignedBatch   apiresp.CaliburSignedBatch
	Signature     string // 0x-hex wrapped signature
	SignatureType string // "secp256k1" | "webauthn"
	// Registration fields, populated for WebAuthn so the relay can persist the
	// credential mapping on first use.
	KeyHash    string
	PublicKeyX string
	PublicKeyY string
}

// Params are the shared batch parameters.
type Params struct {
	ChainID  *big.Int
	Calibur  common.Address
	User     common.Address
	Batch    apiresp.CaliburBatchedCall
	Seq      uint64         // Calibur sequence for the signing key
	Executor common.Address // zero == anyone
	Deadline *big.Int       // zero == no deadline
}

// Secp256k1 signs the batch with a root secp256k1 key (keyHash == 0), matching
// gocalibur.SignSecp256k1 / the dapp's private-key connector.
func Secp256k1(key *ecdsa.PrivateKey, p Params) (SignedRelay, error) {
	var keyHash [32]byte // root key
	nonce := buildCaliburNonce(keyHash, p.Seq)

	signed := gocalibur.SignedBatchedCall{
		BatchedCall: toGoCaliburBatch(p.Batch),
		Nonce:       nonce,
		KeyHash:     keyHash,
		Executor:    p.Executor,
		Deadline:    p.Deadline,
	}
	domain := gocalibur.CaliburDomain(p.ChainID, p.User, p.Calibur)

	sig, err := gocalibur.SignSecp256k1(key, domain, signed, nil)
	if err != nil {
		return SignedRelay{}, fmt.Errorf("testsign: secp256k1: %w", err)
	}

	return SignedRelay{
		SignedBatch:   toAPISignedBatch(p, nonce, keyHash),
		Signature:     hexEncode(sig),
		SignatureType: "secp256k1",
	}, nil
}

// WebAuthn signs the batch with a P-256 passkey, producing the WebAuthn-wrapped
// signature (keyHash prefix + abi-encoded WebAuthnAuth) Calibur expects for a
// registered passkey, matching the dapp's calibur-webauthn connector.
func WebAuthn(key *ecdsa.PrivateKey, rpID, origin string, p Params) (SignedRelay, error) {
	pub := key.PublicKey
	keyObj := gocalibur.NewWebAuthnP256Key(pub.X, pub.Y)
	keyHash := keyObj.KeyHash()

	nonce := buildCaliburNonce(keyHash, p.Seq)
	signed := gocalibur.SignedBatchedCall{
		BatchedCall: toGoCaliburBatch(p.Batch),
		Nonce:       nonce,
		KeyHash:     keyHash,
		Executor:    p.Executor,
		Deadline:    p.Deadline,
	}
	domain := gocalibur.CaliburDomain(p.ChainID, p.User, p.Calibur)
	digest := gocalibur.ComputeDigest(domain, signed)

	// The WebAuthn challenge is the base64url (no-pad) EIP-712 digest.
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	clientDataJSON := fmt.Sprintf(
		`{"type":"webauthn.get","challenge":"%s","origin":"%s","crossOrigin":false}`,
		challenge, origin,
	)

	// authenticatorData = rpIdHash(32) || flags(1, UP|UV) || signCount(4).
	rpIDHash := sha256.Sum256([]byte(rpID))
	authData := make([]byte, 0, 37)
	authData = append(authData, rpIDHash[:]...)
	authData = append(authData, 0x05)       // UP (0x01) | UV (0x04)
	authData = append(authData, 0, 0, 0, 1) // signCount

	// The authenticator signs sha256(authData || sha256(clientDataJSON)).
	cdHash := sha256.Sum256([]byte(clientDataJSON))
	signData := append(append([]byte{}, authData...), cdHash[:]...)

	r, s, err := gocalibur.SignP256(key, signData)
	if err != nil {
		return SignedRelay{}, fmt.Errorf("testsign: p256 sign: %w", err)
	}

	auth, err := gocalibur.ParseWebAuthnAuthFromBrowser(authData, clientDataJSON, r, s)
	if err != nil {
		return SignedRelay{}, fmt.Errorf("testsign: parse webauthn: %w", err)
	}
	encoded, err := gocalibur.EncodeWebAuthnAuth(auth)
	if err != nil {
		return SignedRelay{}, fmt.Errorf("testsign: encode webauthn: %w", err)
	}
	wrapped := gocalibur.WrapSignatureWithKeyHash(keyHash, encoded, nil)

	out := toAPISignedBatch(p, nonce, keyHash)
	return SignedRelay{
		SignedBatch:   out,
		Signature:     hexEncode(wrapped),
		SignatureType: "webauthn",
		KeyHash:       common.BytesToHash(keyHash[:]).Hex(),
		PublicKeyX:    pub.X.String(),
		PublicKeyY:    pub.Y.String(),
	}, nil
}

func toGoCaliburBatch(b apiresp.CaliburBatchedCall) gocalibur.BatchedCall {
	calls := make([]gocalibur.Call, 0, len(b.Calls))
	for _, c := range b.Calls {
		value, _ := new(big.Int).SetString(c.Value, 10)
		if value == nil {
			value = big.NewInt(0)
		}
		calls = append(calls, gocalibur.Call{
			To:    common.HexToAddress(c.To),
			Value: value,
			Data:  common.FromHex(c.Data),
		})
	}
	return gocalibur.BatchedCall{Calls: calls, RevertOnFailure: b.RevertOnFailure}
}

func toAPISignedBatch(p Params, nonce *big.Int, keyHash [32]byte) apiresp.CaliburSignedBatch {
	deadline := p.Deadline
	if deadline == nil {
		deadline = big.NewInt(0)
	}
	return apiresp.CaliburSignedBatch{
		BatchedCall: p.Batch,
		Nonce:       nonce.String(),
		KeyHash:     common.BytesToHash(keyHash[:]).Hex(),
		Executor:    p.Executor.Hex(),
		Deadline:    deadline.String(),
	}
}

// buildCaliburNonce packs a 192-bit key prefix and a 64-bit sequence into the
// single uint256 Calibur uses as a per-key nonce: (keyHash[:24] << 64) | seq.
func buildCaliburNonce(keyHash [32]byte, seq uint64) *big.Int {
	keyBig := new(big.Int).SetBytes(keyHash[:])
	mask := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 192), big.NewInt(1))
	keyBig.And(keyBig, mask)
	keyBig.Lsh(keyBig, 64)
	return keyBig.Or(keyBig, new(big.Int).SetUint64(seq))
}

func hexEncode(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 2+len(b)*2)
	out[0], out[1] = '0', 'x'
	for i, v := range b {
		out[2+i*2] = hexdigits[v>>4]
		out[3+i*2] = hexdigits[v&0x0f]
	}
	return string(out)
}
