package worker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/cosmo-local-credit/clc-core/internal/store"
)

// Verdict is what the dispatcher should do about a transaction the node would
// not accept. Every rejection gets one — an unclassified rejection is
// VerdictUnknown, never a silent success.
type Verdict int

const (
	// VerdictInPool — the pool already holds these exact bytes. Nothing to do
	// but wait for a receipt.
	VerdictInPool Verdict = iota
	// VerdictRetrySame — transient. Resend the identical bytes later; the nonce
	// is still valid and the signature still covers the right payload.
	VerdictRetrySame
	// VerdictRetryAfter — an earlier nonce from this signer must land first.
	VerdictRetryAfter
	// VerdictRebuild — re-sign the same nonce at a higher fee.
	VerdictRebuild
	// VerdictResolve — the nonce is already consumed. Look up what consumed it
	// before deciding.
	VerdictResolve
	// VerdictHold — needs an operator: the relayer cannot pay for this.
	VerdictHold
	// VerdictTerminal — our bug. These bytes will never be accepted; resending
	// them only burns the nonce for longer.
	VerdictTerminal
	// VerdictNetwork — the RPC endpoint itself failed. Says nothing about the
	// transaction.
	VerdictNetwork
	// VerdictUnknown — a rejection we do not recognise. Logged verbatim so the
	// string can be added to the table below, and retried under a cap.
	VerdictUnknown
)

func (v Verdict) String() string {
	switch v {
	case VerdictInPool:
		return "IN_POOL"
	case VerdictRetrySame:
		return "RETRY_SAME"
	case VerdictRetryAfter:
		return "RETRY_AFTER"
	case VerdictRebuild:
		return "REBUILD"
	case VerdictResolve:
		return "RESOLVE"
	case VerdictHold:
		return "HOLD"
	case VerdictTerminal:
		return "TERMINAL"
	case VerdictNetwork:
		return "NETWORK"
	default:
		return "UNKNOWN"
	}
}

// Republishable reports whether the reconciler may resend the stored raw
// transaction as-is. Rebuild is excluded: it needs new bytes at a higher fee.
func (v Verdict) Republishable() bool {
	return v == VerdictRetrySame || v == VerdictRetryAfter || v == VerdictNetwork || v == VerdictUnknown
}

var (
	ErrAlreadyKnown             = errors.New("clc-core: transaction already in pool")
	ErrPoolFull                 = errors.New("clc-core: txpool full")
	ErrPoolConflict             = errors.New("clc-core: pool reservation conflict")
	ErrInsufficientGas          = errors.New("clc-core: insufficient funds")
	ErrGasPriceTooLow           = errors.New("clc-core: gas price too low")
	ErrNonceTooLow              = errors.New("clc-core: nonce too low")
	ErrNonceTooHigh             = errors.New("clc-core: nonce too high")
	ErrReplacementTxUnderpriced = errors.New("clc-core: replacement tx underpriced")
	ErrInvalidTx                = errors.New("clc-core: transaction rejected as invalid")
	ErrUnknownRejection         = errors.New("clc-core: unrecognised rpc rejection")
	ErrNetwork                  = errors.New("clc-core: network related error")
)

type (
	DispatchError struct {
		Err         error
		OriginalErr error
		Verdict     Verdict
	}
)

func (e *DispatchError) Error() string {
	return fmt.Sprintf("%v [verdict=%s] (original rpc error: %v)", e.Err, e.Verdict, e.OriginalErr)
}

func (e *DispatchError) Unwrap() error {
	return e.Err
}

// classifyRPCError maps an eth_sendRawTransaction rejection to a verdict.
//
// The strings are reth's *RPC* vocabulary, not its internal one. reth's pool
// produces PoolErrorKind strings ("already imported") but the RPC layer remaps
// them to geth-compatible strings in RpcPoolError ("already known") before they
// reach a client, so matching the internal names never fires. Verified against
// reth on Gnosis: eth_sendRawTransaction returns `error code 3: already known`.
//
// Several strings interpolate values, so every match is a substring test, and
// order matters where one string contains another — "replacement transaction
// underpriced" contains "transaction underpriced", and "intrinsic gas too low"
// must not be read as a fee problem.
//
// A rejection that matches nothing returns (VerdictUnknown, ErrUnknownRejection).
// It must never return a nil error.
func classifyRPCError(errMsg string) (Verdict, error) {
	msg := strings.ToLower(errMsg)

	contains := func(needles ...string) bool {
		for _, n := range needles {
			if strings.Contains(msg, n) {
				return true
			}
		}
		return false
	}

	switch {
	// -- Already held ------------------------------------------------------
	// RpcPoolError::AlreadyKnown. "already imported" is the internal
	// PoolErrorKind spelling, matched too in case a node surfaces it raw.
	case contains("already known", "already imported"):
		return VerdictInPool, ErrAlreadyKnown

	// -- Pool capacity -----------------------------------------------------
	// RpcPoolError::TxPoolOverflow (SpammerExceededCapacity, DiscardedOnInsert).
	case contains("txpool is full", "transaction discarded outright", "identified as a spammer"):
		return VerdictRetrySame, ErrPoolFull

	// -- EIP-7702 ordering and reservations --------------------------------
	// All order-sensitive and transient. Misclassifying any of these as
	// terminal burns the nonce: RELAY_DELEGATE / RELAY_DELEGATE_EXEC are
	// type-4, so this block is the one that fires in practice.
	case contains("gapped-nonce tx from delegated accounts"):
		return VerdictRetryAfter, ErrNonceTooHigh
	case contains(
		"in-flight transaction limit reached for delegated accounts",
		"authority already reserved",
		"address already reserved",
		"conflicts with existing transaction",
	):
		return VerdictRetrySame, ErrPoolConflict
	case contains(
		"no items in authorization list",
		"authorization list has invalid fields",
		"authorization list not supported",
	):
		return VerdictTerminal, ErrInvalidTx

	// -- Nonce -------------------------------------------------------------
	// Checked before the fee rules: "nonce too low" carries interpolated
	// values ("next nonce {state}, tx nonce {tx}") and must not be shadowed.
	case contains("nonce too low"):
		return VerdictResolve, ErrNonceTooLow
	case contains("nonce too high", "nonce is not consistent"):
		return VerdictRetryAfter, ErrNonceTooHigh
	case contains("nonce has max value"):
		return VerdictTerminal, ErrInvalidTx

	// -- Funds -------------------------------------------------------------
	// Before the gas rules: "insufficient funds for gas * price + value"
	// would otherwise be read as a fee problem and bumped forever.
	case contains(
		"insufficient funds",
		"transaction overdraws from account",
		"lack of funds",
	):
		return VerdictHold, ErrInsufficientGas

	// -- Fees --------------------------------------------------------------
	// Replacement first: its string contains "transaction underpriced".
	case contains("replacement transaction underpriced", "insufficient gas price to replace"):
		return VerdictRebuild, ErrReplacementTxUnderpriced
	case contains(
		"transaction underpriced",
		"max fee per gas less than block base fee",
		"priority fee below minimum",
		"below chain minimum",
	):
		return VerdictRebuild, ErrGasPriceTooLow
	case contains("max priority fee per gas higher than max fee per gas"):
		return VerdictTerminal, ErrInvalidTx

	// -- Our bugs ----------------------------------------------------------
	case contains(
		"intrinsic gas too low",
		"intrinsic gas too high",
		"exceeds block gas limit",
		"exceeds block's gas limit",
		"exceeds max transaction gas limit",
		"gas limit too high",
		"gas uint64 overflow",
		"oversized data",
		"max initcode size exceeded",
		"negative value",
		"sender is not an eoa",
		"invalid sender",
		"invalid chain id",
		"transaction type not supported",
	):
		return VerdictTerminal, ErrInvalidTx

	default:
		return VerdictUnknown, ErrUnknownRejection
	}
}

// dispatchStatus maps a classified rejection to a dispatch status value. Every
// value returned here must exist in dispatch_status_type (migrations 001 and
// 026).
func dispatchStatus(err error) string {
	switch {
	case errors.Is(err, ErrAlreadyKnown):
		return store.IN_NETWORK
	case errors.Is(err, ErrPoolFull):
		return store.POOL_FULL
	case errors.Is(err, ErrPoolConflict):
		return store.POOL_CONFLICT
	case errors.Is(err, ErrNonceTooHigh):
		return store.NONCE_GAP
	case errors.Is(err, ErrNonceTooLow):
		return store.LOW_NONCE
	case errors.Is(err, ErrInsufficientGas):
		return store.NO_GAS
	case errors.Is(err, ErrGasPriceTooLow):
		return store.LOW_GAS_PRICE
	case errors.Is(err, ErrReplacementTxUnderpriced):
		return store.REPLACEMENT_UNDERPRICED
	case errors.Is(err, ErrInvalidTx):
		return store.INVALID_TX
	case errors.Is(err, ErrNetwork):
		return store.NETWORK_ERROR
	default:
		return store.UNKNOWN_RPC_ERROR
	}
}

func handleNetworkError(err error) error {
	if err == nil {
		return nil
	}

	var netErr net.Error
	var urlErr *url.Error

	if errors.As(err, &netErr) || errors.As(err, &urlErr) ||
		strings.Contains(err.Error(), "timeout") || errors.Is(err, context.DeadlineExceeded) {
		return &DispatchError{
			Err:         ErrNetwork,
			OriginalErr: err,
			Verdict:     VerdictNetwork,
		}
	}

	// Not recognisably a transport failure, but the send did fail. Never let
	// it pass as a success — that is how nonces get burned silently.
	return &DispatchError{
		Err:         ErrUnknownRejection,
		OriginalErr: err,
		Verdict:     VerdictUnknown,
	}
}
