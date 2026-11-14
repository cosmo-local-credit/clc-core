package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/router"
	"github.com/ethereum/go-ethereum/common"
	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
)

type (
	// RouterStore is the router's persistence: the ordered pool composition
	// projection, the chain-read state the reconcilers write, and route receipts.
	RouterStore interface {
		router.StateStore
		SetPoolTokenPriceIndex(ctx context.Context, tx pgx.Tx, quoter, token, value string, pos router.Position) ([]router.EdgeKey, error)
		InsertRoutedSwap(ctx context.Context, tx pgx.Tx, swap RoutedSwapRecord) error
		SettleRoutedSwapHop(ctx context.Context, tx pgx.Tx, trackingID string, settlement RoutedSwapSettlement) (bool, error)
		GetRoutedSwap(ctx context.Context, tx pgx.Tx, trackingID, wallet string) (RoutedSwap, error)
		ListRoutedSwaps(ctx context.Context, tx pgx.Tx, wallet string, cursor *RoutedSwapCursor, limit int) ([]RoutedSwap, error)
	}

	routerPoolRow struct {
		ID                int64   `db:"id"`
		Address           string  `db:"address"`
		Name              string  `db:"name"`
		Active            bool    `db:"active"`
		QuoterType        string  `db:"quoter_type"`
		RegistryAddress   string  `db:"registry_address"`
		LimiterAddress    string  `db:"limiter_address"`
		QuoterAddress     string  `db:"quoter_address"`
		FeePolicyAddress  string  `db:"fee_policy_address"`
		ControllerAddress string  `db:"controller_address"`
		FeesDecoupled     bool    `db:"fees_decoupled"`
		DefaultFeePPM     *int64  `db:"default_fee_ppm"`
		ProtocolFeePPM    *int64  `db:"protocol_fee_ppm"`
		ProtocolRecipient *string `db:"protocol_fee_recipient"`
		SealState         *int16  `db:"seal_state"`
		QuoterVerified    *bool   `db:"quoter_verified"`
		ParamsBlock       *int64  `db:"params_block"`
	}

	routerVoucherRow struct {
		ID          int64  `db:"id"`
		Address     string `db:"address"`
		Symbol      string `db:"symbol"`
		Name        string `db:"name"`
		Decimals    *int   `db:"decimals"`
		Active      bool   `db:"active"`
		VoucherType string `db:"voucher_type"`
	}

	routerEdgeRow struct {
		PoolID          int64   `db:"pool_id"`
		VoucherID       int64   `db:"voucher_id"`
		IsAllowed       bool    `db:"is_allowed"`
		TokenLimit      *string `db:"token_limit"`
		PriceIndex      *string `db:"price_index"`
		AllowedBlock    int64   `db:"allowed_block"`
		AllowedLogIndex int32   `db:"allowed_log_index"`
		LimitBlock      int64   `db:"limit_block"`
		LimitLogIndex   int32   `db:"limit_log_index"`
		PriceBlock      int64   `db:"price_block"`
		PriceLogIndex   int32   `db:"price_log_index"`
		Balance         *string `db:"balance"`
		FeesHeld        *string `db:"fees_held"`
		BalanceBlock    int64   `db:"balance_block"`
	}

	// RoutedSwapRecord is a route recorded from signed calldata when the relay
	// accepts it.
	RoutedSwapRecord struct {
		TrackingID   string
		ChainID      int64
		Wallet       string
		Recipient    string
		TokenIn      string
		TokenOut     string
		AmountIn     string
		MinAmountOut string
		Bounded      bool
		Deadline     *uint64
		Hops         []RoutedSwapHopRecord
	}

	RoutedSwapHopRecord struct {
		Pool         string
		TokenIn      string
		TokenOut     string
		AmountIn     string
		MinAmountOut string
		Recipient    string
	}

	// RoutedSwapSettlement is one hop's settlement event.
	RoutedSwapSettlement struct {
		Pool             string
		TokenIn          string
		TokenOut         string
		TxHash           string
		Block            uint64
		LogIndex         uint32
		AmountIn         string
		QuotedAmountOut  *string
		NominalAmountOut *string
		AmountOut        string
		PoolFee          string
		ProtocolFee      *string
	}

	// RoutedSwap is a receipt.
	RoutedSwap struct {
		TrackingID   string          `db:"tracking_id"    json:"trackingId"`
		ChainID      int64           `db:"chain_id"       json:"chainId"`
		Wallet       string          `db:"wallet"         json:"wallet"`
		Recipient    string          `db:"recipient"      json:"recipient"`
		TokenIn      string          `db:"token_in"       json:"tokenIn"`
		TokenOut     string          `db:"token_out"      json:"tokenOut"`
		AmountIn     string          `db:"amount_in"      json:"amountIn"`
		MinAmountOut string          `db:"min_amount_out" json:"minAmountOut"`
		HopCount     int16           `db:"hop_count"      json:"hopCount"`
		Bounded      bool            `db:"bounded"        json:"bounded"`
		Deadline     *int64          `db:"deadline"       json:"deadline"`
		CreatedAt    time.Time       `db:"created_at"     json:"createdAt"`
		TxHash       *string         `db:"tx_hash"        json:"txHash"`
		Status       *string         `db:"status"         json:"status"`
		Hops         []RoutedSwapHop `db:"-"              json:"hops"`
	}

	RoutedSwapHop struct {
		TrackingID       string     `db:"tracking_id"        json:"-"`
		HopIndex         int16      `db:"hop_index"          json:"index"`
		Pool             string     `db:"pool_address"       json:"pool"`
		TokenIn          string     `db:"token_in"           json:"tokenIn"`
		TokenOut         string     `db:"token_out"          json:"tokenOut"`
		AmountIn         string     `db:"amount_in"          json:"amountIn"`
		MinAmountOut     string     `db:"min_amount_out"     json:"minAmountOut"`
		Recipient        string     `db:"recipient"          json:"recipient"`
		TxHash           *string    `db:"tx_hash"            json:"txHash"`
		Block            *int64     `db:"block_number"       json:"block"`
		LogIndex         *int32     `db:"log_index"          json:"logIndex"`
		SettledAmountIn  *string    `db:"settled_amount_in"  json:"settledAmountIn"`
		QuotedAmountOut  *string    `db:"quoted_amount_out"  json:"quotedAmountOut"`
		NominalAmountOut *string    `db:"nominal_amount_out" json:"nominalAmountOut"`
		AmountOut        *string    `db:"amount_out"         json:"amountOut"`
		PoolFee          *string    `db:"pool_fee"           json:"poolFee"`
		ProtocolFee      *string    `db:"protocol_fee"       json:"protocolFee"`
		SettledAt        *time.Time `db:"settled_at"         json:"settledAt"`
	}

	// RoutedSwapCursor pages a wallet's receipts, newest first.
	RoutedSwapCursor struct {
		CreatedAt  time.Time
		TrackingID string
	}
)

func scanEdgeKeys(rows pgx.Rows) ([]router.EdgeKey, error) {
	defer rows.Close()
	var keys []router.EdgeKey
	for rows.Next() {
		var poolID, voucherID int64
		if err := rows.Scan(&poolID, &voucherID); err != nil {
			return nil, err
		}
		keys = append(keys, router.EdgeKey{PoolID: uint64(poolID), VoucherID: uint64(voucherID)})
	}
	return keys, rows.Err()
}

func (pg *Pg) projectEdges(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]router.EdgeKey, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return scanEdgeKeys(rows)
}

// SetPoolTokenPriceIndex records a RelativeQuoter price index for every pool
// using that quoter, when pos is newer than the last write.
func (pg *Pg) SetPoolTokenPriceIndex(
	ctx context.Context, tx pgx.Tx, quoterAddress, tokenAddress, value string, pos router.Position,
) ([]router.EdgeKey, error) {
	return pg.projectEdges(ctx, tx, pg.queries.SetPoolTokenPriceIndex,
		quoterAddress, tokenAddress, value, int64(pos.Block), int64(pos.LogIndex))
}

func position(block int64, logIndex int32) router.Position {
	if block <= 0 {
		return router.Position{}
	}
	return router.Position{Block: uint64(block), LogIndex: uint32(logIndex)}
}

// LoadRouterState reads every pool, voucher and edge of a chain in one
// consistent snapshot.
func (pg *Pg) LoadRouterState(ctx context.Context, chainID int64) (router.StateRows, error) {
	tx, err := pg.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return router.StateRows{}, err
	}
	defer tx.Rollback(ctx)

	var pools []routerPoolRow
	if err := pgxscan.Select(ctx, tx, &pools, pg.queries.LoadRouterPools, chainID); err != nil {
		return router.StateRows{}, fmt.Errorf("load router pools: %w", err)
	}
	var vouchers []routerVoucherRow
	if err := pgxscan.Select(ctx, tx, &vouchers, pg.queries.LoadRouterVouchers, chainID); err != nil {
		return router.StateRows{}, fmt.Errorf("load router vouchers: %w", err)
	}
	var edges []routerEdgeRow
	if err := pgxscan.Select(ctx, tx, &edges, pg.queries.LoadRouterEdges, chainID); err != nil {
		return router.StateRows{}, fmt.Errorf("load router edges: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return router.StateRows{}, err
	}

	out := router.StateRows{
		Pools:  make([]router.PoolRow, 0, len(pools)),
		Tokens: make([]router.TokenRow, 0, len(vouchers)),
		Edges:  make([]router.EdgeRow, 0, len(edges)),
	}
	for _, p := range pools {
		row := router.PoolRow{
			ID: uint64(p.ID), Address: p.Address, Name: p.Name, Active: p.Active, QuoterType: p.QuoterType,
			RegistryAddress: p.RegistryAddress, LimiterAddress: p.LimiterAddress, QuoterAddress: p.QuoterAddress,
			FeePolicyAddress: p.FeePolicyAddress, ControllerAddress: p.ControllerAddress, FeesDecoupled: p.FeesDecoupled,
		}
		if p.ParamsBlock != nil && p.DefaultFeePPM != nil && p.ProtocolFeePPM != nil && p.SealState != nil && p.QuoterVerified != nil {
			cs := &router.PoolChainState{
				DefaultFeePPM:  uint64(*p.DefaultFeePPM),
				ProtocolFeePPM: uint64(*p.ProtocolFeePPM),
				SealState:      uint8(*p.SealState),
				QuoterVerified: *p.QuoterVerified,
				Block:          uint64(*p.ParamsBlock),
			}
			if p.ProtocolRecipient != nil {
				cs.ProtocolFeeRecipient = *p.ProtocolRecipient
			}
			row.ChainState = cs
		}
		out.Pools = append(out.Pools, row)
	}
	for _, v := range vouchers {
		out.Tokens = append(out.Tokens, router.TokenRow{
			ID: uint64(v.ID), Address: v.Address, Symbol: v.Symbol, Name: v.Name,
			Decimals: v.Decimals, Active: v.Active, VoucherType: v.VoucherType,
		})
	}
	for _, e := range edges {
		out.Edges = append(out.Edges, router.EdgeRow{
			PoolID: uint64(e.PoolID), VoucherID: uint64(e.VoucherID), Allowed: e.IsAllowed,
			Limit: e.TokenLimit, PriceIndex: e.PriceIndex,
			AllowedPos: position(e.AllowedBlock, e.AllowedLogIndex),
			LimitPos:   position(e.LimitBlock, e.LimitLogIndex),
			PricePos:   position(e.PriceBlock, e.PriceLogIndex),
			Balance:    e.Balance, FeesHeld: e.FeesHeld, BalanceBlock: uint64(e.BalanceBlock),
		})
	}
	return out, nil
}

// WriteStructure applies chain-read membership, limits and price indexes
// through the same ordered statements the events use. Membership is written
// first, so a newly found member's row exists before its cap and price.
func (pg *Pg) WriteStructure(ctx context.Context, _ int64, writes []router.StructureWrite) error {
	tx, err := pg.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, w := range writes {
		token := w.Token.Hex()
		if w.Allowed != nil && w.Registry != "" {
			if _, err := pg.SetPoolTokenMembership(ctx, tx, w.Registry, token, *w.Allowed, w.Pos); err != nil {
				return fmt.Errorf("membership %s %s: %w", w.Registry, token, err)
			}
		}
		if w.Limit != nil {
			if _, err := pg.SetPoolTokenLimit(ctx, tx, w.Pool, token, w.Limit.Dec(), w.Pos); err != nil {
				return fmt.Errorf("limit %s %s: %w", w.Pool, token, err)
			}
		}
		if w.PriceIndex != nil && w.Quoter != "" {
			if _, err := pg.SetPoolTokenPriceIndex(ctx, tx, w.Quoter, token, w.PriceIndex.Dec(), w.Pos); err != nil {
				return fmt.Errorf("price index %s %s: %w", w.Quoter, token, err)
			}
		}
	}
	return tx.Commit(ctx)
}

// WriteLiquidity upserts chain-read balances, keeping the newer of two reads,
// and returns the rows that changed.
func (pg *Pg) WriteLiquidity(ctx context.Context, rows []router.LiquidityWrite) ([]router.EdgeKey, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	poolIDs := make([]int64, len(rows))
	voucherIDs := make([]int64, len(rows))
	balances := make([]string, len(rows))
	fees := make([]string, len(rows))
	blocks := make([]int64, len(rows))
	for i, r := range rows {
		poolIDs[i], voucherIDs[i] = int64(r.Key.PoolID), int64(r.Key.VoucherID)
		balances[i], fees[i] = r.Balance.Dec(), r.FeesHeld.Dec()
		blocks[i] = int64(r.Block)
	}
	tx, err := pg.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	keys, err := pg.projectEdges(ctx, tx, pg.queries.UpsertPoolTokenLiquidity, poolIDs, voucherIDs, balances, fees, blocks)
	if err != nil {
		return nil, err
	}
	return keys, tx.Commit(ctx)
}

func addressOrEmpty(a common.Address) string {
	if a == (common.Address{}) {
		return ""
	}
	return a.Hex()
}

// WritePoolParams records each pool's chain-read parameters and corrects its
// periphery pointers when the chain disagrees.
func (pg *Pg) WritePoolParams(ctx context.Context, rows []router.PoolParamsWrite) error {
	tx, err := pg.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, r := range rows {
		p := r.Params
		if _, err := tx.Exec(ctx, pg.queries.UpsertPoolChainState, int64(r.PoolID),
			int64(p.DefaultFeePPM), int64(p.ProtocolFeePPM), addressOrEmpty(p.ProtocolRecipient),
			int16(p.SealState), r.QuoterVerified, int64(r.Block)); err != nil {
			return fmt.Errorf("pool chain state %d: %w", r.PoolID, err)
		}
		if _, err := tx.Exec(ctx, pg.queries.CorrectPoolPointers, int64(r.PoolID),
			addressOrEmpty(p.Registry), addressOrEmpty(p.Limiter), addressOrEmpty(p.Quoter),
			addressOrEmpty(p.FeePolicy), addressOrEmpty(p.Controller), p.FeesDecoupled); err != nil {
			return fmt.Errorf("pool pointers %d: %w", r.PoolID, err)
		}
	}
	return tx.Commit(ctx)
}

// SetQuoterVerified flips a pool's verification, unless a newer refresh has
// already written it.
func (pg *Pg) SetQuoterVerified(ctx context.Context, poolID uint64, verified bool, block uint64) error {
	_, err := pg.db.Exec(ctx, pg.queries.SetPoolQuoterVerified, int64(poolID), verified, int64(block))
	return err
}

// InsertRoutedSwap records a route in the relay's transaction.
func (pg *Pg) InsertRoutedSwap(ctx context.Context, tx pgx.Tx, swap RoutedSwapRecord) error {
	var deadline *int64
	if swap.Deadline != nil {
		d := int64(*swap.Deadline)
		deadline = &d
	}
	if _, err := tx.Exec(ctx, pg.queries.InsertRoutedSwap, swap.TrackingID, swap.ChainID, swap.Wallet,
		swap.Recipient, swap.TokenIn, swap.TokenOut, swap.AmountIn, swap.MinAmountOut,
		int16(len(swap.Hops)), swap.Bounded, deadline); err != nil {
		return err
	}
	for i, h := range swap.Hops {
		if _, err := tx.Exec(ctx, pg.queries.InsertRoutedSwapHop, swap.TrackingID, int16(i), h.Pool,
			h.TokenIn, h.TokenOut, h.AmountIn, h.MinAmountOut, h.Recipient); err != nil {
			return err
		}
	}
	return nil
}

// SettleRoutedSwapHop fills the matching hop of a tracked route from its
// settlement event. It reports whether a hop matched.
func (pg *Pg) SettleRoutedSwapHop(ctx context.Context, tx pgx.Tx, trackingID string, s RoutedSwapSettlement) (bool, error) {
	var hop int16
	err := tx.QueryRow(ctx, pg.queries.SettleRoutedSwapHop, trackingID, s.Pool, s.TokenIn, s.TokenOut,
		s.TxHash, int64(s.Block), int64(s.LogIndex), s.AmountIn, s.QuotedAmountOut, s.NominalAmountOut,
		s.AmountOut, s.PoolFee, s.ProtocolFee).Scan(&hop)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (pg *Pg) attachRoutedSwapHops(ctx context.Context, tx pgx.Tx, swaps []RoutedSwap) error {
	if len(swaps) == 0 {
		return nil
	}
	ids := make([]string, len(swaps))
	index := make(map[string]int, len(swaps))
	for i, s := range swaps {
		ids[i] = s.TrackingID
		index[strings.ToLower(s.TrackingID)] = i
		swaps[i].Hops = []RoutedSwapHop{}
	}
	var hops []RoutedSwapHop
	if err := pgxscan.Select(ctx, tx, &hops, pg.queries.ListRoutedSwapHops, ids); err != nil {
		return err
	}
	for _, h := range hops {
		if i, ok := index[strings.ToLower(h.TrackingID)]; ok {
			swaps[i].Hops = append(swaps[i].Hops, h)
		}
	}
	return nil
}

// GetRoutedSwap returns a wallet's receipt; another wallet's tracking id
// reads as not found.
func (pg *Pg) GetRoutedSwap(ctx context.Context, tx pgx.Tx, trackingID, wallet string) (RoutedSwap, error) {
	var swaps []RoutedSwap
	if err := pgxscan.Select(ctx, tx, &swaps, pg.queries.GetRoutedSwap, trackingID, wallet); err != nil {
		return RoutedSwap{}, err
	}
	if len(swaps) == 0 {
		return RoutedSwap{}, pgx.ErrNoRows
	}
	if err := pg.attachRoutedSwapHops(ctx, tx, swaps); err != nil {
		return RoutedSwap{}, err
	}
	return swaps[0], nil
}

// ListRoutedSwaps pages a wallet's receipts, newest first.
func (pg *Pg) ListRoutedSwaps(ctx context.Context, tx pgx.Tx, wallet string, cursor *RoutedSwapCursor, limit int) ([]RoutedSwap, error) {
	var (
		createdAt *time.Time
		after     *string
	)
	if cursor != nil {
		createdAt, after = &cursor.CreatedAt, &cursor.TrackingID
	}
	var swaps []RoutedSwap
	if err := pgxscan.Select(ctx, tx, &swaps, pg.queries.ListRoutedSwaps, wallet, createdAt, after, limit); err != nil {
		return nil, err
	}
	if err := pg.attachRoutedSwapHops(ctx, tx, swaps); err != nil {
		return nil, err
	}
	return swaps, nil
}
