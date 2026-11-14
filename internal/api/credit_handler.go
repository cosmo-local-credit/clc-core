package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/router"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/google/uuid"
	"github.com/holiman/uint256"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
	"golang.org/x/time/rate"
)

const (
	// A quote costs several RPC round trips and a simulation.
	creditQuoteRateLimit = rate.Limit(0.5)
	creditQuoteRateBurst = 10

	defaultRouteDeadlineHorizon = time.Hour
	routedSwapPageSize          = 20
)

func (a *API) routeDeadlineHorizonSecs() uint64 {
	if a.routeHorizon <= 0 {
		return uint64(defaultRouteDeadlineHorizon.Seconds())
	}
	return uint64(a.routeHorizon.Seconds())
}

// creditQuoteRateLimit throttles quotes per signed-in user.
func (a *API) creditQuoteRateLimitMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			_, address, _ := getUserClaims(c)
			return a.throttleKey(c, a.creditLimiter, clientKey(address), next)
		}
	}
}

func privateNoStore(c *echo.Context) {
	c.Response().Header().Set("Cache-Control", "private, no-store")
}

// respondRouterError maps the router's typed errors onto the states a client
// must tell apart. An unverified or failed read is never an empty result.
func respondRouterError(c *echo.Context, err error) error {
	var (
		reqErr   *router.RequestError
		routeErr *router.RouteError
		mismatch *router.MismatchError
		sim      *router.SimulationError
		cursor   router.CursorError
	)
	switch {
	case errors.As(err, &reqErr):
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok: false, ErrCode: apiresp.ErrCodeValidationFailed, Description: reqErr.Msg,
		})
	case errors.Is(err, router.ErrUnknownVoucher):
		return c.JSON(http.StatusNotFound, apiresp.ErrResponse{
			Ok: false, ErrCode: apiresp.ErrCodeNotFound, Description: "voucher is not in the catalog",
		})
	case errors.As(err, &routeErr):
		return c.JSON(http.StatusConflict, apiresp.ErrResponse{
			Ok: false, ErrCode: apiresp.ErrCodeRouteUnavailable, Description: "route_unavailable",
			Result: map[string]any{"state": "route_unavailable", "reason": routeErr.Reason, "hop": routeErr.Hop},
		})
	case errors.As(err, &mismatch):
		return c.JSON(http.StatusConflict, apiresp.ErrResponse{
			Ok: false, ErrCode: apiresp.ErrCodeQuoteMismatch, Description: "quote_mismatch",
			Result: map[string]any{"state": "quote_mismatch", "hop": mismatch.Hop, "local": mismatch.Local, "chain": mismatch.Chain},
		})
	case errors.As(err, &sim):
		return c.JSON(http.StatusConflict, apiresp.ErrResponse{
			Ok: false, ErrCode: apiresp.ErrCodeSimulationFailed, Description: "simulation_failed",
			Result: map[string]any{"state": "simulation_failed", "revert": sim.Revert},
		})
	case errors.As(err, &cursor):
		return c.JSON(http.StatusConflict, apiresp.ErrResponse{
			Ok: false, ErrCode: apiresp.ErrCodeCursorStale, Description: cursor.Error(),
		})
	case errors.Is(err, router.ErrUnavailable):
		c.Response().Header().Set("Retry-After", "5")
		return c.JSON(http.StatusServiceUnavailable, apiresp.ErrResponse{
			Ok: false, ErrCode: apiresp.ErrCodeRouterUnavailable, Description: "router_unavailable",
			Result: map[string]any{"state": "router_unavailable"},
		})
	}
	return err
}

func (a *API) routerOrUnavailable(c *echo.Context) (*router.Service, error) {
	if a.creditRouter == nil {
		return nil, respondRouterError(c, router.ErrUnavailable)
	}
	return a.creditRouter, nil
}

func sessionWallet(c *echo.Context) (common.Address, bool) {
	_, address, _ := getUserClaims(c)
	if !common.IsHexAddress(address) {
		return common.Address{}, false
	}
	return common.HexToAddress(address), true
}

func parseDepth(raw string, maxHops int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "max" {
		return maxHops, nil
	}
	depth, err := strconv.Atoi(raw)
	if err != nil || depth < 1 || depth > maxHops {
		return 0, &router.RequestError{Msg: fmt.Sprintf("depth must be max or between 1 and %d", maxHops)}
	}
	return depth, nil
}

func freshnessFields(f router.Freshness) map[string]any {
	return map[string]any{
		"asOfBlock":    f.AsOfBlock,
		"graphVersion": f.GraphVersion,
		"stale":        f.Stale,
	}
}

// creditReachHandler answers GET /users/me/credit: the vouchers and pools the
// session wallet can probably reach within depth hops (the router's maximum by
// default), with the vouchers it already holds at zero hops. An estimate as of
// a block; only a quote is authoritative.
func (a *API) creditReachHandler(c *echo.Context) error {
	privateNoStore(c)
	svc, err := a.routerOrUnavailable(c)
	if svc == nil {
		return err
	}
	wallet, ok := sessionWallet(c)
	if !ok {
		return handleForbiddenError(c, "a wallet session is required")
	}
	depth, err := parseDepth(c.QueryParam("depth"), svc.Config().MaxHops)
	if err != nil {
		return respondRouterError(c, err)
	}
	view, err := svc.Reach(c.Request().Context(), wallet, depth)
	if err != nil {
		return respondRouterError(c, err)
	}
	snap := view.Snap
	inputs := make([]map[string]any, 0, len(view.Result.Inputs))
	for _, h := range view.Result.Inputs {
		inputs = append(inputs, map[string]any{"voucher": snap.Tokens[h.Token].Address.Hex(), "balance": h.Balance.Dec()})
	}
	vouchers := make([]map[string]any, 0, len(view.Result.Vouchers))
	for _, v := range view.Result.Vouchers {
		vouchers = append(vouchers, map[string]any{"address": snap.Tokens[v.Token].Address.Hex(), "minHops": v.MinHops})
	}
	pools := make([]map[string]any, 0, len(view.Result.Pools))
	for _, p := range view.Result.Pools {
		pools = append(pools, map[string]any{"address": snap.Pools[p.Pool].Addr.Hex(), "minHops": p.MinHops})
	}
	result := freshnessFields(view.Freshness)
	result["depth"] = view.Result.Depth
	result["maxHops"] = svc.Config().MaxHops
	result["digest"] = view.Result.Digest.Hex()
	result["inputs"] = inputs
	result["vouchers"] = vouchers
	result["pools"] = pools
	return c.JSON(http.StatusOK, apiresp.OKResponse{Ok: true, Description: "Estimated credit reach", Result: result})
}

func parseAddressParam(raw, name string) (*common.Address, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if !common.IsHexAddress(raw) {
		return nil, &router.RequestError{Msg: name + " must be an address"}
	}
	addr := common.HexToAddress(raw)
	return &addr, nil
}

func parseAmount(raw string) (*uint256.Int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	amount, err := uint256.FromDecimal(raw)
	if err != nil || amount.IsZero() {
		return nil, &router.RequestError{Msg: "amount must be a positive integer in raw token units"}
	}
	return amount, nil
}

func routeHops(snap *router.Snapshot, eval router.RouteEval) []map[string]any {
	hops := make([]map[string]any, len(eval.Route))
	for i, leg := range eval.Route {
		pool := snap.Pools[leg.Pool]
		hops[i] = map[string]any{
			"pool":           map[string]any{"address": pool.Addr.Hex(), "name": pool.Name},
			"tokenIn":        snap.Tokens[leg.In].Address.Hex(),
			"tokenOut":       snap.Tokens[leg.Out].Address.Hex(),
			"feePpm":         strconv.FormatUint(eval.Params[i].FeePPM, 10),
			"feeSource":      eval.FeeSources[i],
			"protocolFeePpm": strconv.FormatUint(eval.Params[i].ProtocolPPM, 10),
		}
	}
	return hops
}

func routeJSON(snap *router.Snapshot, eval router.RouteEval) map[string]any {
	out := map[string]any{
		"id":              eval.ID,
		"hops":            routeHops(snap, eval),
		"effectiveFeePpm": eval.EffectiveFeePPM.Dec(),
		"maxAmountIn":     nil,
		"maxAmountOut":    nil,
		"maxIsExact":      false,
		"limitedBy":       nil,
	}
	if eval.RateKnown {
		out["rate"] = map[string]any{"amountIn": eval.RateIn.Dec(), "amountOut": eval.RateOut.Dec()}
	} else {
		out["rate"] = nil
	}
	if eval.Max.Known {
		out["maxAmountIn"] = eval.Max.AmountIn.Dec()
		out["maxAmountOut"] = eval.Max.AmountOut.Dec()
		out["maxIsExact"] = eval.Max.Exact
		out["limitedBy"] = eval.Max.LimitedBy
		out["limitedByHop"] = eval.Max.LimitedByHop
	}
	if eval.HasAmount {
		out["carriesAmount"] = eval.CarriesAmount
		if eval.CarriesAmount {
			out["amountIn"] = eval.AmountIn.Dec()
			out["amountOut"] = eval.AmountOut.Dec()
		} else if eval.Refusal != nil {
			out["refusal"] = map[string]any{"reason": eval.Refusal.Reason, "hop": eval.Refusal.Hop}
		}
	}
	return out
}

// creditRoutesHandler answers GET /users/me/credit/routes: routes from the
// wallet's vouchers to one voucher, grouped by input voucher and ranked within
// each group. Paged by cursor and never silently truncated.
func (a *API) creditRoutesHandler(c *echo.Context) error {
	privateNoStore(c)
	svc, err := a.routerOrUnavailable(c)
	if svc == nil {
		return err
	}
	wallet, ok := sessionWallet(c)
	if !ok {
		return handleForbiddenError(c, "a wallet session is required")
	}
	q := c.Request().URL.Query()
	target, err := parseAddressParam(q.Get("to"), "to")
	if err != nil {
		return respondRouterError(c, err)
	}
	if target == nil {
		return respondRouterError(c, &router.RequestError{Msg: "to is required"})
	}
	from, err := parseAddressParam(q.Get("from"), "from")
	if err != nil {
		return respondRouterError(c, err)
	}
	depth, err := parseDepth(q.Get("depth"), svc.Config().MaxHops)
	if err != nil {
		return respondRouterError(c, err)
	}
	amount, err := parseAmount(q.Get("amount"))
	if err != nil {
		return respondRouterError(c, err)
	}
	view, err := svc.Routes(c.Request().Context(), wallet, router.RoutesRequest{
		Target: *target, Depth: depth, From: from, Mode: router.Mode(q.Get("mode")), Amount: amount,
		Cursor: q.Get("cursor"),
	})
	if err != nil {
		return respondRouterError(c, err)
	}
	snap := view.Snap
	targetToken := snap.Tokens[view.Target]
	inputs := make([]map[string]any, 0, len(view.Groups))
	for _, g := range view.Groups {
		routes := make([]map[string]any, 0, len(g.Routes))
		for _, r := range g.Routes {
			routes = append(routes, routeJSON(snap, r))
		}
		inputs = append(inputs, map[string]any{
			"voucher":       snap.Tokens[g.Input.Token].Address.Hex(),
			"walletBalance": g.Input.Balance.Dec(),
			"routes":        routes,
			"omitted":       g.Omitted,
		})
	}
	fallback := make([]map[string]any, 0, len(view.Fallback))
	for _, fp := range view.Fallback {
		accepts := make([]string, 0, len(fp.Accepts))
		for _, t := range fp.Accepts {
			accepts = append(accepts, snap.Tokens[t].Address.Hex())
		}
		fallback = append(fallback, map[string]any{
			"address": snap.Pools[fp.Pool].Addr.Hex(), "name": snap.Pools[fp.Pool].Name,
			"liquidity": fp.Liquidity.Dec(), "accepts": accepts,
		})
	}
	result := freshnessFields(view.Freshness)
	result["target"] = map[string]any{
		"address": targetToken.Address.Hex(), "symbol": targetToken.Symbol, "decimals": targetToken.Decimals,
	}
	result["complete"] = view.Complete
	result["omitted"] = view.Omitted
	result["inputs"] = inputs
	result["fallbackPools"] = fallback
	if view.NextCursor != "" {
		result["cursor"] = view.NextCursor
	} else {
		result["cursor"] = nil
	}
	return c.JSON(http.StatusOK, apiresp.OKResponse{Ok: true, Description: "Credit routes", Result: result})
}

// creditQuoteHandler answers POST /users/me/credit/quote with an
// authoritative quote and a directly signable batch. Nothing is stored; the
// digest is the quote id.
func (a *API) creditQuoteHandler(c *echo.Context) error {
	privateNoStore(c)
	svc, err := a.routerOrUnavailable(c)
	if svc == nil {
		return err
	}
	wallet, ok := sessionWallet(c)
	if !ok {
		return handleForbiddenError(c, "a wallet session is required")
	}
	req := apiresp.CreditQuoteRequest{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}
	amount, err := parseAmount(req.Amount)
	if err != nil {
		return respondRouterError(c, err)
	}
	quoteReq := router.QuoteRequest{Mode: router.Mode(req.Mode), Amount: *amount}
	for _, h := range req.Route {
		quoteReq.Hops = append(quoteReq.Hops, router.HopRef{
			Pool: common.HexToAddress(h.Pool), TokenIn: common.HexToAddress(h.TokenIn), TokenOut: common.HexToAddress(h.TokenOut),
		})
	}
	if req.Recipient != nil {
		recipient := common.HexToAddress(*req.Recipient)
		quoteReq.Recipient = &recipient
	}
	if req.KeyHash != "" {
		quoteReq.KeyHash = common.HexToHash(req.KeyHash)
	}

	q, err := svc.Quote(c.Request().Context(), wallet, quoteReq)
	if err != nil {
		return respondRouterError(c, err)
	}
	return c.JSON(http.StatusOK, apiresp.OKResponse{Ok: true, Description: "Credit quote", Result: quoteJSON(q)})
}

func quoteJSON(q router.QuoteView) map[string]any {
	hops := make([]map[string]any, len(q.Quote.Hops))
	for i, hop := range q.Quote.Hops {
		minOut := q.MinAmountOut
		if i+1 < len(q.Quote.Hops) {
			minOut = q.Quote.Inputs[i+1]
		}
		hops[i] = map[string]any{
			"pool":           q.Hops[i].Pool.Hex(),
			"tokenIn":        q.Hops[i].TokenIn.Hex(),
			"tokenOut":       q.Hops[i].TokenOut.Hex(),
			"amountIn":       q.Quote.Inputs[i].Dec(),
			"grossOut":       hop.Gross.Dec(),
			"poolFee":        hop.PoolFee.Dec(),
			"protocolFee":    hop.ProtocolFee.Dec(),
			"netOut":         hop.Net.Dec(),
			"minAmountOut":   minOut.Dec(),
			"feePpm":         strconv.FormatUint(q.Legs[i].FeePPM, 10),
			"protocolFeePpm": strconv.FormatUint(q.Legs[i].ProtocolPPM, 10),
		}
	}
	bridge := make([]map[string]any, 0, len(q.Quote.Hops))
	for i, surplus := range q.Quote.BridgeSurplus() {
		bridge = append(bridge, map[string]any{"voucher": q.Hops[i].TokenOut.Hex(), "amount": surplus.Dec()})
	}
	calls := make([]map[string]any, len(q.Batch.Calls))
	for i, call := range q.Batch.Calls {
		calls[i] = map[string]any{"to": call.To.Hex(), "value": call.Value.String(), "data": hexutil.Encode(call.Data)}
	}
	amountIn, amountOut, surplus := q.Quote.AmountIn(), q.Quote.AmountOut(), q.OutputSurplus()
	sim := map[string]any{"ok": q.Simulation.OK, "gasEstimate": nil}
	if q.Simulation.GasEstimate > 0 {
		sim["gasEstimate"] = strconv.FormatUint(q.Simulation.GasEstimate, 10)
	}
	return map[string]any{
		"quoteId":      q.Digest.Hex(),
		"block":        map[string]any{"number": q.Block.Number, "hash": q.Block.Hash.Hex()},
		"expiresAt":    q.ExpiresAt,
		"mode":         q.Mode,
		"amountIn":     amountIn.Dec(),
		"amountOut":    amountOut.Dec(),
		"minAmountOut": q.MinAmountOut.Dec(),
		"recipient":    q.Recipient.Hex(),
		"hops":         hops,
		"fees":         map[string]any{"feeFreeAmountOut": q.FeeFree.Dec(), "effectiveFeePpm": q.EffectiveFeePPM.Dec()},
		"surplus":      map[string]any{"output": surplus.Dec(), "bridge": bridge},
		"batch": map[string]any{
			"batchedCall": map[string]any{"calls": calls, "revertOnFailure": q.Batch.RevertOnFailure},
			"nonce":       q.Batch.Nonce.String(),
			"keyHash":     q.Batch.KeyHash.Hex(),
			"executor":    q.Batch.Executor.Hex(),
			"deadline":    strconv.FormatUint(q.Batch.Deadline, 10),
			"digest":      q.Digest.Hex(),
		},
		"simulation": sim,
		"delegated":  q.Delegated,
		"eoaNonce":   strconv.FormatUint(q.EOANonce, 10),
	}
}

// routedSwapHandler answers GET /users/me/routed-swaps/:trackingId with the
// grouped receipt of one routed exchange. Another wallet's id reads as not
// found.
func (a *API) routedSwapHandler(c *echo.Context) error {
	privateNoStore(c)
	wallet, ok := sessionWallet(c)
	if !ok {
		return handleForbiddenError(c, "a wallet session is required")
	}
	trackingID := c.Param("trackingId")
	if _, err := uuid.Parse(trackingID); err != nil {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok: false, ErrCode: apiresp.ErrCodeValidationFailed, Description: "trackingId must be a UUID",
		})
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	swap, err := a.store.GetRoutedSwap(ctx, tx, trackingID, wallet.Hex())
	if errors.Is(err, pgx.ErrNoRows) {
		return c.JSON(http.StatusNotFound, apiresp.ErrResponse{
			Ok: false, ErrCode: apiresp.ErrCodeNotFound, Description: "routed swap not found",
		})
	}
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}
	return c.JSON(http.StatusOK, apiresp.OKResponse{Ok: true, Description: "Routed swap", Result: receiptJSON(swap)})
}

// receiptJSON adds what a reader wants from a receipt: the bridge remainder
// each intermediate hop left in the wallet once both hops settled.
func receiptJSON(swap store.RoutedSwap) map[string]any {
	remainders := []map[string]any{}
	for i := 0; i+1 < len(swap.Hops); i++ {
		out, next := swap.Hops[i].AmountOut, swap.Hops[i+1].SettledAmountIn
		if out == nil || next == nil {
			continue
		}
		o, err1 := uint256.FromDecimal(*out)
		n, err2 := uint256.FromDecimal(*next)
		if err1 != nil || err2 != nil || o.Lt(n) {
			continue
		}
		remainders = append(remainders, map[string]any{
			"voucher": swap.Hops[i].TokenOut, "amount": new(uint256.Int).Sub(o, n).Dec(),
		})
	}
	settled := len(swap.Hops) > 0
	for _, h := range swap.Hops {
		settled = settled && h.SettledAt != nil
	}
	return map[string]any{"swap": swap, "settled": settled, "bridgeRemainder": remainders}
}

type (
	routedSwapCursor struct {
		CreatedAt  time.Time `json:"c"`
		TrackingID string    `json:"t"`
	}
)

// routedSwapsListHandler answers GET /users/me/routed-swaps, newest first, so
// Activity can show a routed exchange as one entry with its hops.
func (a *API) routedSwapsListHandler(c *echo.Context) error {
	privateNoStore(c)
	wallet, ok := sessionWallet(c)
	if !ok {
		return handleForbiddenError(c, "a wallet session is required")
	}
	var cursor *store.RoutedSwapCursor
	if raw := c.QueryParam("cursor"); raw != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(raw)
		var parsed routedSwapCursor
		if err != nil || json.Unmarshal(decoded, &parsed) != nil || parsed.TrackingID == "" {
			return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
				Ok: false, ErrCode: apiresp.ErrCodeValidationFailed, Description: "invalid cursor",
			})
		}
		cursor = &store.RoutedSwapCursor{CreatedAt: parsed.CreatedAt, TrackingID: parsed.TrackingID}
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	swaps, err := a.store.ListRoutedSwaps(ctx, tx, wallet.Hex(), cursor, routedSwapPageSize+1)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}
	var next any
	if len(swaps) > routedSwapPageSize {
		swaps = swaps[:routedSwapPageSize]
		last := swaps[len(swaps)-1]
		encoded, _ := json.Marshal(routedSwapCursor{CreatedAt: last.CreatedAt, TrackingID: last.TrackingID})
		next = base64.RawURLEncoding.EncodeToString(encoded)
	}
	items := make([]map[string]any, len(swaps))
	for i, s := range swaps {
		items[i] = receiptJSON(s)
	}
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok: true, Description: "Routed swaps", Result: map[string]any{"items": items, "cursor": next},
	})
}

// adminRouterStatsHandler reports graph counts, freshness and loop health.
func (a *API) adminRouterStatsHandler(c *echo.Context) error {
	svc, err := a.routerOrUnavailable(c)
	if svc == nil {
		return err
	}
	stats, err := svc.Stats()
	if err != nil {
		return respondRouterError(c, err)
	}
	return c.JSON(http.StatusOK, apiresp.OKResponse{Ok: true, Description: "Router stats", Result: map[string]any{"stats": stats}})
}

// adminRouterGraphHandler exports the graph for inspection.
func (a *API) adminRouterGraphHandler(c *echo.Context) error {
	svc, err := a.routerOrUnavailable(c)
	if svc == nil {
		return err
	}
	snap := svc.Snapshot()
	if snap == nil {
		return respondRouterError(c, router.ErrUnavailable)
	}
	pools := make([]map[string]any, 0, len(snap.Pools))
	for i, p := range snap.Pools {
		edges := make([]map[string]any, 0, len(snap.PoolEdges(int32(i))))
		for _, id := range snap.PoolEdges(int32(i)) {
			e := snap.Edges[id]
			edge := map[string]any{
				"voucher": snap.Tokens[e.Token].Address.Hex(), "symbol": snap.Tokens[e.Token].Symbol,
				"allowed": e.Allowed, "limit": e.Limit.Dec(), "priceIndex": nil,
				"balance": nil, "feesHeld": nil, "balanceBlock": e.BalanceBlock,
			}
			if e.PriceKnown {
				edge["priceIndex"] = e.PriceIndex.Dec()
			}
			if e.HasBalance {
				edge["balance"], edge["feesHeld"] = e.Balance.Dec(), e.FeesHeld.Dec()
			}
			edges = append(edges, edge)
		}
		pools = append(pools, map[string]any{
			"address": p.Addr.Hex(), "name": p.Name, "active": p.Active, "quoter": p.Quoter.String(),
			"verified": p.Verified, "paramsKnown": p.ParamsKnown, "defaultFeePpm": p.DefaultFeePPM,
			"protocolFeePpm": p.ProtocolPPM, "sealState": p.SealState, "paramsBlock": p.ParamsBlock,
			"edges": edges,
		})
	}
	return c.JSON(http.StatusOK, apiresp.OKResponse{Ok: true, Description: "Router graph", Result: map[string]any{
		"graphVersion": snap.Version, "asOfBlock": snap.Block, "legs": snap.LegCount(), "pools": pools,
	}})
}
