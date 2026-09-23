package worker

import (
	"errors"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClassifyRPCErrorNeverReturnsNil is the regression guard for the
// 2026-09-04 relayer stall: an unclassified rejection used to reach a bare
// `return nil`, which the dispatcher recorded as IN_NETWORK for thirteen
// transactions that had never been published.
func TestClassifyRPCErrorNeverReturnsNil(t *testing.T) {
	for _, msg := range []string{
		"",
		"something nobody has seen before",
		"reth: AcceptTxResult::SomeNewVariant",
		"error code 3: unmapped pool rejection",
	} {
		verdict, err := classifyRPCError(msg)
		require.Error(t, err, "classify(%q) must never return a nil error", msg)
		assert.Equal(t, VerdictUnknown, verdict)
		assert.ErrorIs(t, err, ErrUnknownRejection)
		assert.Equal(t, store.UNKNOWN_RPC_ERROR, dispatchStatus(err))
	}
}

func TestClassifyRPCErrorRethVocabulary(t *testing.T) {
	tests := []struct {
		name    string
		msg     string
		verdict Verdict
		err     error
		status  string
	}{
		// Verified live against reth on Gnosis: republishing a transaction the
		// pool already held returned exactly this.
		{"already known", "already known", VerdictInPool, ErrAlreadyKnown, store.IN_NETWORK},
		{"internal spelling", "already imported", VerdictInPool, ErrAlreadyKnown, store.IN_NETWORK},

		{"pool full", "txpool is full", VerdictRetrySame, ErrPoolFull, store.POOL_FULL},
		{"discarded", "transaction discarded outright due to pool size constraints", VerdictRetrySame, ErrPoolFull, store.POOL_FULL},

		{"underpriced", "transaction underpriced", VerdictRebuild, ErrGasPriceTooLow, store.LOW_GAS_PRICE},
		{"fee cap below base", "max fee per gas less than block base fee", VerdictRebuild, ErrGasPriceTooLow, store.LOW_GAS_PRICE},
		{"priority below min", "transaction priority fee below minimum 1000000000", VerdictRebuild, ErrGasPriceTooLow, store.LOW_GAS_PRICE},
		{"chain minimum", "transaction feeCap 5 below chain minimum", VerdictRebuild, ErrGasPriceTooLow, store.LOW_GAS_PRICE},

		{"nonce too low", "nonce too low: next nonce 16, tx nonce 3", VerdictResolve, ErrNonceTooLow, store.LOW_NONCE},
		{"nonce too high", "nonce too high", VerdictRetryAfter, ErrNonceTooHigh, store.NONCE_GAP},

		{"insufficient funds", "insufficient funds for gas * price + value: have 10 want 200", VerdictHold, ErrInsufficientGas, store.NO_GAS},
		{"overdraft", "transaction overdraws from account, balance: 1, cost: 2", VerdictHold, ErrInsufficientGas, store.NO_GAS},

		{"intrinsic gas", "intrinsic gas too low", VerdictTerminal, ErrInvalidTx, store.INVALID_TX},
		{"block gas limit", "exceeds block gas limit", VerdictTerminal, ErrInvalidTx, store.INVALID_TX},
		{"oversized", "oversized data: transaction size 200000, limit 131072", VerdictTerminal, ErrInvalidTx, store.INVALID_TX},
		{"not an eoa", "sender is not an EOA", VerdictTerminal, ErrInvalidTx, store.INVALID_TX},
		{"bad chain", "invalid chain ID", VerdictTerminal, ErrInvalidTx, store.INVALID_TX},
		{"unsupported type", "transaction type not supported", VerdictTerminal, ErrInvalidTx, store.INVALID_TX},

		// EIP-7702. These are transient and order-sensitive; classifying any
		// of them as terminal would burn a relayer nonce.
		{"gapped delegated", "gapped-nonce tx from delegated accounts", VerdictRetryAfter, ErrNonceTooHigh, store.NONCE_GAP},
		{"inflight limit", "in-flight transaction limit reached for delegated accounts", VerdictRetrySame, ErrPoolConflict, store.POOL_CONFLICT},
		{"authority reserved", "authority already reserved", VerdictRetrySame, ErrPoolConflict, store.POOL_CONFLICT},
		{"address reserved", "address already reserved", VerdictRetrySame, ErrPoolConflict, store.POOL_CONFLICT},
		{"missing auth list", "no items in authorization list for EIP7702 transaction", VerdictTerminal, ErrInvalidTx, store.INVALID_TX},
		{"invalid auth fields", "EIP-7702 authorization list has invalid fields", VerdictTerminal, ErrInvalidTx, store.INVALID_TX},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verdict, err := classifyRPCError(tt.msg)
			assert.Equal(t, tt.verdict, verdict)
			assert.ErrorIs(t, err, tt.err)
			assert.Equal(t, tt.status, dispatchStatus(err))
		})
	}
}

// TestReplacementBeatsPlainUnderpriced pins the ordering hazard: reth's
// replacement message contains the plain underpriced message as a substring,
// so a naive switch reports the wrong verdict and the wrong status. The
// pre-2026-09-04 classifier had exactly this bug.
func TestReplacementBeatsPlainUnderpriced(t *testing.T) {
	verdict, err := classifyRPCError("replacement transaction underpriced")
	assert.Equal(t, VerdictRebuild, verdict)
	assert.ErrorIs(t, err, ErrReplacementTxUnderpriced)
	assert.Equal(t, store.REPLACEMENT_UNDERPRICED, dispatchStatus(err))
}

// TestIntrinsicGasIsNotAFeeProblem guards the other substring trap: bumping
// the fee on a transaction whose gas limit is too low retries forever.
func TestIntrinsicGasIsNotAFeeProblem(t *testing.T) {
	verdict, err := classifyRPCError("intrinsic gas too low")
	assert.Equal(t, VerdictTerminal, verdict)
	assert.NotErrorIs(t, err, ErrGasPriceTooLow)
}

// TestInsufficientFundsIsNotAFeeProblem — "insufficient funds for gas * price
// + value" mentions gas and price, and must still route to an operator.
func TestInsufficientFundsIsNotAFeeProblem(t *testing.T) {
	verdict, err := classifyRPCError("insufficient funds for gas * price + value: have 1 want 2")
	assert.Equal(t, VerdictHold, verdict)
	assert.NotErrorIs(t, err, ErrGasPriceTooLow)
}

func TestClassifyIsCaseInsensitive(t *testing.T) {
	verdict, err := classifyRPCError("ALREADY KNOWN")
	assert.Equal(t, VerdictInPool, verdict)
	assert.ErrorIs(t, err, ErrAlreadyKnown)
}

func TestRepublishable(t *testing.T) {
	// Safe to resend the identical stored bytes.
	for _, v := range []Verdict{VerdictRetrySame, VerdictRetryAfter, VerdictNetwork, VerdictUnknown} {
		assert.True(t, v.Republishable(), "%s should be republishable", v)
	}
	// Not safe: needs new bytes, an operator, or nothing at all.
	for _, v := range []Verdict{VerdictInPool, VerdictRebuild, VerdictResolve, VerdictHold, VerdictTerminal} {
		assert.False(t, v.Republishable(), "%s should not be republishable", v)
	}
}

// TestHandleNetworkErrorNeverSwallows — a send failure that is not a JSON-RPC
// error object and not recognisably a transport fault must still surface.
//
// The input has to be a string nothing classifies: "connection reset by peer"
// used to land here and is now correctly read as transport.
func TestHandleNetworkErrorNeverSwallows(t *testing.T) {
	err := handleNetworkError(errors.New("something nobody has classified yet"))
	require.Error(t, err)

	var dispatchErr *DispatchError
	require.ErrorAs(t, err, &dispatchErr)
	assert.Equal(t, VerdictUnknown, dispatchErr.Verdict)

	assert.NoError(t, handleNetworkError(nil))
}

func TestHandleNetworkErrorClassifiesTimeouts(t *testing.T) {
	err := handleNetworkError(errors.New("context deadline exceeded: timeout"))
	require.Error(t, err)

	var dispatchErr *DispatchError
	require.ErrorAs(t, err, &dispatchErr)
	assert.Equal(t, VerdictNetwork, dispatchErr.Verdict)
	assert.Equal(t, store.NETWORK_ERROR, dispatchStatus(dispatchErr.Err))
}
