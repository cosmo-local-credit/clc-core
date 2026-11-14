// Package routertest provides an in-memory chain for router tests: pools with
// registries, limiters, relative quoters and fee contracts, token balances and
// wallets, read through the router.ChainReader interface.
package routertest

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/cosmo-local-credit/clc-core/internal/router"
	"github.com/ethereum/go-ethereum/common"
	gocalibur "github.com/grassrootseconomics/go-calibur"
	"github.com/holiman/uint256"
)

type (
	// Pool is one SwapPool and its periphery.
	Pool struct {
		Registry, Limiter, Quoter, FeePolicy, Controller common.Address
		FeesDecoupled                                    bool
		Seal                                             uint8
		DefaultFee                                       uint64
		PairFees                                         map[[2]common.Address]uint64
		ProtocolFee                                      uint64
		Recipient                                        common.Address
		// Members is the registry's entry list, in order.
		Members []common.Address
		Limits  map[common.Address]uint256.Int
		Index   map[common.Address]uint256.Int
		Fees    map[common.Address]uint256.Int
		// QuoteSkew is added to getQuote, to simulate a quoter that disagrees
		// with the local arithmetic.
		QuoteSkew uint64
	}

	// Chain is the stub. Every field may be edited between calls under Lock.
	Chain struct {
		mu       sync.Mutex
		Block    uint64
		Pools    map[common.Address]*Pool
		Decimals map[common.Address]uint8
		// Holdings[token][holder].
		Holdings map[common.Address]map[common.Address]uint256.Int
		Code     map[common.Address][]byte
		Seq      map[common.Address]uint64
		Nonce    map[common.Address]uint64
		// SimRevert, when set, makes every simulation revert with it.
		SimRevert []byte
		GasUsed   uint64
		// Down makes every read fail at the transport.
		Down bool
		// Calls counts reads by method, for sharing assertions.
		Calls map[string]int
	}

	// Relay is a fixed relayer for quotes.
	Relay struct {
		Signer, Calibur common.Address
		Budget          uint64
	}
)

// New returns an empty chain at block 1.
func New() *Chain {
	return &Chain{
		Block:    1,
		Pools:    map[common.Address]*Pool{},
		Decimals: map[common.Address]uint8{},
		Holdings: map[common.Address]map[common.Address]uint256.Int{},
		Code:     map[common.Address][]byte{},
		Seq:      map[common.Address]uint64{},
		Nonce:    map[common.Address]uint64{},
		GasUsed:  300_000,
		Calls:    map[string]int{},
	}
}

// Lock and Unlock guard edits made by a test while the router reads.
func (c *Chain) Lock()   { c.mu.Lock() }
func (c *Chain) Unlock() { c.mu.Unlock() }

// AddPool registers a pool and returns it for configuration.
func (c *Chain) AddPool(address common.Address, p *Pool) *Pool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p.Limits == nil {
		p.Limits = map[common.Address]uint256.Int{}
	}
	if p.Index == nil {
		p.Index = map[common.Address]uint256.Int{}
	}
	if p.Fees == nil {
		p.Fees = map[common.Address]uint256.Int{}
	}
	if p.PairFees == nil {
		p.PairFees = map[[2]common.Address]uint64{}
	}
	c.Pools[address] = p
	return p
}

// SetBalance sets token.balanceOf(holder).
func (c *Chain) SetBalance(token, holder common.Address, amount uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setBalance(token, holder, *uint256.NewInt(amount))
}

func (c *Chain) setBalance(token, holder common.Address, amount uint256.Int) {
	if c.Holdings[token] == nil {
		c.Holdings[token] = map[common.Address]uint256.Int{}
	}
	c.Holdings[token][holder] = amount
}

func (c *Chain) balance(token, holder common.Address) uint256.Int {
	return c.Holdings[token][holder]
}

// Delegate puts the Calibur designator on wallet.
func (c *Chain) Delegate(wallet, calibur common.Address) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Code[wallet] = gocalibur.EncodeDelegationCode(calibur)
}

var (
	errDown = errors.New("routertest: chain is down")

	_ router.ChainReader = (*Chain)(nil)
)

func (c *Chain) enter(method string) error {
	c.mu.Lock()
	c.Calls[method]++
	if c.Down {
		c.mu.Unlock()
		return errDown
	}
	return nil
}

func (c *Chain) Head(context.Context) (router.Head, error) {
	if err := c.enter("Head"); err != nil {
		return router.Head{}, err
	}
	defer c.mu.Unlock()
	return router.Head{Number: c.Block, Hash: common.BigToHash(uint256.NewInt(c.Block).ToBig()), Time: 1_700_000_000 + c.Block*5}, nil
}

func (c *Chain) PoolParams(_ context.Context, _ uint64, pools []common.Address) ([]router.PoolParams, error) {
	if err := c.enter("PoolParams"); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	out := make([]router.PoolParams, len(pools))
	for i, addr := range pools {
		p, ok := c.Pools[addr]
		if !ok {
			out[i] = router.PoolParams{Pool: addr, Err: errors.New("no code")}
			continue
		}
		protocol := p.ProtocolFee
		if p.Controller == (common.Address{}) || p.Recipient == (common.Address{}) {
			protocol = 0
		}
		out[i] = router.PoolParams{
			Pool: addr, Registry: p.Registry, Limiter: p.Limiter, Quoter: p.Quoter, FeePolicy: p.FeePolicy,
			Controller: p.Controller, FeesDecoupled: p.FeesDecoupled, SealState: p.Seal,
			DefaultFeePPM: p.DefaultFee, ProtocolFeePPM: protocol, ProtocolRecipient: p.Recipient,
		}
	}
	return out, nil
}

func (c *Chain) PoolStructure(_ context.Context, _ uint64, probes []router.StructureProbe) ([]router.PoolStructure, error) {
	if err := c.enter("PoolStructure"); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	out := make([]router.PoolStructure, len(probes))
	for i, probe := range probes {
		p, ok := c.Pools[probe.Pool]
		out[i].Pool = probe.Pool
		if !ok {
			out[i].Err = errors.New("no code")
			continue
		}
		seen := map[common.Address]bool{}
		var tokens []common.Address
		if !probe.SkipEntries {
			tokens = append(tokens, p.Members...)
		}
		tokens = append(tokens, probe.Known...)
		for _, t := range tokens {
			if seen[t] {
				continue
			}
			seen[t] = true
			m := router.MemberReading{Token: t, Have: p.Registry == (common.Address{}) || contains(p.Members, t)}
			if probe.Limiter != (common.Address{}) {
				limit := p.Limits[t]
				m.Limit = &limit
			}
			if probe.Relative && probe.Quoter != (common.Address{}) {
				index := p.Index[t]
				m.PriceIndex = &index
			}
			out[i].Members = append(out[i].Members, m)
		}
	}
	return out, nil
}

func contains(list []common.Address, a common.Address) bool { return slices.Contains(list, a) }

func (c *Chain) Balances(_ context.Context, _ uint64, probes []router.BalanceProbe) ([]router.BalanceReading, error) {
	if err := c.enter("Balances"); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	out := make([]router.BalanceReading, len(probes))
	for i, probe := range probes {
		out[i] = router.BalanceReading{Pool: probe.Pool, Token: probe.Token, Balance: c.balance(probe.Token, probe.Pool)}
		if p, ok := c.Pools[probe.Pool]; ok {
			out[i].FeesHeld = p.Fees[probe.Token]
		}
	}
	return out, nil
}

func (p *Pool) fee(in, out common.Address) uint64 {
	if fee, ok := p.PairFees[[2]common.Address{in, out}]; ok {
		return fee
	}
	return p.DefaultFee
}

func (c *Chain) PairFees(_ context.Context, _ uint64, probes []router.PairProbe) ([]router.PairFeeReading, error) {
	if err := c.enter("PairFees"); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	out := make([]router.PairFeeReading, len(probes))
	for i, probe := range probes {
		out[i] = router.PairFeeReading{Pool: probe.Pool, In: probe.In, Out: probe.Out}
		if p, ok := c.Pools[probe.Pool]; ok {
			out[i].FeePPM = p.fee(probe.In, probe.Out)
		} else {
			out[i].Err = errors.New("no code")
		}
	}
	return out, nil
}

// leg is the pool's view of one pair, for the arithmetic getters.
func (c *Chain) leg(pool common.Address, in, out common.Address) (router.LegParams, bool) {
	p, ok := c.Pools[pool]
	if !ok {
		return router.LegParams{}, false
	}
	protocol := p.ProtocolFee
	if p.Controller == (common.Address{}) || p.Recipient == (common.Address{}) {
		protocol = 0
	}
	params := router.LegParams{
		Quoter: router.QuoterRelative, FeePPM: p.fee(in, out), ProtocolPPM: protocol,
		InDecimals: c.Decimals[in], OutDecimals: c.Decimals[out],
		InIndex: p.Index[in], OutIndex: p.Index[out],
	}
	if p.Quoter == (common.Address{}) {
		params.Quoter = router.QuoterNone
	}
	return params, true
}

func (c *Chain) Quotes(_ context.Context, _ uint64, probes []router.AmountProbe) ([]router.AmountReading, error) {
	if err := c.enter("Quotes"); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	out := make([]router.AmountReading, len(probes))
	for i, probe := range probes {
		leg, ok := c.leg(probe.Pool, probe.In, probe.Out)
		if !ok {
			out[i].Err = errors.New("no code")
			continue
		}
		gross, err := leg.Gross(&probe.Amount)
		if err != nil {
			out[i].Err = err
			continue
		}
		gross.Add(&gross, uint256.NewInt(c.Pools[probe.Pool].QuoteSkew))
		out[i].Value = gross
	}
	return out, nil
}

func (c *Chain) AmountsOut(_ context.Context, _ uint64, probes []router.AmountProbe) ([]router.AmountReading, error) {
	if err := c.enter("AmountsOut"); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	out := make([]router.AmountReading, len(probes))
	for i, probe := range probes {
		leg, ok := c.leg(probe.Pool, probe.In, probe.Out)
		if !ok {
			out[i].Err = errors.New("no code")
			continue
		}
		hop, err := leg.Arithmetic(&probe.Amount)
		if err != nil {
			out[i].Err = err
			continue
		}
		hop.Net.Add(&hop.Net, uint256.NewInt(c.Pools[probe.Pool].QuoteSkew))
		out[i].Value = hop.Net
	}
	return out, nil
}

// AmountsIn bounds generously: the contract's getAmountIn rounds up twice and
// adds one, so any honest engine input is at or below it.
func (c *Chain) AmountsIn(_ context.Context, _ uint64, probes []router.AmountProbe) ([]router.AmountReading, error) {
	if err := c.enter("AmountsIn"); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	out := make([]router.AmountReading, len(probes))
	for i, probe := range probes {
		leg, ok := c.leg(probe.Pool, probe.In, probe.Out)
		if !ok {
			out[i].Err = errors.New("no code")
			continue
		}
		x, err := leg.MinInputForNet(&probe.Amount, router.DefaultSearchBudget)
		if err != nil {
			out[i].Err = err
			continue
		}
		out[i].Value = *x.Add(&x, uint256.NewInt(1))
	}
	return out, nil
}

func (c *Chain) HolderBalances(_ context.Context, _ uint64, holder common.Address, tokens []common.Address) ([]router.AmountReading, error) {
	if err := c.enter("HolderBalances"); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	out := make([]router.AmountReading, len(tokens))
	for i, t := range tokens {
		out[i].Value = c.balance(t, holder)
	}
	return out, nil
}

func (c *Chain) Wallet(_ context.Context, _ uint64, wallet common.Address, _ common.Hash) (router.WalletReading, error) {
	if err := c.enter("Wallet"); err != nil {
		return router.WalletReading{}, err
	}
	defer c.mu.Unlock()
	return router.WalletReading{Code: c.Code[wallet], CaliburSeq: c.Seq[wallet], EOANonce: c.Nonce[wallet]}, nil
}

func (c *Chain) Simulate(_ context.Context, _ uint64, _ common.Address, _ []byte, _ *common.Address) (router.SimResult, error) {
	if err := c.enter("Simulate"); err != nil {
		return router.SimResult{}, err
	}
	defer c.mu.Unlock()
	if c.SimRevert != nil {
		return router.SimResult{OK: false, RevertData: c.SimRevert}, nil
	}
	return router.SimResult{OK: true, GasEstimate: c.GasUsed}, nil
}

func (r Relay) SignerAddress() common.Address  { return r.Signer }
func (r Relay) CaliburAddress() common.Address { return r.Calibur }
func (r Relay) RelayGasBudget(int, bool, bool) uint64 {
	if r.Budget == 0 {
		return 1_375_000
	}
	return r.Budget
}
