package util

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

// ParseSIWEAddress parses the signer address from an EIP-4361 message.
// Per the spec, line 1 is the domain preamble and line 2 is the raw Ethereum address.
// Returns the EIP-55 checksummed address — the canonical form stored in the DB.
func ParseSIWEAddress(message string) (string, error) {
	lines := strings.Split(message, "\n")
	if len(lines) < 2 {
		return "", fmt.Errorf("siwe: message too short to contain an address")
	}

	raw := strings.TrimSpace(lines[1])
	if !common.IsHexAddress(raw) {
		return "", fmt.Errorf("siwe: invalid ethereum address on line 2: %q", raw)
	}

	// common.HexToAddress normalises any input casing; .Hex() returns the
	// EIP-55 checksummed form. Callers compare with strings.EqualFold and
	// lookups are case-insensitive, so casing never affects correctness.
	return common.HexToAddress(raw).Hex(), nil
}

// ParseSIWENonce scans an EIP-4361 message for the "Nonce: " field and returns
// its value. Returns an error when the field is not present.
func ParseSIWENonce(message string) (string, error) {
	const prefix = "Nonce: "

	for line := range strings.SplitSeq(message, "\n") {
		if after, ok := strings.CutPrefix(line, prefix); ok {
			nonce := strings.TrimSpace(after)
			if nonce == "" {
				return "", fmt.Errorf("siwe: nonce field is empty")
			}
			return nonce, nil
		}
	}

	return "", fmt.Errorf("siwe: nonce field not found in message")
}

// VerifySIWESignature verifies an Ethereum personal_sign (EIP-191) signature
// over the given EIP-4361 message and returns the recovered signer address as
// a lowercase hex string.
//
// The signature must be a 0x-prefixed 65-byte hex string ([R|S|V]).
// V values of 27 or 28 (legacy Ethereum) are normalised to 0/1 before recovery.
func VerifySIWESignature(message, signature string) (string, error) {
	sigBytes, err := hexutil.Decode(signature)
	if err != nil {
		return "", fmt.Errorf("siwe: decode signature hex: %w", err)
	}
	if len(sigBytes) != 65 {
		return "", fmt.Errorf("siwe: expected 65-byte signature, got %d bytes", len(sigBytes))
	}

	// Normalise the recovery ID (V): Ethereum personal_sign historically uses
	// V = 27 or 28; crypto.SigToPub expects 0 or 1.
	if sigBytes[64] >= 27 {
		sigBytes[64] -= 27
	}

	// Hash using the Ethereum personal_sign prefix ("\x19Ethereum Signed Message:\n<len><msg>").
	hash := accounts.TextHash([]byte(message))

	pubKey, err := crypto.SigToPub(hash, sigBytes)
	if err != nil {
		return "", fmt.Errorf("siwe: recover public key: %w", err)
	}

	addr := crypto.PubkeyToAddress(*pubKey)
	// Return the EIP-55 checksummed address (the canonical DB form). Callers
	// compare with strings.EqualFold and lookups are case-insensitive.
	return addr.Hex(), nil
}

// ParseSIWEStatement returns the optional EIP-4361 statement: the line after
// the blank line that follows the address. A message without one returns "".
func ParseSIWEStatement(message string) string {
	lines := strings.Split(message, "\n")
	if len(lines) < 5 || strings.TrimSpace(lines[2]) != "" || strings.TrimSpace(lines[4]) != "" {
		return ""
	}
	return strings.TrimSpace(lines[3])
}

// ParseSIWEChainID returns the "Chain ID: " field of an EIP-4361 message.
func ParseSIWEChainID(message string) (uint64, error) {
	const prefix = "Chain ID: "
	for line := range strings.SplitSeq(message, "\n") {
		if after, ok := strings.CutPrefix(line, prefix); ok {
			var chainID uint64
			if _, err := fmt.Sscan(strings.TrimSpace(after), &chainID); err != nil {
				return 0, fmt.Errorf("siwe: invalid chain id: %w", err)
			}
			return chainID, nil
		}
	}
	return 0, fmt.Errorf("siwe: chain id field not found in message")
}

// keyRotationStatementPrefix starts the statement every proof for one key
// rotation carries, so a signature made to sign in can never move an account.
// clc-app's buildKeyRotationStatement in @clc/core writes the same text.
const (
	keyRotationStatementPrefix = "Replace sign-in card "
)

// KeyRotationStatement is the SIWE statement for moving an account from
// oldAddress to newAddress. Both are written checksummed.
func KeyRotationStatement(oldAddress, newAddress common.Address) string {
	return keyRotationStatementPrefix + oldAddress.Hex() + " with " + newAddress.Hex()
}

// KeyRotationPasskeyChallenge is the WebAuthn challenge a passkey signs to
// confirm a key rotation: base64url (unpadded) of
// sha256(statement + "\n" + nonce). It commits to both addresses through the
// statement and is single use through the SIWE nonce, so neither a sign-in
// assertion nor one made for another destination can confirm this rotation.
// clc-app's keyRotationPasskeyChallenge in @clc/core computes the same bytes.
func KeyRotationPasskeyChallenge(statement, nonce string) string {
	digest := sha256.Sum256([]byte(statement + "\n" + nonce))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

// ParseKeyRotationStatement reads the old and new addresses from a key
// rotation statement written by KeyRotationStatement.
func ParseKeyRotationStatement(statement string) (oldAddress, newAddress common.Address, err error) {
	rest, ok := strings.CutPrefix(statement, keyRotationStatementPrefix)
	if !ok {
		return oldAddress, newAddress, fmt.Errorf("siwe: not a key rotation statement")
	}
	oldRaw, newRaw, ok := strings.Cut(rest, " with ")
	if !ok || !common.IsHexAddress(oldRaw) || !common.IsHexAddress(newRaw) {
		return oldAddress, newAddress, fmt.Errorf("siwe: malformed key rotation statement")
	}
	oldAddress, newAddress = common.HexToAddress(oldRaw), common.HexToAddress(newRaw)
	if KeyRotationStatement(oldAddress, newAddress) != statement {
		return oldAddress, newAddress, fmt.Errorf("siwe: key rotation statement is not canonical")
	}
	if oldAddress == newAddress {
		return oldAddress, newAddress, fmt.Errorf("siwe: key rotation must change the address")
	}
	return oldAddress, newAddress, nil
}
