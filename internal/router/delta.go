package router

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

const (
	// ReconcileLogIndex is the log index a chain read at block N writes with. A
	// read at the end of block N already includes every event in it, so a late
	// event from block N loses and one from block N+1 wins.
	ReconcileLogIndex = 2147483647
)

type (
	// Position orders writes to one projected field: (block, log index).
	Position struct {
		Block    uint64
		LogIndex uint32
	}

	// Delta is a change the graph applies after the database has committed it.
	// Applying a delta twice, or after a newer one, changes nothing.
	Delta interface {
		isDelta()
	}

	// EdgeAllowedDelta records a registry membership change.
	EdgeAllowedDelta struct {
		Key     EdgeKey
		Allowed bool
		Pos     Position
	}

	// EdgeLimitDelta records a limiter cap.
	EdgeLimitDelta struct {
		Key   EdgeKey
		Limit uint256.Int
		Pos   Position
	}

	// EdgePriceDelta records a RelativeQuoter price index.
	EdgePriceDelta struct {
		Key   EdgeKey
		Index uint256.Int
		Pos   Position
	}

	// EdgeLiquidityDelta records a chain-read balance and held fees.
	EdgeLiquidityDelta struct {
		Key      EdgeKey
		Balance  uint256.Int
		FeesHeld uint256.Int
		Block    uint64
	}

	// ReloadDelta asks for the graph to be rebuilt from the database: used
	// when a change touches rows the writer has never seen, or many fields
	// at once.
	ReloadDelta struct{}

	// DirtyDelta marks a pool's balance of a token as changed; the
	// reconciler reads it.
	DirtyDelta struct {
		Pool  common.Address
		Token common.Address
	}

	// TransferDelta reports a token movement between parties; any party that
	// is a pool is marked dirty.
	TransferDelta struct {
		Token   common.Address
		Parties []common.Address
	}

	// PairFeeDelta records a FeePolicy pair fee change for every pool using
	// that policy. Removed means the override was deleted and the default
	// applies.
	PairFeeDelta struct {
		FeePolicy common.Address
		In, Out   common.Address
		FeePPM    uint64
		Removed   bool
		Block     uint64
	}

	// RefreshPoolDelta asks for one pool's parameters to be re-read, after a
	// seal or proxy upgrade on the pool or its periphery.
	RefreshPoolDelta struct {
		Address common.Address
	}
)

// ReconcilePosition is where a chain read at block lands.
func ReconcilePosition(block uint64) Position {
	return Position{Block: block, LogIndex: ReconcileLogIndex}
}

// After reports whether p is strictly newer than q. The zero position is older
// than any real one.
func (p Position) After(q Position) bool {
	if p.Block != q.Block {
		return p.Block > q.Block
	}
	return p.LogIndex > q.LogIndex
}

func (EdgeAllowedDelta) isDelta()   {}
func (EdgeLimitDelta) isDelta()     {}
func (EdgePriceDelta) isDelta()     {}
func (EdgeLiquidityDelta) isDelta() {}
func (ReloadDelta) isDelta()        {}
func (DirtyDelta) isDelta()         {}
func (TransferDelta) isDelta()      {}
func (RefreshPoolDelta) isDelta()   {}
func (PairFeeDelta) isDelta()       {}

// Apply folds one delta into the state. It reports whether anything changed,
// and whether the delta named a row the state does not hold, in which case the
// caller reloads.
func (st *State) Apply(d Delta) (changed, missing bool) {
	switch d := d.(type) {
	case EdgeAllowedDelta:
		edge, ok := st.edge(d.Key)
		if !ok {
			return false, true
		}
		if !d.Pos.After(edge.AllowedPos) {
			return false, false
		}
		changed = edge.Allowed != d.Allowed
		edge.Allowed, edge.AllowedPos = d.Allowed, d.Pos
		return changed, false
	case EdgeLimitDelta:
		edge, ok := st.edge(d.Key)
		if !ok {
			return false, true
		}
		if !d.Pos.After(edge.LimitPos) {
			return false, false
		}
		changed = !edge.HasLimit || !edge.Limit.Eq(&d.Limit)
		edge.Limit, edge.HasLimit, edge.LimitPos = d.Limit, true, d.Pos
		return changed, false
	case EdgePriceDelta:
		edge, ok := st.edge(d.Key)
		if !ok {
			return false, true
		}
		if !d.Pos.After(edge.PricePos) {
			return false, false
		}
		changed = !edge.PriceKnown || !edge.PriceIndex.Eq(&d.Index)
		edge.PriceIndex, edge.PriceKnown, edge.PricePos = d.Index, true, d.Pos
		return changed, false
	case EdgeLiquidityDelta:
		edge, ok := st.edge(d.Key)
		if !ok {
			return false, true
		}
		if edge.HasBalance && d.Block < edge.BalanceBlock {
			return false, false
		}
		changed = !edge.HasBalance || !edge.Balance.Eq(&d.Balance) || !edge.FeesHeld.Eq(&d.FeesHeld) ||
			edge.BalanceBlock != d.Block
		edge.Balance, edge.FeesHeld, edge.HasBalance, edge.BalanceBlock = d.Balance, d.FeesHeld, true, d.Block
		return changed, false
	case ReloadDelta:
		return false, true
	}
	return false, false
}

func (st *State) edge(key EdgeKey) (*EdgeState, bool) {
	if _, ok := st.Pools[key.PoolID]; !ok {
		return nil, false
	}
	if _, ok := st.Tokens[key.VoucherID]; !ok {
		return nil, false
	}
	edge, ok := st.Edges[key]
	return edge, ok
}
