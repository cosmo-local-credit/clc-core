package util

import (
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

// ParseSIWEAddress parses the signer address from an EIP-4361 message.
// Per the spec, line 1 is the domain preamble and line 2 is the raw Ethereum address.
// Returns the address as a lowercase hex string.
func ParseSIWEAddress(message string) (string, error) {
	lines := strings.Split(message, "\n")
	if len(lines) < 2 {
		return "", fmt.Errorf("siwe: message too short to contain an address")
	}

	raw := strings.TrimSpace(lines[1])
	if !common.IsHexAddress(raw) {
		return "", fmt.Errorf("siwe: invalid ethereum address on line 2: %q", raw)
	}

	// Use common.HexToAddress to normalise (handles EIP-55 checksum variants),
	// then return as lowercase.
	return strings.ToLower(common.HexToAddress(raw).Hex()), nil
}

// ParseSIWENonce scans an EIP-4361 message for the "Nonce: " field and returns
// its value. Returns an error when the field is not present.
func ParseSIWENonce(message string) (string, error) {
	const prefix = "Nonce: "

	for _, line := range strings.Split(message, "\n") {
		if strings.HasPrefix(line, prefix) {
			nonce := strings.TrimSpace(strings.TrimPrefix(line, prefix))
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
	return strings.ToLower(addr.Hex()), nil
}
