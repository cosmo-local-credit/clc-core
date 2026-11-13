package router

import (
	"testing"

	"github.com/holiman/uint256"
)

// Production shape (ROUTER.md §2.5): 105 pools, ~1,250 vouchers; the largest
// wallet holds 66 vouchers. The 10x graph checks the engine scales.
func benchSnapshot(b *testing.B, scale int) *Snapshot {
	b.Helper()
	return NewState(syntheticRows(105*scale, 1_250*scale, 11)).Build(BuildConfig{}, 1, NewFeeBook())
}

func walletOf(n int) []Holding {
	out := make([]Holding, n)
	for i := range out {
		out[i] = Holding{Token: int32(i * 7), Balance: *uint256.NewInt(5_000_000)}
	}
	return out
}

func BenchmarkReach66(b *testing.B) {
	for _, scale := range []int{1, 10} {
		snap := benchSnapshot(b, scale)
		holdings := walletOf(66)
		b.Run(map[int]string{1: "production", 10: "10x"}[scale], func(b *testing.B) {
			for b.Loop() {
				Reach(snap, holdings, 2)
			}
		})
	}
}

func BenchmarkRoutesToOneVoucher(b *testing.B) {
	for _, scale := range []int{1, 10} {
		snap := benchSnapshot(b, scale)
		holdings := walletOf(66)
		amount := uint256.NewInt(1_000_000)
		b.Run(map[int]string{1: "production", 10: "10x"}[scale], func(b *testing.B) {
			for b.Loop() {
				FindRoutes(snap, holdings, 3, 2, nil, ExactInput, amount, DefaultSearchBudget, RankConfig{})
			}
		})
	}
}

func BenchmarkPublish(b *testing.B) {
	st := NewState(syntheticRows(105, 1_250, 12))
	fees := NewFeeBook()
	for b.Loop() {
		st.Build(BuildConfig{}, 1, fees)
	}
}
