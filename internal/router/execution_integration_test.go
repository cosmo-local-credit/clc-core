//go:build integration

// Execution tests for the credit router against the real contracts.
//
// A local anvil (no fork) runs Calibur and the clc-protocol pool stack, deployed
// behind ERC-1967 proxies exactly as on chain, from the bytecode in
// testdata/contracts (see testdata/extract.sh). The router reads them through
// its real W3Reader, quotes, simulates, and the batch is signed with the
// wallet's root key and relayed with the real relayer's transaction builder.
//
// Run: CORE_TEST_DSN=... go test -tags integration -count=1 ./internal/router/
// Requires anvil (foundry) on PATH. No network access.
package router_test

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/gas"
	"github.com/cosmo-local-credit/clc-core/internal/relayer"
	"github.com/cosmo-local-credit/clc-core/internal/router"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	gocalibur "github.com/grassrootseconomics/go-calibur"
	"github.com/holiman/uint256"
	"github.com/lmittmann/w3"
	"github.com/lmittmann/w3/module/eth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const execChainID = 77_777

var (
	fnInitToken      = w3.MustNewFunc("initialize(string,string,uint8,address,uint256)", "")
	fnInitOwner      = w3.MustNewFunc("initialize(address)", "")
	fnInitRegistry   = w3.MustNewFunc("initialize(address,address[],bytes32[])", "")
	fnInitFeePolicy  = w3.MustNewFunc("initialize(address,uint256)", "")
	fnInitController = w3.MustNewFunc("initialize(address,uint256,address)", "")
	fnInitPool       = w3.MustNewFunc("initialize(string,string,uint8,address,address,address,address,address,address,bool,address)", "")
	fnDeployAndCall  = w3.MustNewFunc("deployAndCall(address,address,bytes)", "address")
	fnMintTo         = w3.MustNewFunc("mintTo(address,uint256)", "")
	fnMint           = w3.MustNewFunc("mint(address,uint256)", "")
	fnRegistryAdd    = w3.MustNewFunc("add(address)", "bool")
	fnSetLimitFor    = w3.MustNewFunc("setLimitFor(address,address,uint256)", "")
	fnSetPriceIndex  = w3.MustNewFunc("setPriceIndexValue(address,uint256)", "uint256")
	fnSetPairFee     = w3.MustNewFunc("setPairFee(address,address,uint256)", "")
	fnWithdrawLiq    = w3.MustNewFunc("withdrawLiquidity(address,address,uint256)", "uint256")
	fnBalanceOf      = w3.MustNewFunc("balanceOf(address)", "uint256")
	fnAllowance      = w3.MustNewFunc("allowance(address,address)", "uint256")
	fnGetAmountOut   = w3.MustNewFunc("getAmountOut(address,address,uint256)", "uint256")
	deployedTopic    = crypto.Keccak256Hash([]byte("Deployed(address,address,address)"))
)

type chainHarness struct {
	t        *testing.T
	url      string
	eth      *ethclient.Client
	w3       *w3.Client
	deployer *ecdsa.PrivateKey
	owner    common.Address
	factory  common.Address
	impls    map[string]common.Address
	calibur  common.Address
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func startAnvil(t *testing.T) *chainHarness {
	t.Helper()
	if _, err := exec.LookPath("anvil"); err != nil {
		t.Skip("anvil not installed")
	}
	port := freePort(t)
	cmd := exec.Command("anvil", "--port", fmt.Sprint(port), "--chain-id", fmt.Sprint(execChainID),
		"--hardfork", "prague", "--silent", "--code-size-limit", "50000")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	var client *ethclient.Client
	require.Eventually(t, func() bool {
		c, err := ethclient.Dial(url)
		if err != nil {
			return false
		}
		if _, err := c.ChainID(context.Background()); err != nil {
			return false
		}
		client = c
		return true
	}, 15*time.Second, 100*time.Millisecond)

	// anvil's first default account.
	key, err := crypto.HexToECDSA("ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80")
	require.NoError(t, err)
	wc, err := w3.Dial(url)
	require.NoError(t, err)
	h := &chainHarness{t: t, url: url, eth: client, w3: wc, deployer: key,
		owner: crypto.PubkeyToAddress(key.PublicKey), impls: map[string]common.Address{}}
	h.calibur = h.create(contractHex(t, "Calibur"))
	h.factory = h.create(contractHex(t, "ERC1967Factory"))
	for _, name := range []string{"GiftableToken", "SwapPool", "RelativeQuoter", "Limiter", "TokenUniqueSymbolIndex", "FeePolicy", "ProtocolFeeController"} {
		h.impls[name] = h.create(contractHex(t, name))
	}
	return h
}

func contractHex(t *testing.T, name string) []byte {
	raw, err := os.ReadFile("testdata/contracts/" + name + ".hex")
	require.NoError(t, err)
	code, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	require.NoError(t, err)
	return code
}

// send signs and mines a transaction from key; it returns the receipt.
func (h *chainHarness) send(key *ecdsa.PrivateKey, to *common.Address, data []byte) *types.Receipt {
	h.t.Helper()
	ctx := context.Background()
	from := crypto.PubkeyToAddress(key.PublicKey)
	nonce, err := h.eth.PendingNonceAt(ctx, from)
	require.NoError(h.t, err)
	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID: big.NewInt(execChainID), Nonce: nonce, GasTipCap: big.NewInt(1e9), GasFeeCap: big.NewInt(1e11),
		Gas: 12_000_000, To: to, Value: new(big.Int), Data: data,
	})
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(big.NewInt(execChainID)), key)
	require.NoError(h.t, err)
	return h.mine(signed)
}

func (h *chainHarness) mine(tx *types.Transaction) *types.Receipt {
	h.t.Helper()
	ctx := context.Background()
	require.NoError(h.t, h.eth.SendTransaction(ctx, tx))
	var receipt *types.Receipt
	require.Eventually(h.t, func() bool {
		r, err := h.eth.TransactionReceipt(ctx, tx.Hash())
		receipt = r
		return err == nil
	}, 10*time.Second, 50*time.Millisecond)
	return receipt
}

func (h *chainHarness) create(code []byte) common.Address {
	h.t.Helper()
	receipt := h.send(h.deployer, nil, code)
	require.Equal(h.t, types.ReceiptStatusSuccessful, receipt.Status)
	return receipt.ContractAddress
}

func (h *chainHarness) call(to common.Address, fn *w3.Func, args ...any) {
	h.t.Helper()
	data, err := fn.EncodeArgs(args...)
	require.NoError(h.t, err)
	receipt := h.send(h.deployer, &to, data)
	require.Equal(h.t, types.ReceiptStatusSuccessful, receipt.Status, "call %s", to.Hex())
}

// proxy deploys an ERC-1967 proxy for an implementation and initializes it in
// the same transaction, as the deploy worker does.
func (h *chainHarness) proxy(impl string, init []byte) common.Address {
	h.t.Helper()
	data, err := fnDeployAndCall.EncodeArgs(h.impls[impl], h.owner, init)
	require.NoError(h.t, err)
	receipt := h.send(h.deployer, &h.factory, data)
	require.Equal(h.t, types.ReceiptStatusSuccessful, receipt.Status)
	for _, l := range receipt.Logs {
		if len(l.Topics) == 4 && l.Topics[0] == deployedTopic {
			return common.BytesToAddress(l.Topics[1].Bytes())
		}
	}
	h.t.Fatal("no Deployed event")
	return common.Address{}
}

func must(data []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return data
}

func (h *chainHarness) token(symbol string, decimals uint8) common.Address {
	return h.proxy("GiftableToken", must(fnInitToken.EncodeArgs(symbol+" voucher", symbol, decimals, h.owner, big.NewInt(0))))
}

func (h *chainHarness) balance(token, holder common.Address) *big.Int {
	h.t.Helper()
	out := new(big.Int)
	require.NoError(h.t, h.w3.Call(eth.CallFunc(token, fnBalanceOf, holder).Returns(out)))
	return out
}

func (h *chainHarness) allowance(token, owner, spender common.Address) *big.Int {
	h.t.Helper()
	out := new(big.Int)
	require.NoError(h.t, h.w3.Call(eth.CallFunc(token, fnAllowance, owner, spender).Returns(out)))
	return out
}

func (h *chainHarness) rpc(method string, params ...any) {
	h.t.Helper()
	require.NoError(h.t, h.eth.Client().Call(nil, method, params...))
}

type execPool struct {
	address                                       common.Address
	registry, limiter, quoter, policy, controller common.Address
	members                                       []common.Address
}

type poolSpec struct {
	name       string
	defaultFee uint64
	members    []common.Address
	index      map[common.Address]uint64
	liquidity  map[common.Address]*big.Int
}

func (h *chainHarness) pool(spec poolSpec, recipient common.Address) execPool {
	p := execPool{members: spec.members}
	p.registry = h.proxy("TokenUniqueSymbolIndex", must(fnInitRegistry.EncodeArgs(h.owner, []common.Address{}, [][32]byte{})))
	p.limiter = h.proxy("Limiter", must(fnInitOwner.EncodeArgs(h.owner)))
	p.quoter = h.proxy("RelativeQuoter", must(fnInitOwner.EncodeArgs(h.owner)))
	p.policy = h.proxy("FeePolicy", must(fnInitFeePolicy.EncodeArgs(h.owner, new(big.Int).SetUint64(spec.defaultFee))))
	p.controller = h.proxy("ProtocolFeeController", must(fnInitController.EncodeArgs(h.owner, big.NewInt(20_000), recipient)))
	p.address = h.proxy("SwapPool", must(fnInitPool.EncodeArgs(spec.name, spec.name[:3], uint8(6), h.owner,
		p.policy, h.owner, p.registry, p.limiter, p.quoter, false, p.controller)))
	for _, token := range spec.members {
		h.call(p.registry, fnRegistryAdd, token)
		h.call(p.limiter, fnSetLimitFor, token, p.address, new(big.Int).Exp(big.NewInt(10), big.NewInt(30), nil))
		if idx, ok := spec.index[token]; ok {
			h.call(p.quoter, fnSetPriceIndex, token, new(big.Int).SetUint64(idx))
		}
		if amount, ok := spec.liquidity[token]; ok {
			h.call(token, fnMintTo, p.address, amount)
		}
	}
	return p
}

type execNet struct {
	h              *chainHarness
	st             store.Store
	svc            *router.Service
	rel            *relayer.Relayer
	relayerKey     *ecdsa.PrivateKey
	walletKey      *ecdsa.PrivateKey
	wallet         common.Address
	a, b, c, e     common.Address
	tax            common.Address
	p1, p2, p3, p4 execPool
}

type fixedGas struct{}

func (fixedGas) GetSettings() (*gas.GasSettings, error) {
	return &gas.GasSettings{GasFeeCap: big.NewInt(1e11), GasTipCap: big.NewInt(1e9)}, nil
}
func (fixedGas) Start() {}
func (fixedGas) Stop()  {}

type relayAdapter struct {
	r      *relayer.Relayer
	signer common.Address
}

func (a relayAdapter) SignerAddress() common.Address  { return a.signer }
func (a relayAdapter) CaliburAddress() common.Address { return a.r.CaliburAddress() }
func (a relayAdapter) RelayGasBudget(n int, d, w bool) uint64 {
	return a.r.RelayGasBudget(n, d, w)
}

func units(n int64, decimals int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(n), new(big.Int).Exp(big.NewInt(10), big.NewInt(decimals), nil))
}

func setupExecution(t *testing.T, plainERC20 ...common.Address) *execNet {
	t.Helper()
	st := newStore(t)
	h := startAnvil(t)
	n := &execNet{h: h, st: st}
	feeRecipient := common.HexToAddress("0x00000000000000000000000000000000000fee00")

	n.a, n.b, n.c = h.token("AAA", 6), h.token("BBB", 6), h.token("CCC", 6)
	n.e = h.token("EEE", 18)
	n.tax = h.create(append(contractHex(t, "MockFeeOnTransferERC20"), common.LeftPadBytes(big.NewInt(1000).Bytes(), 32)...))
	million := units(1_000, 6)
	n.p1 = h.pool(poolSpec{name: "One pool", members: []common.Address{n.a, n.b},
		liquidity: map[common.Address]*big.Int{n.a: million, n.b: million}}, feeRecipient)
	n.p2 = h.pool(poolSpec{name: "Two pool", defaultFee: 10_000, members: []common.Address{n.b, n.c},
		index:     map[common.Address]uint64{n.b: 10_000_000},
		liquidity: map[common.Address]*big.Int{n.b: million, n.c: units(100_000, 6)}}, feeRecipient)
	h.call(n.p2.policy, fnSetPairFee, n.b, n.c, big.NewInt(3_000))
	n.p3 = h.pool(poolSpec{name: "Three pool", members: []common.Address{n.c, n.e},
		index:     map[common.Address]uint64{n.e: 128_000_000},
		liquidity: map[common.Address]*big.Int{n.c: million, n.e: units(1_000, 18)}}, feeRecipient)
	n.p4 = h.pool(poolSpec{name: "Taxed pool", members: []common.Address{n.a, n.tax},
		liquidity: map[common.Address]*big.Int{n.a: million}}, feeRecipient)
	h.call(n.tax, fnMint, n.p4.address, units(1_000, 18))

	n.walletKey, _ = crypto.GenerateKey()
	n.wallet = crypto.PubkeyToAddress(n.walletKey.PublicKey)
	h.call(n.a, fnMintTo, n.wallet, units(50, 6))
	h.call(n.tax, fnMint, n.wallet, units(50, 18))
	n.relayerKey, _ = crypto.GenerateKey()
	relayerAddr := crypto.PubkeyToAddress(n.relayerKey.PublicKey)
	h.rpc("anvil_setBalance", relayerAddr, hexutil.EncodeBig(units(100, 18)))

	rel, err := relayer.New(relayer.Opts{
		Logg: slog.New(slog.DiscardHandler), GasOracle: fixedGas{}, ChainID: execChainID,
		RPCEndpoint: h.url, CaliburAddress: h.calibur.Hex(),
	})
	require.NoError(t, err)
	n.rel = rel

	n.seedCatalog(t)
	n.svc = router.New(router.Opts{
		Config: router.Config{ChainID: execChainID, MaxHops: 2, PlainERC20: plainERC20},
		Store:  st, Chain: router.NewW3Reader(h.w3, 50), Relay: relayAdapter{r: rel, signer: relayerAddr},
		Logg: slog.New(slog.DiscardHandler),
	})
	ctx := context.Background()
	require.NoError(t, n.svc.Load(ctx))
	require.NoError(t, n.svc.ReconcileOnce(ctx))
	return n
}

// seedCatalog records what the event projection would have: the pools with
// their periphery, the vouchers, and membership. Prices, balances and fees
// arrive through reconciliation.
func (n *execNet) seedCatalog(t *testing.T) {
	ctx := context.Background()
	db := n.st.Pool()
	// Leftovers from an interrupted run on this dedicated chain id.
	_, _ = db.Exec(ctx, `DELETE FROM pools WHERE chain_id = $1`, execChainID)
	_, _ = db.Exec(ctx, `DELETE FROM vouchers WHERE chain_id = $1`, execChainID)
	ids := map[common.Address]uint64{}
	suffix := n.wallet.Hex()[2:8]
	for _, tok := range []struct {
		addr     common.Address
		sym      string
		decimals int
		kind     string
	}{{n.a, "AAA", 6, "GIFTABLE"}, {n.b, "BBB", 6, "GIFTABLE"}, {n.c, "CCC", 6, "GIFTABLE"}, {n.e, "EEE", 18, "GIFTABLE"}, {n.tax, "FOT", 18, "ERC20"}} {
		var id uint64
		require.NoError(t, db.QueryRow(ctx, `INSERT INTO vouchers (chain_id, address, name, symbol, decimals, voucher_type)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, execChainID, tok.addr.Hex(), tok.sym+" exec "+suffix,
			tok.sym+suffix, tok.decimals, tok.kind).Scan(&id))
		ids[tok.addr] = id
	}
	for i, p := range []execPool{n.p1, n.p2, n.p3, n.p4} {
		var id uint64
		require.NoError(t, db.QueryRow(ctx, `INSERT INTO pools (chain_id, address, name, symbol, token_registry_address,
			limiter_address, quoter_address, quoter_type, fee_policy_address, protocol_fee_controller_address, fees_decoupled)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 'relative', $8, $9, FALSE) RETURNING id`,
			execChainID, p.address.Hex(), fmt.Sprintf("Exec pool %d %s", i, suffix), fmt.Sprintf("EP%d%s", i, suffix),
			p.registry.Hex(), p.limiter.Hex(), p.quoter.Hex(), p.policy.Hex(), p.controller.Hex()).Scan(&id))
		for _, m := range p.members {
			_, err := db.Exec(ctx, `INSERT INTO pool_tokens (pool_id, voucher_id, is_allowed) VALUES ($1, $2, TRUE)`, id, ids[m])
			require.NoError(t, err)
		}
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = db.Exec(ctx, `DELETE FROM pools WHERE chain_id = $1`, execChainID)
		_, _ = db.Exec(ctx, `DELETE FROM vouchers WHERE chain_id = $1`, execChainID)
	})
}

// relay signs the quote's batch with the wallet's root key and relays it as
// the relay worker would: a SetCodeTx with the wallet's authorization on its
// first relay, a plain transaction after.
func (n *execNet) relay(t *testing.T, q router.QuoteView) *types.Receipt {
	t.Helper()
	ctx := context.Background()
	calls := make([]gocalibur.Call, len(q.Batch.Calls))
	apiCalls := make([]apiresp.CaliburCall, len(q.Batch.Calls))
	for i, c := range q.Batch.Calls {
		calls[i] = gocalibur.Call{To: c.To, Value: c.Value, Data: c.Data}
		apiCalls[i] = apiresp.CaliburCall{To: c.To.Hex(), Value: c.Value.String(), Data: hexutil.Encode(c.Data)}
	}
	signed := gocalibur.SignedBatchedCall{
		BatchedCall: gocalibur.BatchedCall{Calls: calls, RevertOnFailure: q.Batch.RevertOnFailure},
		Nonce:       q.Batch.Nonce, KeyHash: q.Batch.KeyHash, Executor: q.Batch.Executor,
		Deadline: new(big.Int).SetUint64(q.Batch.Deadline),
	}
	domain := gocalibur.CaliburDomain(big.NewInt(execChainID), n.wallet, n.h.calibur)
	require.Equal(t, q.Digest, common.Hash(gocalibur.ComputeDigest(domain, signed)), "the quote's digest is what the wallet signs")
	sig, err := gocalibur.SignSecp256k1(n.walletKey, domain, signed, nil)
	require.NoError(t, err)

	var auth *apiresp.EIP7702Authorization
	if !q.Delegated {
		eoaNonce, err := n.h.eth.NonceAt(ctx, n.wallet, nil)
		require.NoError(t, err)
		sa, err := types.SignSetCode(n.walletKey, types.SetCodeAuthorization{
			ChainID: *uint256.NewInt(execChainID), Address: n.h.calibur, Nonce: eoaNonce,
		})
		require.NoError(t, err)
		r, s := sa.R.Bytes32(), sa.S.Bytes32()
		auth = &apiresp.EIP7702Authorization{
			ChainID: fmt.Sprint(execChainID), ContractAddress: n.h.calibur.Hex(), Nonce: fmt.Sprint(eoaNonce),
			YParity: sa.V, R: hexutil.Encode(r[:]), S: hexutil.Encode(s[:]),
		}
	}
	relayerAddr := crypto.PubkeyToAddress(n.relayerKey.PublicKey)
	nonce, err := n.h.eth.PendingNonceAt(ctx, relayerAddr)
	require.NoError(t, err)
	built, err := n.rel.BuildAndSignRelay(ctx, n.relayerKey, relayerAddr, n.wallet, apiresp.CaliburSignedBatch{
		BatchedCall: apiresp.CaliburBatchedCall{Calls: apiCalls, RevertOnFailure: q.Batch.RevertOnFailure},
		Nonce:       q.Batch.Nonce.String(), KeyHash: q.Batch.KeyHash.Hex(), Executor: q.Batch.Executor.Hex(),
		Deadline: fmt.Sprint(q.Batch.Deadline),
	}, sig, "secp256k1", auth, q.Delegated, nonce)
	require.NoError(t, err)
	return n.h.mine(built.Tx)
}

func (n *execNet) quote(t *testing.T, mode router.Mode, amount *big.Int, hops ...router.HopRef) (router.QuoteView, error) {
	t.Helper()
	n.svc.Forget(n.wallet)
	return n.svc.Quote(context.Background(), n.wallet, router.QuoteRequest{Hops: hops, Mode: mode, Amount: *uint256.MustFromBig(amount)})
}

func (n *execNet) abc() []router.HopRef {
	return []router.HopRef{{Pool: n.p1.address, TokenIn: n.a, TokenOut: n.b}, {Pool: n.p2.address, TokenIn: n.b, TokenOut: n.c}}
}

// Differential: the local arithmetic equals getAmountOut for every directed
// leg, across decimal scales, indexes and pair fees, and chains for two hops.
func TestExecutionLocalQuoteMatchesTheContracts(t *testing.T) {
	n := setupExecution(t)
	// Until the sweep (or a quote) records a pair fee, discovery uses the
	// pool's default fee: an estimate. After it, the arithmetic is exact.
	changed, err := n.svc.RunPairFeeSweep(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, changed, "one pair overrides its pool's default fee")
	require.Eventually(t, func() bool {
		snap := n.svc.Snapshot()
		p2, _ := snap.PoolByAddress(n.p2.address)
		b, _ := snap.TokenByAddress(n.b)
		c, _ := snap.TokenByAddress(n.c)
		leg, _ := snap.LegFor(p2, b, c)
		_, source := snap.LegParams(leg)
		return source == router.FeeSourcePair
	}, 5*time.Second, 20*time.Millisecond)
	snap := n.svc.Snapshot()
	checked := 0
	for token := range snap.Tokens {
		for _, leg := range snap.Legs(int32(token)) {
			params, _ := snap.LegParams(leg)
			in, out := snap.Tokens[leg.In], snap.Tokens[leg.Out]
			for _, raw := range []string{"1", "7", "999999", "1000000", "123456789", "1000000000000", "333333333333333333"} {
				amount := uint256.MustFromDecimal(raw)
				local, localErr := params.Arithmetic(amount)
				chainOut := new(big.Int)
				err := n.h.w3.Call(eth.CallFunc(snap.Pools[leg.Pool].Addr, fnGetAmountOut, out.Address, in.Address, amount.ToBig()).Returns(chainOut))
				if localErr != nil {
					assert.Error(t, err, "%s→%s %s: local refused (%v), chain did not", in.Symbol, out.Symbol, raw, localErr)
					continue
				}
				require.NoError(t, err, "%s→%s %s", in.Symbol, out.Symbol, raw)
				require.Equal(t, chainOut.String(), local.Net.Dec(), "%s→%s %s", in.Symbol, out.Symbol, raw)
				checked++
			}
		}
	}
	assert.Greater(t, checked, 30)
	assert.Equal(t, 6, snap.LegCount(), "the taxed voucher's pool is not routable")

	// Two hops: chaining getAmountOut, as SwapRouter.quoteExactInput does.
	q, err := n.quote(t, router.ExactInput, units(3, 6), n.abc()...)
	require.NoError(t, err)
	mid, final := new(big.Int), new(big.Int)
	require.NoError(t, n.h.w3.Call(eth.CallFunc(n.p1.address, fnGetAmountOut, n.b, n.a, units(3, 6)).Returns(mid)))
	require.NoError(t, n.h.w3.Call(eth.CallFunc(n.p2.address, fnGetAmountOut, n.c, n.b, mid).Returns(final)))
	out := q.Quote.AmountOut()
	assert.Equal(t, final.String(), out.Dec())
	assert.Equal(t, uint64(3_000), q.Legs[1].FeePPM, "the quote reads the exact pair fee")
}

func TestExecutionTwoHopExactInputFirstRelay(t *testing.T) {
	n := setupExecution(t)
	amount := units(2, 6)
	q, err := n.quote(t, router.ExactInput, amount, n.abc()...)
	require.NoError(t, err)
	require.False(t, q.Delegated, "a fresh wallet is simulated through a code override")
	require.True(t, q.Simulation.OK)

	// A bridge balance the wallet already held must not be spent.
	n.h.call(n.b, fnMintTo, n.wallet, big.NewInt(12_345))
	aBefore, bBefore, cBefore := n.h.balance(n.a, n.wallet), n.h.balance(n.b, n.wallet), n.h.balance(n.c, n.wallet)

	receipt := n.relay(t, q)
	require.Equal(t, types.ReceiptStatusSuccessful, receipt.Status, "simulation said it would succeed")

	assert.Equal(t, new(big.Int).Sub(aBefore, amount).String(), n.h.balance(n.a, n.wallet).String())
	cGot := new(big.Int).Sub(n.h.balance(n.c, n.wallet), cBefore)
	out := q.Quote.AmountOut()
	assert.Equal(t, out.Dec(), cGot.String(), "fixed-rate pools deliver exactly the quote")
	bridge := q.Quote.BridgeSurplus()[0]
	assert.Equal(t, new(big.Int).Add(bBefore, bridge.ToBig()).String(), n.h.balance(n.b, n.wallet).String(),
		"only the reported bridge surplus stays; the pre-existing balance is untouched")
	assert.Zero(t, n.h.allowance(n.a, n.wallet, n.p1.address).Sign())
	assert.Zero(t, n.h.allowance(n.b, n.wallet, n.p2.address).Sign())
}

func TestExecutionExactOutputNeverDeliversLess(t *testing.T) {
	n := setupExecution(t)
	// Delegate first, so this relay takes the plain-transaction path.
	n.h.rpc("anvil_setCode", n.wallet, hexutil.Encode(gocalibur.EncodeDelegationCode(n.h.calibur)))
	want := units(15, 6) // C is a tenth of B, so this needs ~1.5 A
	q, err := n.quote(t, router.ExactOutput, want, n.abc()...)
	require.NoError(t, err)
	require.True(t, q.Delegated)
	cBefore := n.h.balance(n.c, n.wallet)

	receipt := n.relay(t, q)
	require.Equal(t, types.ReceiptStatusSuccessful, receipt.Status)
	got := new(big.Int).Sub(n.h.balance(n.c, n.wallet), cBefore)
	assert.True(t, got.Cmp(want) >= 0, "delivered %s, requested %s", got, want)
	surplus := q.OutputSurplus()
	assert.Equal(t, new(big.Int).Sub(got, want).String(), surplus.Dec(), "the reported surplus is the actual surplus")

	// The input is the smallest that works: one unit less is refused.
	smaller := new(big.Int).Sub(q.Quote.Inputs[0].ToBig(), big.NewInt(1))
	alt, err := n.quote(t, router.ExactInput, smaller, n.abc()...)
	if err == nil {
		altOut := alt.Quote.AmountOut()
		assert.True(t, altOut.ToBig().Cmp(want) < 0 || alt.Quote.Inputs[1].Cmp(&q.Quote.Inputs[1]) < 0)
	}
}

func TestExecutionSecondHopFailureRollsBackEverything(t *testing.T) {
	n := setupExecution(t)
	n.h.rpc("anvil_setCode", n.wallet, hexutil.Encode(gocalibur.EncodeDelegationCode(n.h.calibur)))
	q, err := n.quote(t, router.ExactInput, units(2, 6), n.abc()...)
	require.NoError(t, err)

	// Another trader takes hop two's liquidity between quote and mining.
	n.h.call(n.p2.address, fnWithdrawLiq, n.c, n.h.owner, n.h.balance(n.c, n.p2.address))
	aBefore, bBefore := n.h.balance(n.a, n.wallet), n.h.balance(n.b, n.wallet)

	// The simulation agrees before signing.
	_, err = n.quote(t, router.ExactInput, units(2, 6), n.abc()...)
	var routeErr *router.RouteError
	require.ErrorAs(t, err, &routeErr)
	assert.Equal(t, router.ReasonLiquidity, routeErr.Reason)

	receipt := n.relay(t, q)
	assert.Equal(t, types.ReceiptStatusFailed, receipt.Status)
	assert.Equal(t, aBefore.String(), n.h.balance(n.a, n.wallet).String(), "hop one rolled back")
	assert.Equal(t, bBefore.String(), n.h.balance(n.b, n.wallet).String())
	assert.Zero(t, n.h.allowance(n.a, n.wallet, n.p1.address).Sign(), "approvals rolled back")
}

func TestExecutionMinimumAndDeadlineHold(t *testing.T) {
	n := setupExecution(t)
	n.h.rpc("anvil_setCode", n.wallet, hexutil.Encode(gocalibur.EncodeDelegationCode(n.h.calibur)))

	// An owner raises C's price after the quote: hop two would deliver less
	// than the signed minimum.
	q, err := n.quote(t, router.ExactInput, units(1, 6), n.abc()...)
	require.NoError(t, err)
	n.h.call(n.p2.quoter, fnSetPriceIndex, n.c, big.NewInt(2_000_000))
	cBefore := n.h.balance(n.c, n.wallet)
	receipt := n.relay(t, q)
	assert.Equal(t, types.ReceiptStatusFailed, receipt.Status, "minAmountOut holds")
	assert.Equal(t, cBefore.String(), n.h.balance(n.c, n.wallet).String())
	n.h.call(n.p2.quoter, fnSetPriceIndex, n.c, big.NewInt(0))

	// The deadline passes before mining.
	q, err = n.quote(t, router.ExactInput, units(1, 6), n.abc()...)
	require.NoError(t, err)
	n.h.rpc("evm_increaseTime", 3_600)
	n.h.rpc("evm_mine")
	receipt = n.relay(t, q)
	assert.Equal(t, types.ReceiptStatusFailed, receipt.Status, "an expired batch does not execute")
	assert.Equal(t, cBefore.String(), n.h.balance(n.c, n.wallet).String())
}

// The router supports vouchers whose transfers move exactly the stated
// amount. A transfer-taxed voucher is excluded from discovery; forced through
// the allowlist into a quote, the simulation fails; forced on chain, the whole
// batch reverts and nothing moves.
func TestExecutionTaxedVoucherCannotDeliverLess(t *testing.T) {
	// Excluded by default.
	excluded := setupExecution(t)
	_, err := excluded.quote(t, router.ExactInput, units(1, 18),
		router.HopRef{Pool: excluded.p4.address, TokenIn: excluded.tax, TokenOut: excluded.a})
	var routeErr *router.RouteError
	require.ErrorAs(t, err, &routeErr)
	assert.Equal(t, router.ReasonUnsupportedToken, routeErr.Reason)

	// Wrongly allowlisted: the quote's simulation catches it, as input and as
	// output. Contract addresses follow the deployer's nonce, so a second
	// chain set up the same way puts the taxed voucher at the same address.
	n := setupExecution(t, excluded.tax)
	require.Equal(t, excluded.tax, n.tax)
	n.h.rpc("anvil_setCode", n.wallet, hexutil.Encode(gocalibur.EncodeDelegationCode(n.h.calibur)))
	for _, c := range []struct {
		hop    router.HopRef
		amount *big.Int
	}{
		{router.HopRef{Pool: n.p4.address, TokenIn: n.tax, TokenOut: n.a}, units(1, 18)},
		{router.HopRef{Pool: n.p4.address, TokenIn: n.a, TokenOut: n.tax}, units(1, 6)},
	} {
		hop := c.hop
		_, err := n.quote(t, router.ExactInput, c.amount, hop)
		var sim *router.SimulationError
		require.ErrorAs(t, err, &sim, "%s→%s", hop.TokenIn.Hex(), hop.TokenOut.Hex())
		assert.Equal(t, "InsufficientOutput", sim.Revert.Reason)
	}

	// Forced on chain: build the batch from local arithmetic and relay it.
	snap := n.svc.Snapshot()
	pool, _ := snap.PoolByAddress(n.p4.address)
	tIn, _ := snap.TokenByAddress(n.a)
	tOut, _ := snap.TokenByAddress(n.tax)
	leg, ok := snap.LegFor(pool, tIn, tOut)
	require.True(t, ok)
	params, _ := snap.LegParams(leg)
	amount := uint256.MustFromBig(units(1, 6))
	hop, err := params.Swap(amount)
	require.NoError(t, err)
	deadline := uint64(time.Now().Add(5 * time.Minute).Unix())
	calls := router.BuildRouteCalls([]router.PlannedHop{{
		Pool: n.p4.address, TokenIn: n.a, TokenOut: n.tax, AmountIn: amount.ToBig(), MinOut: hop.Net.ToBig(), Recipient: n.wallet,
	}}, deadline)
	relayerAddr := crypto.PubkeyToAddress(n.relayerKey.PublicKey)
	forced := router.QuoteView{Delegated: true, Batch: router.SignedBatch{
		Calls: calls, RevertOnFailure: true, Nonce: router.CaliburNonce(common.Hash{}, 0), Executor: relayerAddr, Deadline: deadline,
	}}
	forced.Digest = forced.Batch.Digest(big.NewInt(execChainID), n.wallet, n.h.calibur)
	aBefore, tBefore := n.h.balance(n.a, n.wallet), n.h.balance(n.tax, n.wallet)
	receipt := n.relay(t, forced)
	assert.Equal(t, types.ReceiptStatusFailed, receipt.Status)
	assert.Equal(t, aBefore.String(), n.h.balance(n.a, n.wallet).String())
	assert.Equal(t, tBefore.String(), n.h.balance(n.tax, n.wallet).String())
}
