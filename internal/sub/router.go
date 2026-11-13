package sub

import (
	"context"
	"encoding/json"
	"math"

	"github.com/cosmo-local-credit/clc-core/internal/router"
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/ethereum/go-ethereum/common"
	"github.com/grassrootseconomics/eth-tracker/pkg/event"
	"github.com/holiman/uint256"
	"github.com/jackc/pgx/v5"
)

type (
	// RouterSink receives graph deltas after the event's transaction commits.
	RouterSink interface {
		Apply(deltas ...router.Delta)
	}
)

// Chain events the router listens to beyond pool composition.
const (
	quoterPriceEvent   = "QUOTER_PRICE_INDEX_UPDATED"
	poolSwapEvent      = "POOL_SWAP"
	poolDepositEvent   = "POOL_DEPOSIT"
	tokenTransferEvent = "TOKEN_TRANSFER"
	tokenMintEvent     = "TOKEN_MINT"
	tokenBurnEvent     = "TOKEN_BURN"
	sealEvent          = "SEAL_STATE_CHANGE"
	proxyUpgradeEvent  = "PROXY_UPGRADE"
	quoterUpdatedEvent = "QUOTER_UPDATED"

	// FeePolicy and ProtocolFeeController events (eth-tracker).
	feeDefaultUpdatedEvent  = "FEE_DEFAULT_UPDATED"
	feePairUpdatedEvent     = "FEE_PAIR_UPDATED"
	feePairRemovedEvent     = "FEE_PAIR_REMOVED"
	protocolFeeUpdatedEvent = "PROTOCOL_FEE_UPDATED"
	protocolRecipientEvent  = "PROTOCOL_FEE_RECIPIENT_UPDATED"
	protocolFeeActiveEvent  = "PROTOCOL_FEE_ACTIVE_UPDATED"
)

// decodeEvent reads a tracker message. The pinned eth-tracker module drops
// the log index (json:"-"), while the tracker sends it; it is read separately
// so projections can order events within a block.
func decodeEvent(msg []byte) (event.Event, error) {
	var evt event.Event
	if err := json.Unmarshal(msg, &evt); err != nil {
		return evt, err
	}
	var position struct {
		Index *uint64 `json:"index"`
	}
	if err := json.Unmarshal(msg, &position); err == nil && position.Index != nil {
		evt.Index = uint(*position.Index)
	}
	return evt, nil
}

// eventPosition is where an event sits on chain. A log index beyond the
// column's range (the contract-creation sentinel) is clamped below the
// reconciliation position.
func eventPosition(evt event.Event) router.Position {
	index := uint64(evt.Index)
	if index >= router.ReconcileLogIndex {
		index = router.ReconcileLogIndex - 1
	}
	if index > math.MaxUint32 {
		index = math.MaxUint32
	}
	return router.Position{Block: evt.Block, LogIndex: uint32(index)}
}

// routerSignals are the deltas an event implies that need no database write:
// balances to re-read and pools to refresh.
func routerSignals(evt event.Event) []router.Delta {
	if !evt.Success || !common.IsHexAddress(evt.ContractAddress) {
		return nil
	}
	contract := common.HexToAddress(evt.ContractAddress)
	addr := func(key string) (common.Address, bool) {
		v, ok := payloadAddress(evt, key)
		return common.HexToAddress(v), ok
	}
	switch evt.TxType {
	case poolSwapEvent:
		var out []router.Delta
		for _, key := range []string{"tokenIn", "tokenOut"} {
			if token, ok := addr(key); ok {
				out = append(out, router.DirtyDelta{Pool: contract, Token: token})
			}
		}
		return out
	case poolDepositEvent:
		if token, ok := addr("tokenIn"); ok {
			return []router.Delta{router.DirtyDelta{Pool: contract, Token: token}}
		}
	case tokenTransferEvent, tokenMintEvent, tokenBurnEvent:
		var parties []common.Address
		for _, key := range []string{"from", "to", "tokenBurner"} {
			if party, ok := addr(key); ok {
				parties = append(parties, party)
			}
		}
		if len(parties) > 0 {
			return []router.Delta{router.TransferDelta{Token: contract, Parties: parties}}
		}
	case sealEvent, quoterUpdatedEvent, feeDefaultUpdatedEvent,
		protocolFeeUpdatedEvent, protocolRecipientEvent, protocolFeeActiveEvent:
		// The contract is the pool, its fee policy or its protocol fee
		// controller; the refresh re-reads every pool that points at it.
		return []router.Delta{router.RefreshPoolDelta{Address: contract}}
	case feePairUpdatedEvent, feePairRemovedEvent:
		in, okIn := addr("tokenIn")
		out, okOut := addr("tokenOut")
		if !okIn || !okOut {
			return nil
		}
		d := router.PairFeeDelta{FeePolicy: contract, In: in, Out: out, Block: evt.Block,
			Removed: evt.TxType == feePairRemovedEvent}
		if !d.Removed {
			fee, ok := payloadUint(evt, "newFee")
			v, okV := parseUint256(fee)
			if !ok || !okV || !v.IsUint64() || v.Uint64() > router.PPM {
				return nil
			}
			d.FeePPM = v.Uint64()
		}
		return []router.Delta{d}
	case proxyUpgradeEvent:
		out := []router.Delta{router.RefreshPoolDelta{Address: contract}}
		if proxy, ok := addr("proxy"); ok && proxy != contract {
			out = append(out, router.RefreshPoolDelta{Address: proxy})
		}
		return out
	}
	return nil
}

// settleRoutedSwap fills a tracked route's hop from a pool's settlement event.
func (s *Sub) settleRoutedSwap(ctx context.Context, tx pgx.Tx, trackingID string, evt event.Event) error {
	if !evt.Success || evt.TxType != poolSwapEvent || trackingID == "" {
		return nil
	}
	tokenIn, okIn := payloadAddress(evt, "tokenIn")
	tokenOut, okOut := payloadAddress(evt, "tokenOut")
	amountIn, okA := payloadUint(evt, "amountIn")
	amountOut, okB := payloadUint(evt, "amountOut")
	fee, okF := payloadUint(evt, "fee")
	if !okIn || !okOut || !okA || !okB || !okF {
		return nil
	}
	optional := func(key string) *string {
		if v, ok := payloadUint(evt, key); ok {
			return &v
		}
		return nil
	}
	matched, err := s.store.SettleRoutedSwapHop(ctx, tx, trackingID, store.RoutedSwapSettlement{
		Pool: evt.ContractAddress, TokenIn: tokenIn, TokenOut: tokenOut,
		TxHash: evt.TxHash, Block: evt.Block, LogIndex: eventPosition(evt).LogIndex,
		AmountIn: amountIn, AmountOut: amountOut, PoolFee: fee,
		QuotedAmountOut:  optional("quotedAmountOut"),
		NominalAmountOut: optional("nominalAmountOut"),
		ProtocolFee:      optional("protocolFee"),
	})
	if err != nil {
		return err
	}
	if matched {
		s.logg.Info("Sub: settled routed swap hop", "trackingId", trackingID, "pool", evt.ContractAddress, "tx", evt.TxHash)
	}
	return nil
}

func edgeDeltas(keys []router.EdgeKey, build func(router.EdgeKey) router.Delta) []router.Delta {
	out := make([]router.Delta, 0, len(keys))
	for _, k := range keys {
		out = append(out, build(k))
	}
	return out
}

func parseUint256(v string) (uint256.Int, bool) {
	z, err := uint256.FromDecimal(v)
	if err != nil {
		return uint256.Int{}, false
	}
	return *z, true
}
