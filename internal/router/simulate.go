package router

import (
	"bytes"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

var (
	// knownErrors names the custom errors a routed batch can revert with: Calibur
	// wrapping a failed call, SwapPool, the solady ERC-20 GiftableToken builds on,
	// and the periphery. SwapPool and ERC-20 share InsufficientBalance(); the hop
	// that reverted tells them apart.
	knownErrors = func() map[[4]byte]string {
		names := []string{
			// Calibur
			"CallFailed(bytes)", "InvalidNonce()", "InvalidSignature()", "SignatureExpired()",
			"KeyDoesNotExist()", "KeyExpired(uint40)", "Unauthorized()",
			// SwapPool
			"Sealed()", "InvalidState()", "TokenCallFailed()", "TransferFailed()", "QuoterCallFailed()",
			"InsufficientBalance()", "UnauthorizedToken()", "RegistryCallFailed()", "LimitExceeded()",
			"LimiterCallFailed()", "InvalidRecipient()", "InvalidToken()", "Expired()",
			"InsufficientOutput()", "FeeTooHigh()", "Reentrancy()",
			// ERC-20 (solady) and GiftableToken
			"InsufficientAllowance()", "TotalSupplyOverflow()", "TokenExpired()",
		}
		out := make(map[[4]byte]string, len(names))
		for _, name := range names {
			var sel [4]byte
			copy(sel[:], selector(name))
			out[sel] = name[:bytes.IndexByte([]byte(name), '(')]
		}
		return out
	}()

	callFailedSelector = selector("CallFailed(bytes)")
	errorStringSel     = selector("Error(string)")
	panicSel           = selector("Panic(uint256)")
	bytesArgs          = abi.Arguments{{Type: mustType("bytes")}}
	stringArgs         = abi.Arguments{{Type: mustType("string")}}
	uintArgs           = abi.Arguments{{Type: mustType("uint256")}}
)

func mustType(t string) abi.Type {
	typ, err := abi.NewType(t, "", nil)
	if err != nil {
		panic(err)
	}
	return typ
}

type (
	// DecodedRevert is a simulation failure in readable form.
	DecodedRevert struct {
		// Reason is the innermost error name, or "unknown".
		Reason string `json:"reason"`
		// Message is set for Error(string) and Panic(uint256).
		Message string `json:"message,omitempty"`
		// Data is the innermost raw revert payload.
		Data string `json:"data"`
	}
)

// DecodeRevert unwraps Calibur's CallFailed(bytes) and names the inner error.
func DecodeRevert(data []byte) DecodedRevert {
	for range 4 {
		if len(data) < 4 || !bytes.Equal(data[:4], callFailedSelector) {
			break
		}
		values, err := bytesArgs.Unpack(data[4:])
		if err != nil || len(values) != 1 {
			break
		}
		inner, ok := values[0].([]byte)
		if !ok {
			break
		}
		data = inner
	}
	out := DecodedRevert{Reason: "unknown", Data: hexutil.Encode(data)}
	if len(data) < 4 {
		if len(data) == 0 {
			out.Reason = "empty"
		}
		return out
	}
	switch {
	case bytes.Equal(data[:4], errorStringSel):
		if values, err := stringArgs.Unpack(data[4:]); err == nil && len(values) == 1 {
			out.Reason = "Error"
			out.Message, _ = values[0].(string)
		}
	case bytes.Equal(data[:4], panicSel):
		if values, err := uintArgs.Unpack(data[4:]); err == nil && len(values) == 1 {
			out.Reason = "Panic"
			if code, ok := values[0].(*big.Int); ok {
				out.Message = "0x" + code.Text(16)
			}
		}
	default:
		var sel [4]byte
		copy(sel[:], data[:4])
		if name, ok := knownErrors[sel]; ok {
			out.Reason = name
		}
	}
	return out
}
