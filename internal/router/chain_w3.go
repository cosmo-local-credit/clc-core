package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/rpc"
	gocalibur "github.com/grassrootseconomics/go-calibur"
	"github.com/holiman/uint256"
	"github.com/lmittmann/w3"
	"github.com/lmittmann/w3/w3types"
)

// Contract functions the router reads.
var (
	fnTokenRegistry         = w3.MustNewFunc("tokenRegistry()", "address")
	fnTokenLimiter          = w3.MustNewFunc("tokenLimiter()", "address")
	fnQuoter                = w3.MustNewFunc("quoter()", "address")
	fnFeePolicy             = w3.MustNewFunc("feePolicy()", "address")
	fnFeesDecoupled         = w3.MustNewFunc("feesDecoupled()", "bool")
	fnProtocolFeeController = w3.MustNewFunc("protocolFeeController()", "address")
	fnSealState             = w3.MustNewFunc("sealState()", "uint8")
	fnFeeAddress            = w3.MustNewFunc("feeAddress()", "address")
	fnDefaultFee            = w3.MustNewFunc("defaultFee()", "uint256")
	fnGetProtocolFee        = w3.MustNewFunc("getProtocolFee()", "uint256")
	fnGetProtocolRecipient  = w3.MustNewFunc("getProtocolFeeRecipient()", "address")
	fnEntryCount            = w3.MustNewFunc("entryCount()", "uint256")
	fnEntry                 = w3.MustNewFunc("entry(uint256)", "address")
	fnHave                  = w3.MustNewFunc("have(address)", "bool")
	fnLimitOf               = w3.MustNewFunc("limitOf(address,address)", "uint256")
	fnPriceIndex            = w3.MustNewFunc("priceIndex(address)", "uint256")
	fnBalanceOf             = w3.MustNewFunc("balanceOf(address)", "uint256")
	fnFees                  = w3.MustNewFunc("fees(address)", "uint256")
	fnGetFee                = w3.MustNewFunc("getFee(address,address,uint256)", "uint256")
	fnGetQuote              = w3.MustNewFunc("getQuote(address,address,uint256)", "uint256")
	fnGetAmountOut          = w3.MustNewFunc("getAmountOut(address,address,uint256)", "uint256")
	fnGetAmountIn           = w3.MustNewFunc("getAmountIn(address,address,uint256)", "uint256")
	fnGetSeq                = w3.MustNewFunc("getSeq(uint256)", "uint256")
)

type (
	// W3Reader implements ChainReader over JSON-RPC batches.
	W3Reader struct {
		client    *w3.Client
		batchSize int
	}

	// rawCall is one JSON-RPC request whose error is kept on the call instead of
	// failing the batch.
	rawCall struct {
		method string
		args   []any
		result any
		err    error
	}

	// ethCall is one eth_call whose decoded result lands in out.
	ethCall struct {
		to      common.Address
		raw     *rawCall
		ret     hexutil.Bytes
		fn      interface{ DecodeReturns([]byte, ...any) error }
		returns []any
	}
)

// NewW3Reader wraps a client. batchSize bounds the calls per JSON-RPC batch.
func NewW3Reader(client *w3.Client, batchSize int) *W3Reader {
	if batchSize <= 0 {
		batchSize = 150
	}
	return &W3Reader{client: client, batchSize: batchSize}
}

func (r *rawCall) CreateRequest() (rpc.BatchElem, error) {
	return rpc.BatchElem{Method: r.method, Args: r.args, Result: r.result}, nil
}

func (r *rawCall) HandleResponse(elem rpc.BatchElem) error {
	r.err = elem.Error
	return nil
}

func blockArg(block uint64) any {
	if block == 0 {
		return "latest"
	}
	return hexutil.EncodeUint64(block)
}

// do sends the calls in chunks; a transport failure fails the whole read,
// while a failed call keeps its error on the call.
func (r *W3Reader) do(ctx context.Context, calls []*rawCall) error {
	for start := 0; start < len(calls); start += r.batchSize {
		end := min(start+r.batchSize, len(calls))
		chunk := make([]w3types.RPCCaller, 0, end-start)
		for _, c := range calls[start:end] {
			chunk = append(chunk, c)
		}
		if err := r.client.CallCtx(ctx, chunk...); err != nil {
			var callErrs w3.CallErrors
			if !errors.As(err, &callErrs) {
				return err
			}
		}
	}
	return nil
}

func (r *W3Reader) newCall(block uint64, to common.Address, fn interface {
	EncodeArgs(...any) ([]byte, error)
	DecodeReturns([]byte, ...any) error
}, args []any, returns ...any) (*ethCall, error) {
	input, err := fn.EncodeArgs(args...)
	if err != nil {
		return nil, err
	}
	c := &ethCall{to: to, fn: fn, returns: returns}
	c.raw = &rawCall{
		method: "eth_call",
		args:   []any{map[string]any{"to": to, "data": hexutil.Bytes(input)}, blockArg(block)},
		result: &c.ret,
	}
	return c, nil
}

func (c *ethCall) decode() error {
	if c.raw.err != nil {
		return c.raw.err
	}
	if len(c.ret) == 0 {
		return errors.New("empty return data")
	}
	return c.fn.DecodeReturns(c.ret, c.returns...)
}

func (r *W3Reader) run(ctx context.Context, calls []*ethCall) error {
	raws := make([]*rawCall, len(calls))
	for i, c := range calls {
		raws[i] = c.raw
	}
	return r.do(ctx, raws)
}

func mustCall(c *ethCall, err error) *ethCall {
	if err != nil {
		panic(err) // every ABI here is static; an encoding error is a bug
	}
	return c
}

// Head reads the latest block.
func (r *W3Reader) Head(ctx context.Context) (Head, error) {
	// The node's own hash is used rather than one recomputed from a decoded
	// header, which would depend on every field the chain puts in it.
	var header struct {
		Number    hexutil.Uint64 `json:"number"`
		Hash      common.Hash    `json:"hash"`
		Timestamp hexutil.Uint64 `json:"timestamp"`
	}
	call := &rawCall{method: "eth_getBlockByNumber", args: []any{"latest", false}, result: &header}
	if err := r.do(ctx, []*rawCall{call}); err != nil {
		return Head{}, err
	}
	if call.err != nil {
		return Head{}, call.err
	}
	return Head{Number: uint64(header.Number), Hash: header.Hash, Time: uint64(header.Timestamp)}, nil
}

// PoolParams reads the eight pool getters, then each fee policy's default fee
// and each protocol fee controller's fee and recipient.
func (r *W3Reader) PoolParams(ctx context.Context, block uint64, pools []common.Address) ([]PoolParams, error) {
	out := make([]PoolParams, len(pools))
	calls := make([]*ethCall, 0, 8*len(pools))
	for i, pool := range pools {
		p := &out[i]
		p.Pool = pool
		calls = append(calls,
			mustCall(r.newCall(block, pool, fnTokenRegistry, nil, &p.Registry)),
			mustCall(r.newCall(block, pool, fnTokenLimiter, nil, &p.Limiter)),
			mustCall(r.newCall(block, pool, fnQuoter, nil, &p.Quoter)),
			mustCall(r.newCall(block, pool, fnFeePolicy, nil, &p.FeePolicy)),
			mustCall(r.newCall(block, pool, fnFeesDecoupled, nil, &p.FeesDecoupled)),
			mustCall(r.newCall(block, pool, fnProtocolFeeController, nil, &p.Controller)),
			mustCall(r.newCall(block, pool, fnSealState, nil, &p.SealState)),
			mustCall(r.newCall(block, pool, fnFeeAddress, nil, &p.FeeAddress)),
		)
	}
	if err := r.run(ctx, calls); err != nil {
		return nil, err
	}
	for i := range out {
		for _, c := range calls[8*i : 8*i+8] {
			if err := c.decode(); err != nil {
				out[i].Err = err
				break
			}
		}
	}

	policies := map[common.Address]*big.Int{}
	type controllerState struct {
		fee       *big.Int
		recipient common.Address
	}
	controllers := map[common.Address]*controllerState{}
	var second []*ethCall
	for i := range out {
		if out[i].Err != nil {
			continue
		}
		if fp := out[i].FeePolicy; fp != (common.Address{}) {
			if _, ok := policies[fp]; !ok {
				policies[fp] = new(big.Int)
				second = append(second, mustCall(r.newCall(block, fp, fnDefaultFee, nil, policies[fp])))
			}
		}
		if ctrl := out[i].Controller; ctrl != (common.Address{}) {
			if _, ok := controllers[ctrl]; !ok {
				state := &controllerState{fee: new(big.Int)}
				controllers[ctrl] = state
				second = append(second,
					mustCall(r.newCall(block, ctrl, fnGetProtocolFee, nil, state.fee)),
					mustCall(r.newCall(block, ctrl, fnGetProtocolRecipient, nil, &state.recipient)))
			}
		}
	}
	if err := r.run(ctx, second); err != nil {
		return nil, err
	}
	failed := map[common.Address]error{}
	for _, c := range second {
		if err := c.decode(); err != nil {
			failed[c.to] = err
		}
	}
	for i := range out {
		p := &out[i]
		if p.Err != nil {
			continue
		}
		if p.FeePolicy != (common.Address{}) {
			if err, ok := failed[p.FeePolicy]; ok {
				p.Err = fmt.Errorf("fee policy: %w", err)
				continue
			}
			fee := policies[p.FeePolicy]
			if !fee.IsUint64() || fee.Uint64() > PPM {
				p.Err = errors.New("fee policy default fee out of range")
				continue
			}
			p.DefaultFeePPM = fee.Uint64()
		}
		if p.Controller != (common.Address{}) {
			if err, ok := failed[p.Controller]; ok {
				p.Err = fmt.Errorf("protocol fee controller: %w", err)
				continue
			}
			state := controllers[p.Controller]
			if !state.fee.IsUint64() || state.fee.Uint64() > PPM {
				p.Err = errors.New("protocol fee out of range")
				continue
			}
			p.ProtocolRecipient = state.recipient
			if state.recipient != (common.Address{}) {
				p.ProtocolFeePPM = state.fee.Uint64()
			}
		}
	}
	return out, nil
}

// PoolStructure enumerates each registry, then reads membership, limit and
// price index for every member and every known token.
func (r *W3Reader) PoolStructure(ctx context.Context, block uint64, probes []StructureProbe) ([]PoolStructure, error) {
	out := make([]PoolStructure, len(probes))
	counts := make([]*big.Int, len(probes))
	var countCalls []*ethCall
	countIndex := map[int]int{}
	for i, probe := range probes {
		out[i].Pool = probe.Pool
		if probe.SkipEntries || probe.Registry == (common.Address{}) {
			continue
		}
		counts[i] = new(big.Int)
		countIndex[i] = len(countCalls)
		countCalls = append(countCalls, mustCall(r.newCall(block, probe.Registry, fnEntryCount, nil, counts[i])))
	}
	if err := r.run(ctx, countCalls); err != nil {
		return nil, err
	}

	entries := make([][]common.Address, len(probes))
	var entryCalls []*ethCall
	type entryRef struct{ probe, slot int }
	var refs []entryRef
	for i, probe := range probes {
		ci, ok := countIndex[i]
		if !ok {
			continue
		}
		if err := countCalls[ci].decode(); err != nil {
			out[i].Err = fmt.Errorf("entryCount: %w", err)
			continue
		}
		if !counts[i].IsInt64() || counts[i].Int64() > 100_000 {
			out[i].Err = errors.New("entryCount out of range")
			continue
		}
		n := int(counts[i].Int64())
		entries[i] = make([]common.Address, n)
		for slot := range n {
			entryCalls = append(entryCalls, mustCall(r.newCall(block, probe.Registry, fnEntry,
				[]any{big.NewInt(int64(slot))}, &entries[i][slot])))
			refs = append(refs, entryRef{i, slot})
		}
	}
	if err := r.run(ctx, entryCalls); err != nil {
		return nil, err
	}
	for k, c := range entryCalls {
		if err := c.decode(); err != nil && out[refs[k].probe].Err == nil {
			out[refs[k].probe].Err = fmt.Errorf("entry(%d): %w", refs[k].slot, err)
		}
	}

	type memberCalls struct {
		have, limit, price *ethCall
		limitVal, priceVal *big.Int
	}
	var calls []*ethCall
	perProbe := make([][]memberCalls, len(probes))
	for i, probe := range probes {
		if out[i].Err != nil {
			continue
		}
		seen := map[common.Address]bool{}
		tokens := make([]common.Address, 0, len(entries[i])+len(probe.Known))
		for _, t := range append(append([]common.Address{}, entries[i]...), probe.Known...) {
			if t == (common.Address{}) || seen[t] {
				continue
			}
			seen[t] = true
			tokens = append(tokens, t)
		}
		out[i].Members = make([]MemberReading, len(tokens))
		perProbe[i] = make([]memberCalls, len(tokens))
		for j, token := range tokens {
			m := &out[i].Members[j]
			m.Token = token
			mc := &perProbe[i][j]
			if probe.Registry != (common.Address{}) {
				mc.have = mustCall(r.newCall(block, probe.Registry, fnHave, []any{token}, &m.Have))
				calls = append(calls, mc.have)
			} else {
				m.Have = true
			}
			if probe.Limiter != (common.Address{}) {
				mc.limitVal = new(big.Int)
				mc.limit = mustCall(r.newCall(block, probe.Limiter, fnLimitOf, []any{token, probe.Pool}, mc.limitVal))
				calls = append(calls, mc.limit)
			}
			if probe.Relative && probe.Quoter != (common.Address{}) {
				mc.priceVal = new(big.Int)
				mc.price = mustCall(r.newCall(block, probe.Quoter, fnPriceIndex, []any{token}, mc.priceVal))
				calls = append(calls, mc.price)
			}
		}
	}
	if err := r.run(ctx, calls); err != nil {
		return nil, err
	}
	for i := range probes {
		for j := range perProbe[i] {
			mc := perProbe[i][j]
			m := &out[i].Members[j]
			for _, c := range []*ethCall{mc.have, mc.limit, mc.price} {
				if c == nil {
					continue
				}
				if err := c.decode(); err != nil && out[i].Err == nil {
					out[i].Err = fmt.Errorf("member %s: %w", m.Token.Hex(), err)
				}
			}
			if out[i].Err != nil {
				continue
			}
			var overflow bool
			if mc.limitVal != nil {
				if m.Limit, overflow = uint256.FromBig(mc.limitVal); overflow {
					out[i].Err = errors.New("limit exceeds uint256")
				}
			}
			if mc.priceVal != nil {
				if m.PriceIndex, overflow = uint256.FromBig(mc.priceVal); overflow {
					out[i].Err = errors.New("price index exceeds uint256")
				}
			}
		}
	}
	return out, nil
}

// Balances reads token.balanceOf(pool) and pool.fees(token).
func (r *W3Reader) Balances(ctx context.Context, block uint64, probes []BalanceProbe) ([]BalanceReading, error) {
	out := make([]BalanceReading, len(probes))
	bal := make([]*big.Int, len(probes))
	fees := make([]*big.Int, len(probes))
	calls := make([]*ethCall, 0, 2*len(probes))
	for i, p := range probes {
		out[i].Pool, out[i].Token = p.Pool, p.Token
		bal[i], fees[i] = new(big.Int), new(big.Int)
		calls = append(calls,
			mustCall(r.newCall(block, p.Token, fnBalanceOf, []any{p.Pool}, bal[i])),
			mustCall(r.newCall(block, p.Pool, fnFees, []any{p.Token}, fees[i])))
	}
	if err := r.run(ctx, calls); err != nil {
		return nil, err
	}
	for i := range out {
		if err := calls[2*i].decode(); err != nil {
			out[i].Err = fmt.Errorf("balanceOf: %w", err)
			continue
		}
		if err := calls[2*i+1].decode(); err != nil {
			out[i].Err = fmt.Errorf("fees: %w", err)
			continue
		}
		b, o1 := uint256.FromBig(bal[i])
		f, o2 := uint256.FromBig(fees[i])
		if o1 || o2 {
			out[i].Err = errors.New("value exceeds uint256")
			continue
		}
		out[i].Balance, out[i].FeesHeld = *b, *f
	}
	return out, nil
}

// PairFees reads pool.getFee(in, out, 1e6), which is the pair's fee in ppm.
func (r *W3Reader) PairFees(ctx context.Context, block uint64, probes []PairProbe) ([]PairFeeReading, error) {
	out := make([]PairFeeReading, len(probes))
	vals := make([]*big.Int, len(probes))
	calls := make([]*ethCall, len(probes))
	for i, p := range probes {
		out[i].Pool, out[i].In, out[i].Out = p.Pool, p.In, p.Out
		vals[i] = new(big.Int)
		calls[i] = mustCall(r.newCall(block, p.Pool, fnGetFee, []any{p.In, p.Out, big.NewInt(PPM)}, vals[i]))
	}
	if err := r.run(ctx, calls); err != nil {
		return nil, err
	}
	for i := range out {
		if err := calls[i].decode(); err != nil {
			out[i].Err = err
			continue
		}
		if !vals[i].IsUint64() || vals[i].Uint64() > PPM {
			out[i].Err = errors.New("pair fee out of range")
			continue
		}
		out[i].FeePPM = vals[i].Uint64()
	}
	return out, nil
}

func (r *W3Reader) amounts(ctx context.Context, block uint64, fn interface {
	EncodeArgs(...any) ([]byte, error)
	DecodeReturns([]byte, ...any) error
}, probes []AmountProbe) ([]AmountReading, error) {
	out := make([]AmountReading, len(probes))
	vals := make([]*big.Int, len(probes))
	calls := make([]*ethCall, len(probes))
	for i, p := range probes {
		vals[i] = new(big.Int)
		calls[i] = mustCall(r.newCall(block, p.Pool, fn, []any{p.Out, p.In, p.Amount.ToBig()}, vals[i]))
	}
	if err := r.run(ctx, calls); err != nil {
		return nil, err
	}
	for i := range out {
		if err := calls[i].decode(); err != nil {
			out[i].Err = err
			continue
		}
		v, overflow := uint256.FromBig(vals[i])
		if overflow {
			out[i].Err = errors.New("value exceeds uint256")
			continue
		}
		out[i].Value = *v
	}
	return out, nil
}

// Quotes reads pool.getQuote(out, in, amount).
func (r *W3Reader) Quotes(ctx context.Context, block uint64, probes []AmountProbe) ([]AmountReading, error) {
	return r.amounts(ctx, block, fnGetQuote, probes)
}

// AmountsOut reads pool.getAmountOut(out, in, amount).
func (r *W3Reader) AmountsOut(ctx context.Context, block uint64, probes []AmountProbe) ([]AmountReading, error) {
	return r.amounts(ctx, block, fnGetAmountOut, probes)
}

// AmountsIn reads pool.getAmountIn(out, in, amount).
func (r *W3Reader) AmountsIn(ctx context.Context, block uint64, probes []AmountProbe) ([]AmountReading, error) {
	return r.amounts(ctx, block, fnGetAmountIn, probes)
}

// HolderBalances reads token.balanceOf(holder) for each token.
func (r *W3Reader) HolderBalances(ctx context.Context, block uint64, holder common.Address, tokens []common.Address) ([]AmountReading, error) {
	out := make([]AmountReading, len(tokens))
	vals := make([]*big.Int, len(tokens))
	calls := make([]*ethCall, len(tokens))
	for i, token := range tokens {
		vals[i] = new(big.Int)
		calls[i] = mustCall(r.newCall(block, token, fnBalanceOf, []any{holder}, vals[i]))
	}
	if err := r.run(ctx, calls); err != nil {
		return nil, err
	}
	for i := range out {
		if err := calls[i].decode(); err != nil {
			out[i].Err = err
			continue
		}
		v, overflow := uint256.FromBig(vals[i])
		if overflow {
			out[i].Err = errors.New("value exceeds uint256")
			continue
		}
		out[i].Value = *v
	}
	return out, nil
}

// Wallet reads the wallet's code, EOA nonce and Calibur sequence for keyHash.
// An undelegated wallet has no Calibur storage, so its sequence is zero.
func (r *W3Reader) Wallet(ctx context.Context, block uint64, wallet common.Address, keyHash common.Hash) (WalletReading, error) {
	var (
		code  hexutil.Bytes
		nonce hexutil.Uint64
		seq   = new(big.Int)
	)
	codeCall := &rawCall{method: "eth_getCode", args: []any{wallet, blockArg(block)}, result: &code}
	nonceCall := &rawCall{method: "eth_getTransactionCount", args: []any{wallet, blockArg(block)}, result: &nonce}
	seqCall := mustCall(r.newCall(block, wallet, fnGetSeq, []any{new(big.Int).SetBytes(keyHash[:])}, seq))
	if err := r.do(ctx, []*rawCall{codeCall, nonceCall, seqCall.raw}); err != nil {
		return WalletReading{}, err
	}
	if codeCall.err != nil {
		return WalletReading{}, codeCall.err
	}
	if nonceCall.err != nil {
		return WalletReading{}, nonceCall.err
	}
	out := WalletReading{Code: code, EOANonce: uint64(nonce)}
	if len(code) > 0 {
		if err := seqCall.decode(); err != nil {
			return WalletReading{}, fmt.Errorf("getSeq: %w", err)
		}
		if !seq.IsUint64() {
			return WalletReading{}, errors.New("calibur sequence out of range")
		}
		out.CaliburSeq = seq.Uint64()
	}
	return out, nil
}

// Simulate runs execute(BatchedCall) from the wallet to itself. When
// delegateTo is set, the wallet's code is overridden with the EIP-7702
// designator for it, so a wallet that has not delegated yet can be simulated
// without a signature.
func (r *W3Reader) Simulate(ctx context.Context, block uint64, wallet common.Address, calldata []byte, delegateTo *common.Address) (SimResult, error) {
	msg := map[string]any{"from": wallet, "to": wallet, "data": hexutil.Bytes(calldata)}
	args := []any{msg, blockArg(block)}
	if delegateTo != nil {
		args = append(args, map[common.Address]map[string]any{
			wallet: {"code": hexutil.Bytes(gocalibur.EncodeDelegationCode(*delegateTo))},
		})
	}
	var ret hexutil.Bytes
	call := &rawCall{method: "eth_call", args: args, result: &ret}
	var gas hexutil.Uint64
	estimate := &rawCall{method: "eth_estimateGas", args: args, result: &gas}
	if err := r.do(ctx, []*rawCall{call, estimate}); err != nil {
		return SimResult{}, err
	}
	if call.err != nil {
		data, ok := RevertData(call.err)
		if !ok {
			return SimResult{}, call.err
		}
		return SimResult{OK: false, RevertData: data}, nil
	}
	result := SimResult{OK: true}
	if estimate.err == nil {
		result.GasEstimate = uint64(gas)
	}
	return result, nil
}

// RevertData extracts the revert payload a node attaches to an execution
// error. ok is false for errors that are not reverts.
func RevertData(err error) ([]byte, bool) {
	var dataErr rpc.DataError
	if !errors.As(err, &dataErr) {
		if strings.Contains(strings.ToLower(err.Error()), "execution reverted") {
			return nil, true
		}
		return nil, false
	}
	switch v := dataErr.ErrorData().(type) {
	case string:
		data, decErr := hexutil.Decode(v)
		if decErr != nil {
			return nil, true
		}
		return data, true
	case json.RawMessage:
		var s string
		if json.Unmarshal(v, &s) == nil {
			data, _ := hexutil.Decode(s)
			return data, true
		}
	}
	return nil, true
}
