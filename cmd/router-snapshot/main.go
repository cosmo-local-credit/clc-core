// Command router-snapshot captures the credit router's graph as a test
// fixture, from public data only: the pool catalog from a clc-core
// deployment's public API, and chain state read at one pinned block through
// the router's own reconciliation code.
//
// Nothing is written anywhere but the output file. No credential is needed:
// the API reads are the public pool catalog, and the chain reads are eth_call
// against an archive RPC. Names, owners, contact details and wallet balances
// are not recorded.
//
//	go run ./cmd/router-snapshot -api https://api.cosmolocal.credit \
//	    -rpc https://rpc.gnosischain.com -block 49254469 \
//	    -out internal/router/testdata/production_graph.json.gz
package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/router"
	"github.com/ethereum/go-ethereum/common"
	"github.com/lmittmann/w3"
)

type (
	apiPool struct {
		ID                           uint64  `json:"id"`
		ChainID                      int64   `json:"chainId"`
		Address                      string  `json:"address"`
		Active                       bool    `json:"active"`
		QuoterType                   *string `json:"quoterType"`
		TokenRegistryAddress         *string `json:"tokenRegistryAddress"`
		LimiterAddress               *string `json:"limiterAddress"`
		QuoterAddress                *string `json:"quoterAddress"`
		FeePolicyAddress             *string `json:"feePolicyAddress"`
		ProtocolFeeControllerAddress *string `json:"protocolFeeControllerAddress"`
		FeesDecoupled                *bool   `json:"feesDecoupled"`
	}

	apiVoucher struct {
		ID          uint64  `json:"id"`
		ChainID     int64   `json:"chainId"`
		Address     string  `json:"address"`
		Decimals    *int    `json:"decimals"`
		VoucherType *string `json:"voucherType"`
		Active      bool    `json:"active"`
		IsAllowed   bool    `json:"isAllowed"`
		TokenLimit  *string `json:"tokenLimit"`
	}

	// Fixture is the file format; internal/router's fixture test reads it.
	Fixture struct {
		Meta     FixtureMeta       `json:"meta"`
		Rows     router.StateRows  `json:"rows"`
		PairFees []router.PairFee  `json:"pairFees"`
		Counts   map[string]int    `json:"counts"`
		Loops    map[string]string `json:"loops"`
	}

	FixtureMeta struct {
		ChainID    int64     `json:"chainId"`
		Block      uint64    `json:"block"`
		CapturedAt time.Time `json:"capturedAt"`
		Source     string    `json:"source"`
	}

	// memStore is a router.StateStore in memory, so the reconciliation code
	// runs unchanged without a database.
	memStore struct {
		mu   sync.Mutex
		rows router.StateRows
	}

	// pinned reads every call at one block and retries transport errors, as
	// a public RPC rate-limits batches.
	pinned struct {
		router.ChainReader

		head router.Head
	}
)

var (
	errNotOK = errors.New("not ok")
)

func main() {
	var (
		apiBase = flag.String("api", "https://api.cosmolocal.credit", "clc-core deployment whose public catalog to read")
		rpcURL  = flag.String("rpc", "https://rpc.gnosischain.com", "archive JSON-RPC endpoint")
		chainID = flag.Int64("chain", 100, "chain id")
		block   = flag.Uint64("block", 0, "block to pin every read to; 0 is the current head")
		batch   = flag.Int("batch", 60, "calls per JSON-RPC batch")
		out     = flag.String("out", "internal/router/testdata/production_graph.json.gz", "output file")
	)
	flag.Parse()
	logg := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(context.Background(), logg, *apiBase, *rpcURL, *chainID, *block, *batch, *out); err != nil {
		logg.Error("snapshot failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, logg *slog.Logger, apiBase, rpcURL string, chainID int64, block uint64, batch int, out string) error {
	rows, err := catalog(ctx, logg, apiBase, chainID)
	if err != nil {
		return fmt.Errorf("catalog: %w", err)
	}
	logg.Info("catalog read", "pools", len(rows.Pools), "vouchers", len(rows.Tokens), "edges", len(rows.Edges))

	client, err := w3.Dial(rpcURL)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	reader := router.NewW3Reader(client, batch)
	head, err := reader.Head(ctx)
	if err != nil {
		return err
	}
	if block != 0 {
		head = router.Head{Number: block}
	}
	chain := &pinned{ChainReader: reader, head: head}
	store := &memStore{rows: rows}

	svc := router.New(router.Opts{
		Config: router.Config{
			ChainID: chainID, MaxHops: 2, ChainBatchSize: batch,
			// No pause between pair-fee batches.
			PairFeeInterval: time.Nanosecond,
		},
		Store: store, Chain: chain, Logg: logg,
	})
	if err := svc.Load(ctx); err != nil {
		return err
	}
	if err := svc.ReconcileOnce(ctx); err != nil {
		return fmt.Errorf("reconcile at %d: %w", head.Number, err)
	}
	if _, err := svc.RunPairFeeSweep(ctx); err != nil {
		return fmt.Errorf("pair fees at %d: %w", head.Number, err)
	}
	snap := svc.Snapshot()
	stats, err := svc.Stats()
	if err != nil {
		return err
	}

	// Keep only pair fees that differ from their pool's default: the rest
	// are implied, and the fixture stays small.
	var fees []router.PairFee
	for _, f := range snap.Fees().Entries() {
		if pool, ok := snap.PoolByAddress(f.Pool); ok && snap.Pools[pool].DefaultFeePPM != f.FeePPM {
			fees = append(fees, f)
		}
	}
	slices.SortFunc(fees, func(a, b router.PairFee) int {
		return strings.Compare(a.Pool.Hex()+a.In.Hex()+a.Out.Hex(), b.Pool.Hex()+b.In.Hex()+b.Out.Hex())
	})

	fixture := Fixture{
		Meta: FixtureMeta{
			ChainID: chainID, Block: head.Number, CapturedAt: time.Now().UTC(),
			Source: "public pool catalog (" + apiBase + ") and eth_call at the pinned block",
		},
		Rows:     store.snapshot(),
		PairFees: fees,
		Counts: map[string]int{
			"pools": len(snap.Pools), "verifiedPools": stats.Verified, "vouchers": len(snap.Tokens),
			"edges": len(snap.Edges), "legs": snap.LegCount(), "pairFeesRead": snap.Fees().Len(),
		},
		Loops: map[string]string{},
	}
	for name, l := range stats.Loops {
		fixture.Loops[name] = fmt.Sprintf("corrections=%d error=%q", l.Corrections, l.LastError)
	}
	return write(out, fixture)
}

func write(path string, fixture Fixture) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(f)
	enc := json.NewEncoder(gz)
	enc.SetIndent("", " ")
	if err := enc.Encode(fixture); err != nil {
		_ = f.Close()
		return err
	}
	if err := gz.Close(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// getJSON reads one public API page, waiting out the rate limit.
func getJSON(ctx context.Context, rawURL string, out any) error {
	for attempt := range 30 {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", "clc-router-snapshot")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			wait, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
			time.Sleep(time.Duration(max(wait, 1)) * time.Second)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("%s: %d %s", rawURL, resp.StatusCode, body)
		}
		var envelope struct {
			Ok     bool            `json:"ok"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			return err
		}
		if !envelope.Ok {
			return fmt.Errorf("%s: %w", rawURL, errNotOK)
		}
		return json.Unmarshal(envelope.Result, out)
	}
	return fmt.Errorf("%s: gave up after rate limiting", rawURL)
}

// pages walks the numeric keyset pagination of a public list.
func pages[T any](ctx context.Context, base, key string, each func([]T)) error {
	cursor := uint64(0)
	for {
		u := base + "?perPage=100"
		if cursor != 0 {
			u += "&next=true&cursor=" + strconv.FormatUint(cursor, 10)
		}
		var page map[string]json.RawMessage
		if err := getJSON(ctx, u, &page); err != nil {
			return err
		}
		var items []T
		if err := json.Unmarshal(page[key], &items); err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}
		each(items)
		var last uint64
		if err := json.Unmarshal(page["last"], &last); err != nil || last == 0 || last == cursor {
			return err
		}
		cursor = last
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func catalog(ctx context.Context, logg *slog.Logger, apiBase string, chainID int64) (router.StateRows, error) {
	var rows router.StateRows
	var pools []apiPool
	if err := pages(ctx, apiBase+"/api/v2/pools", "pools", func(p []apiPool) { pools = append(pools, p...) }); err != nil {
		return rows, err
	}
	tokens := map[uint64]router.TokenRow{}
	for i, p := range pools {
		if p.ChainID != chainID {
			continue
		}
		decoupled := p.FeesDecoupled != nil && *p.FeesDecoupled
		rows.Pools = append(rows.Pools, router.PoolRow{
			ID: p.ID, Address: p.Address, Active: p.Active, QuoterType: deref(p.QuoterType),
			RegistryAddress: deref(p.TokenRegistryAddress), LimiterAddress: deref(p.LimiterAddress),
			QuoterAddress: deref(p.QuoterAddress), FeePolicyAddress: deref(p.FeePolicyAddress),
			ControllerAddress: deref(p.ProtocolFeeControllerAddress), FeesDecoupled: decoupled,
		})
		path := apiBase + "/api/v2/pools/" + url.PathEscape(p.Address) + "/vouchers"
		err := pages(ctx, path, "vouchers", func(vs []apiVoucher) {
			for _, v := range vs {
				voucherType := deref(v.VoucherType)
				tokens[v.ID] = router.TokenRow{
					ID: v.ID, Address: v.Address, Decimals: v.Decimals, Active: v.Active, VoucherType: voucherType,
				}
				rows.Edges = append(rows.Edges, router.EdgeRow{
					PoolID: p.ID, VoucherID: v.ID, Allowed: v.IsAllowed, Limit: v.TokenLimit,
				})
			}
		})
		if err != nil {
			return rows, err
		}
		if (i+1)%20 == 0 {
			logg.Info("catalog progress", "pools", i+1, "of", len(pools))
		}
	}
	for _, t := range tokens {
		rows.Tokens = append(rows.Tokens, t)
	}
	slices.SortFunc(rows.Tokens, func(a, b router.TokenRow) int { return int(a.ID) - int(b.ID) })
	return rows, nil
}

func clone(rows router.StateRows) router.StateRows {
	raw, _ := json.Marshal(rows)
	var out router.StateRows
	_ = json.Unmarshal(raw, &out)
	return out
}

func (m *memStore) snapshot() router.StateRows {
	m.mu.Lock()
	defer m.mu.Unlock()
	return clone(m.rows)
}

func (m *memStore) LoadRouterState(context.Context, int64) (router.StateRows, error) {
	return m.snapshot(), nil
}

func same(a, b string) bool { return a != "" && strings.EqualFold(a, b) }

func (m *memStore) edge(poolID, voucherID uint64) *router.EdgeRow {
	for i := range m.rows.Edges {
		if m.rows.Edges[i].PoolID == poolID && m.rows.Edges[i].VoucherID == voucherID {
			return &m.rows.Edges[i]
		}
	}
	m.rows.Edges = append(m.rows.Edges, router.EdgeRow{PoolID: poolID, VoucherID: voucherID})
	return &m.rows.Edges[len(m.rows.Edges)-1]
}

func (m *memStore) voucher(address common.Address) (uint64, bool) {
	for _, t := range m.rows.Tokens {
		if common.HexToAddress(t.Address) == address {
			return t.ID, true
		}
	}
	return 0, false
}

func (m *memStore) WriteStructure(_ context.Context, _ int64, writes []router.StructureWrite) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, w := range writes {
		voucherID, ok := m.voucher(w.Token)
		if !ok {
			continue
		}
		for _, p := range m.rows.Pools {
			if w.Allowed != nil && same(p.RegistryAddress, w.Registry) {
				m.edge(p.ID, voucherID).Allowed = *w.Allowed
			}
			if w.Limit != nil && same(p.Address, w.Pool) {
				limit := w.Limit.Dec()
				m.edge(p.ID, voucherID).Limit = &limit
			}
			if w.PriceIndex != nil && same(p.QuoterAddress, w.Quoter) {
				price := w.PriceIndex.Dec()
				m.edge(p.ID, voucherID).PriceIndex = &price
			}
		}
	}
	return nil
}

func (m *memStore) WriteLiquidity(_ context.Context, rows []router.LiquidityWrite) ([]router.EdgeKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]router.EdgeKey, 0, len(rows))
	for _, r := range rows {
		e := m.edge(r.Key.PoolID, r.Key.VoucherID)
		balance, fees := r.Balance.Dec(), r.FeesHeld.Dec()
		e.Balance, e.FeesHeld, e.BalanceBlock = &balance, &fees, r.Block
		keys = append(keys, r.Key)
	}
	return keys, nil
}

func addressOrEmpty(a common.Address) string {
	if a == (common.Address{}) {
		return ""
	}
	return a.Hex()
}

func (m *memStore) WritePoolParams(_ context.Context, rows []router.PoolParamsWrite) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range rows {
		for i := range m.rows.Pools {
			p := &m.rows.Pools[i]
			if p.ID != r.PoolID {
				continue
			}
			pp := r.Params
			p.RegistryAddress, p.LimiterAddress = addressOrEmpty(pp.Registry), addressOrEmpty(pp.Limiter)
			p.QuoterAddress, p.FeePolicyAddress = addressOrEmpty(pp.Quoter), addressOrEmpty(pp.FeePolicy)
			p.ControllerAddress, p.FeesDecoupled = addressOrEmpty(pp.Controller), pp.FeesDecoupled
			p.ChainState = &router.PoolChainState{
				DefaultFeePPM: pp.DefaultFeePPM, ProtocolFeePPM: pp.ProtocolFeePPM,
				ProtocolFeeRecipient: addressOrEmpty(pp.ProtocolRecipient), SealState: pp.SealState,
				QuoterVerified: r.QuoterVerified, Block: r.Block,
			}
		}
	}
	return nil
}

func (m *memStore) SetQuoterVerified(_ context.Context, poolID uint64, verified bool, _ uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.rows.Pools {
		if m.rows.Pools[i].ID == poolID && m.rows.Pools[i].ChainState != nil {
			m.rows.Pools[i].ChainState.QuoterVerified = verified
		}
	}
	return nil
}

func retry[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	var (
		v   T
		err error
	)
	for attempt := range 8 {
		if v, err = fn(); err == nil {
			return v, nil
		}
		select {
		case <-ctx.Done():
			return v, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 2 * time.Second):
		}
	}
	return v, err
}

func (p *pinned) Head(context.Context) (router.Head, error) { return p.head, nil }

func (p *pinned) PoolParams(ctx context.Context, _ uint64, pools []common.Address) ([]router.PoolParams, error) {
	return retry(ctx, func() ([]router.PoolParams, error) { return p.ChainReader.PoolParams(ctx, p.head.Number, pools) })
}

func (p *pinned) PoolStructure(ctx context.Context, _ uint64, probes []router.StructureProbe) ([]router.PoolStructure, error) {
	return retry(ctx, func() ([]router.PoolStructure, error) {
		return p.ChainReader.PoolStructure(ctx, p.head.Number, probes)
	})
}

func (p *pinned) Balances(ctx context.Context, _ uint64, probes []router.BalanceProbe) ([]router.BalanceReading, error) {
	return retry(ctx, func() ([]router.BalanceReading, error) { return p.ChainReader.Balances(ctx, p.head.Number, probes) })
}

func (p *pinned) PairFees(ctx context.Context, _ uint64, probes []router.PairProbe) ([]router.PairFeeReading, error) {
	return retry(ctx, func() ([]router.PairFeeReading, error) { return p.ChainReader.PairFees(ctx, p.head.Number, probes) })
}

func (p *pinned) Quotes(ctx context.Context, _ uint64, probes []router.AmountProbe) ([]router.AmountReading, error) {
	return retry(ctx, func() ([]router.AmountReading, error) { return p.ChainReader.Quotes(ctx, p.head.Number, probes) })
}
