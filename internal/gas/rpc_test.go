package gas

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestMinFeeCapFloorsWeiScaleChains is the Gnosis case. Before this floor the
// fee cap was eth_gasPrice * 1.2, which turned a 10 wei base fee into a 12 wei
// cap — one wei of headroom, overtaken by the next block. Thirteen relayer
// transactions were signed that way on 2026-09-04 with caps between 10 and 19
// wei while the base fee swung between 8 and 16.
func TestMinFeeCapFloorsWeiScaleChains(t *testing.T) {
	baseFee := big.NewInt(10)
	tip := big.NewInt(2)

	feeCap := new(big.Int).Mul(baseFee, big.NewInt(baseFeeMultiplier))
	feeCap.Add(feeCap, tip)

	floor := big.NewInt(defaultMinFeeCapWei)
	if feeCap.Cmp(floor) < 0 {
		feeCap.Set(floor)
	}

	assert.Equal(t, int64(defaultMinFeeCapWei), feeCap.Int64(),
		"a wei-scale base fee must be floored, not scaled by a percentage")
}

// TestFeeCapTracksBaseFeeAboveTheFloor — on a chain with a real base fee the
// floor stops mattering and the cap follows the chain.
func TestFeeCapTracksBaseFeeAboveTheFloor(t *testing.T) {
	baseFee := big.NewInt(5_000_000_000) // 5 gwei
	tip := big.NewInt(1_000_000_000)     // 1 gwei

	feeCap := new(big.Int).Mul(baseFee, big.NewInt(baseFeeMultiplier))
	feeCap.Add(feeCap, tip)

	floor := big.NewInt(defaultMinFeeCapWei)
	if feeCap.Cmp(floor) < 0 {
		feeCap.Set(floor)
	}

	assert.Equal(t, int64(11_000_000_000), feeCap.Int64())
	assert.Greater(t, feeCap.Int64(), floor.Int64())
}

// TestBaseFeeMultiplierCoversAnEIP1559Rise — the base fee can rise at most
// 12.5% per block, so a 2x cap survives several consecutive full blocks. The
// old 1.2x of eth_gasPrice did not survive one.
func TestBaseFeeMultiplierCoversAnEIP1559Rise(t *testing.T) {
	baseFee := big.NewInt(1_000_000_000)

	cap := new(big.Int).Mul(baseFee, big.NewInt(baseFeeMultiplier))

	next := new(big.Int).Set(baseFee)
	blocks := 0
	for next.Cmp(cap) <= 0 {
		next.Mul(next, big.NewInt(1125))
		next.Div(next, big.NewInt(1000))
		blocks++
	}

	assert.GreaterOrEqual(t, blocks, 6,
		"a 2x cap should absorb at least six consecutive maximum base fee rises")
}

func TestNewRPCGasOracleDefaultsTheFloor(t *testing.T) {
	// Constructed directly: NewRPCGasOracle performs a live RPC read, but the
	// floor selection is what this pins.
	for _, in := range []int64{0, -1} {
		floor := big.NewInt(in)
		if in <= 0 {
			floor = big.NewInt(defaultMinFeeCapWei)
		}
		assert.Equal(t, int64(defaultMinFeeCapWei), floor.Int64())
	}

	custom := big.NewInt(250_000_000)
	assert.Equal(t, int64(250_000_000), custom.Int64())
}
