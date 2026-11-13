package router

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	gocalibur "github.com/grassrootseconomics/go-calibur"
)

var (
	selectorApprove         = selector("approve(address,uint256)")
	selectorWithdraw        = selector("withdraw(address,address,uint256)")
	selectorWithdrawTo      = selector("withdraw(address,address,uint256,address)")
	selectorWithdrawBounded = selector("withdraw(address,address,uint256,address,uint256,uint256)")

	// ErrInvalidRoute wraps every reason ValidateRoute refuses a route-shaped
	// batch.
	ErrInvalidRoute = errors.New("invalid swap route")
)

func selector(signature string) []byte { return crypto.Keccak256([]byte(signature))[:4] }

type (
	// Call is one Calibur call.
	Call struct {
		To    common.Address
		Value *big.Int
		Data  []byte
	}

	// PlannedHop is one hop of a batch to build.
	PlannedHop struct {
		Pool      common.Address
		TokenIn   common.Address
		TokenOut  common.Address
		AmountIn  *big.Int
		MinOut    *big.Int
		Recipient common.Address
	}

	// DecodedHop is one hop recovered from calldata.
	DecodedHop struct {
		Pool     common.Address
		TokenIn  common.Address
		TokenOut common.Address
		// Amount is the withdrawn (spent) input.
		Amount *big.Int
		// Reset and Approved are the two approvals' tokens, spenders and
		// amounts.
		ResetToken, ApprovedToken     common.Address
		ResetSpender, ApprovedSpender common.Address
		ResetAmount, ApprovedAmount   *big.Int
		// Recipient is the zero address when the overload pays msg.sender.
		Recipient common.Address
		Bounded   bool
		MinOut    *big.Int
		Deadline  *big.Int
	}

	// SignedBatch is everything the wallet signs.
	SignedBatch struct {
		Calls           []Call
		RevertOnFailure bool
		Nonce           *big.Int
		KeyHash         common.Hash
		Executor        common.Address
		Deadline        uint64
	}
)

func word(v *big.Int) []byte { return common.LeftPadBytes(v.Bytes(), 32) }

func addressWord(a common.Address) []byte { return common.LeftPadBytes(a.Bytes(), 32) }

func encodeApprove(spender common.Address, amount *big.Int) []byte {
	data := slicesConcat(selectorApprove, addressWord(spender), word(amount))
	return data
}

func encodeBoundedWithdraw(out, in common.Address, value *big.Int, recipient common.Address, minOut, deadline *big.Int) []byte {
	return slicesConcat(selectorWithdrawBounded,
		addressWord(out), addressWord(in), word(value), addressWord(recipient), word(minOut), word(deadline))
}

func slicesConcat(parts ...[]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// BuildRouteCalls lays out three calls per hop, the shape the app and the
// relay already use:
//
//	tokenIn.approve(pool, 0)
//	tokenIn.approve(pool, amountIn)
//	pool.withdraw(tokenOut, tokenIn, amountIn, recipient, minOut, deadline)
//
// Hop i's minOut is hop i+1's input, so the next hop never dips into bridge
// vouchers the wallet already held.
func BuildRouteCalls(hops []PlannedHop, deadline uint64) []Call {
	calls := make([]Call, 0, 3*len(hops))
	dl := new(big.Int).SetUint64(deadline)
	for _, hop := range hops {
		calls = append(calls,
			Call{To: hop.TokenIn, Value: new(big.Int), Data: encodeApprove(hop.Pool, new(big.Int))},
			Call{To: hop.TokenIn, Value: new(big.Int), Data: encodeApprove(hop.Pool, hop.AmountIn)},
			Call{To: hop.Pool, Value: new(big.Int), Data: encodeBoundedWithdraw(
				hop.TokenOut, hop.TokenIn, hop.AmountIn, hop.Recipient, hop.MinOut, dl)},
		)
	}
	return calls
}

// RecipientOr returns the hop's recipient, or sender for the overload that
// pays msg.sender.
func (h DecodedHop) RecipientOr(sender common.Address) common.Address {
	if h.Recipient == (common.Address{}) {
		return sender
	}
	return h.Recipient
}

func calldataWord(data []byte, index int) ([]byte, bool) {
	start := 4 + index*32
	if start+32 > len(data) {
		return nil, false
	}
	return data[start : start+32], true
}

func calldataAddr(data []byte, index int) (common.Address, bool) {
	w, ok := calldataWord(data, index)
	if !ok {
		return common.Address{}, false
	}
	for _, b := range w[:12] {
		if b != 0 {
			return common.Address{}, false
		}
	}
	return common.BytesToAddress(w[12:]), true
}

func calldataInt(data []byte, index int) *big.Int {
	w, ok := calldataWord(data, index)
	if !ok {
		return new(big.Int)
	}
	return new(big.Int).SetBytes(w)
}

func decodeApproveCall(call Call) (token, spender common.Address, amount *big.Int, ok bool) {
	if call.Value != nil && call.Value.Sign() != 0 {
		return token, spender, nil, false
	}
	if len(call.Data) != 68 || !bytes.Equal(call.Data[:4], selectorApprove) {
		return token, spender, nil, false
	}
	spender, ok = calldataAddr(call.Data, 0)
	if !ok {
		return token, spender, nil, false
	}
	return call.To, spender, calldataInt(call.Data, 1), true
}

func isWithdraw(data []byte) bool {
	if len(data) < 4 {
		return false
	}
	switch {
	case len(data) == 100 && bytes.Equal(data[:4], selectorWithdraw),
		len(data) == 132 && bytes.Equal(data[:4], selectorWithdrawTo),
		len(data) == 196 && bytes.Equal(data[:4], selectorWithdrawBounded):
		return true
	}
	return false
}

func decodeWithdrawCall(call Call, hop *DecodedHop) bool {
	if call.Value != nil && call.Value.Sign() != 0 || !isWithdraw(call.Data) {
		return false
	}
	var ok bool
	if hop.TokenOut, ok = calldataAddr(call.Data, 0); !ok {
		return false
	}
	if hop.TokenIn, ok = calldataAddr(call.Data, 1); !ok {
		return false
	}
	hop.Pool = call.To
	hop.Amount = calldataInt(call.Data, 2)
	if len(call.Data) >= 132 {
		if hop.Recipient, ok = calldataAddr(call.Data, 3); !ok {
			return false
		}
	}
	if len(call.Data) == 196 {
		hop.Bounded = true
		hop.MinOut = calldataInt(call.Data, 4)
		hop.Deadline = calldataInt(call.Data, 5)
	}
	return true
}

// DecodeRouteShape recognises a batch shaped like a swap route: groups of
// approve, approve, withdraw (any of the three trader overloads). It checks
// shape only; ValidateRoute checks the values. Any other batch returns false
// and relays as it always has.
func DecodeRouteShape(calls []Call) ([]DecodedHop, bool) {
	if len(calls) == 0 || len(calls)%3 != 0 {
		return nil, false
	}
	hops := make([]DecodedHop, 0, len(calls)/3)
	for i := 0; i < len(calls); i += 3 {
		var hop DecodedHop
		var ok bool
		hop.ResetToken, hop.ResetSpender, hop.ResetAmount, ok = decodeApproveCall(calls[i])
		if !ok {
			return nil, false
		}
		hop.ApprovedToken, hop.ApprovedSpender, hop.ApprovedAmount, ok = decodeApproveCall(calls[i+1])
		if !ok {
			return nil, false
		}
		if !decodeWithdrawCall(calls[i+2], &hop) {
			return nil, false
		}
		hops = append(hops, hop)
	}
	return hops, true
}

func invalid(hop int, format string, args ...any) error {
	return fmt.Errorf("%w: hop %d: %s", ErrInvalidRoute, hop, fmt.Sprintf(format, args...))
}

// CheckHopValues checks what every hop must satisfy whoever sent it: it
// resets then approves exactly the withdrawn amount of its input to its pool,
// and swaps two distinct vouchers.
func CheckHopValues(hops []DecodedHop) error {
	zero := common.Address{}
	for i, hop := range hops {
		switch {
		case hop.Pool == zero || hop.TokenIn == zero || hop.TokenOut == zero:
			return invalid(i, "zero address")
		case hop.TokenIn == hop.TokenOut:
			return invalid(i, "input and output are the same voucher")
		case hop.ResetToken != hop.TokenIn || hop.ApprovedToken != hop.TokenIn:
			return invalid(i, "approval is not for the input voucher")
		case hop.ResetSpender != hop.Pool || hop.ApprovedSpender != hop.Pool:
			return invalid(i, "approval spender is not the pool")
		case hop.ResetAmount.Sign() != 0:
			return invalid(i, "first approval must reset to zero")
		case hop.Amount.Sign() <= 0:
			return invalid(i, "zero input")
		case hop.ApprovedAmount.Cmp(hop.Amount) != 0:
			return invalid(i, "approval does not equal the withdrawn amount")
		case hop.Bounded && (hop.Recipient == zero):
			return invalid(i, "zero recipient")
		}
	}
	return nil
}

// ValidateRoute checks a route-shaped batch: CheckHopValues, intermediate
// output paid back to the sender, and every bounded deadline no further than
// horizonSecs past now.
func ValidateRoute(hops []DecodedHop, sender common.Address, now uint64, horizonSecs uint64) error {
	if err := CheckHopValues(hops); err != nil {
		return err
	}
	limit := new(big.Int).SetUint64(now + horizonSecs)
	for i, hop := range hops {
		if i+1 < len(hops) && hop.RecipientOr(sender) != sender {
			return invalid(i, "intermediate output must return to the sender")
		}
		if hop.Bounded && hop.Deadline.Cmp(limit) > 0 {
			return invalid(i, "deadline is beyond the allowed horizon")
		}
	}
	return nil
}

// Chained reports whether each hop spends the voucher the previous one
// delivered: a route, rather than unrelated swaps sharing a batch.
func Chained(hops []DecodedHop) bool {
	for i := 1; i < len(hops); i++ {
		if hops[i-1].TokenOut != hops[i].TokenIn {
			return false
		}
	}
	return len(hops) > 0
}

// CaliburNonce packs a 192-bit key and a 64-bit sequence into the uint256
// Calibur uses as a per-key nonce: (low 192 bits of keyHash << 64) | seq, as
// the relayer and the app's buildCaliburNonce do.
func CaliburNonce(keyHash common.Hash, seq uint64) *big.Int {
	key := new(big.Int).SetBytes(keyHash[:])
	mask := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 192), big.NewInt(1))
	key.And(key, mask)
	key.Lsh(key, 64)
	return key.Or(key, new(big.Int).SetUint64(seq))
}

func (b SignedBatch) calibur() gocalibur.SignedBatchedCall {
	calls := make([]gocalibur.Call, len(b.Calls))
	for i, c := range b.Calls {
		calls[i] = gocalibur.Call{To: c.To, Value: c.Value, Data: c.Data}
	}
	return gocalibur.SignedBatchedCall{
		BatchedCall: gocalibur.BatchedCall{Calls: calls, RevertOnFailure: b.RevertOnFailure},
		Nonce:       b.Nonce,
		KeyHash:     b.KeyHash,
		Executor:    b.Executor,
		Deadline:    new(big.Int).SetUint64(b.Deadline),
	}
}

// Digest is the EIP-712 digest the wallet signs, as /relayer/relay/preview
// computes it.
func (b SignedBatch) Digest(chainID *big.Int, wallet, calibur common.Address) common.Hash {
	domain := gocalibur.CaliburDomain(chainID, wallet, calibur)
	return common.Hash(gocalibur.ComputeDigest(domain, b.calibur()))
}

// ExecuteCalldata is Calibur's execute(BatchedCall): what the wallet runs when
// it calls itself, which is how the batch is simulated before signing.
func (b SignedBatch) ExecuteCalldata() ([]byte, error) {
	return gocalibur.EncodeBatchCalldata(b.calibur().BatchedCall)
}
