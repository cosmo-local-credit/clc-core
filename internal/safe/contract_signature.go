package safe

import (
	"bytes"
	"context"
	"errors"
	"math/big"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/lmittmann/w3"
)

// Contract signatures (v = 0) let an owner that is itself a contract, such as a
// Calibur passkey account, confirm through EIP-1271. Safe 1.5.0 verifies them
// with isValidSignature(bytes32,bytes); 1.3.0 and 1.4.1 call the legacy
// isValidSignature(bytes,bytes), which Calibur does not implement, so they are
// accepted only from 1.5.0 on.

var (
	isValidSignatureFunc = w3.MustNewFunc("isValidSignature(bytes32,bytes)", "bytes4")
	erc1271MagicValue    = []byte{0x16, 0x26, 0xba, 0x7e}

	errNotContractSignature = errors.New("not a standalone contract signature")
)

type (
	// ContractSignature is one owner's v = 0 signature: the owner and the
	// bytes its isValidSignature receives.
	ContractSignature struct {
		Owner common.Address
		Data  []byte
	}

	// ContractVerifier checks contract signatures against one Safe at one
	// block.
	ContractVerifier interface {
		// Accepts reports whether the Safe verifies contract signatures with
		// isValidSignature(bytes32,bytes), that is, it is Safe 1.5.0 or later.
		Accepts(ctx context.Context) (bool, error)
		// Valid reports whether owner's isValidSignature accepts data for hash.
		Valid(ctx context.Context, owner common.Address, hash common.Hash, data []byte) (bool, error)
	}

	callerVerifier struct {
		safe common.Address
		call Caller
	}
)

// IsContractSignature reports whether sig is marked as a contract signature.
func IsContractSignature(sig []byte) bool {
	return len(sig) >= 65 && sig[64] == 0
}

// ParseContractSignature decodes a contract signature as it is submitted alone
// and as the Transaction Service stores it: r the owner, s the offset 65, v 0,
// then the length-prefixed data.
func ParseContractSignature(sig []byte) (*ContractSignature, error) {
	if len(sig) < 97 || sig[64] != 0 {
		return nil, errNotContractSignature
	}
	if !bytes.Equal(sig[:12], make([]byte, 12)) {
		return nil, errNotContractSignature
	}
	if new(big.Int).SetBytes(sig[32:64]).Cmp(big.NewInt(65)) != 0 {
		return nil, errNotContractSignature
	}
	length := new(big.Int).SetBytes(sig[65:97])
	if length.Sign() == 0 || !length.IsInt64() || length.Int64() > int64(len(sig)-97) {
		return nil, errNotContractSignature
	}
	end := 97 + int(length.Int64())
	if len(sig)-end >= 32 || !bytes.Equal(sig[end:], make([]byte, len(sig)-end)) {
		return nil, errNotContractSignature
	}
	return &ContractSignature{Owner: common.BytesToAddress(sig[12:32]), Data: bytes.Clone(sig[97:end])}, nil
}

// SupportsContractSignatures reports whether a Safe VERSION() verifies
// contract signatures with isValidSignature(bytes32,bytes). An empty or
// unparseable version does not.
func SupportsContractSignatures(version string) bool {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	return major > 1 || (major == 1 && minor >= 5)
}

// NewContractVerifier verifies through call, which fixes the block.
func NewContractVerifier(safe common.Address, call Caller) ContractVerifier {
	return callerVerifier{safe: safe, call: call}
}

func (v callerVerifier) Accepts(ctx context.Context) (bool, error) {
	data, err := v.call(ctx, v.safe, versionFunc.Selector[:])
	if UnsupportedCall(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var version string
	if versionFunc.DecodeReturns(data, &version) != nil {
		return false, nil
	}
	return SupportsContractSignatures(version), nil
}

func (v callerVerifier) Valid(ctx context.Context, owner common.Address, hash common.Hash, data []byte) (bool, error) {
	input, err := isValidSignatureFunc.EncodeArgs(hash, data)
	if err != nil {
		return false, err
	}
	out, err := v.call(ctx, owner, input)
	if UnsupportedCall(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// The magic value is a left-aligned bytes4 in a 32-byte word. An owner
	// without code answers with no data at all.
	return len(out) == 32 && bytes.Equal(out[:4], erc1271MagicValue) &&
		bytes.Equal(out[4:], make([]byte, 28)), nil
}
