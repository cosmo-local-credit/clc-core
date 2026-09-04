package gas

import (
	"context"
	"log/slog"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/grassrootseconomics/ethutils"
	"github.com/lmittmann/w3/module/eth"
)

type (
	RPCGasOracleOpts struct {
		Logg          *slog.Logger
		ChainProvider *ethutils.Provider
		// MinFeeCapWei floors the computed fee cap. On a chain whose base fee
		// is denominated in single-digit wei — Gnosis sits between 8 and 16 —
		// a percentage bump over eth_gasPrice is rounding noise, and a cap one
		// wei above the base fee is stranded by the next block. 1 gwei is
		// negligible at relayer gas limits and removes the whole failure
		// class. Zero selects defaultMinFeeCapWei.
		MinFeeCapWei int64
	}

	RPCGasOracle struct {
		logg           *slog.Logger
		chainProvider  *ethutils.Provider
		cachedGasPrice *GasSettings
		minFeeCap      *big.Int
		stopCh         chan struct{}
	}
)

const (
	// Gnosis and Celo both produce a block every ~5s, so a 30s cache ran up to
	// six blocks stale — long enough for the base fee to move past a cap that
	// was correct when it was read.
	rpcUpdateInterval = 10 * time.Second

	// defaultMinFeeCapWei is 1 gwei.
	defaultMinFeeCapWei = 1_000_000_000

	// baseFeeMultiplier covers the base fee's maximum rise over the next
	// block (EIP-1559 caps it at 12.5% per block, so 2x buys several blocks of
	// headroom). Unused base fee is refunded, making a generous cap free.
	baseFeeMultiplier = 2
)

func NewRPCGasOracle(o RPCGasOracleOpts) (*RPCGasOracle, error) {
	minFeeCap := big.NewInt(o.MinFeeCapWei)
	if o.MinFeeCapWei <= 0 {
		minFeeCap = big.NewInt(defaultMinFeeCapWei)
	}

	rpcGasOracle := &RPCGasOracle{
		logg:          o.Logg,
		chainProvider: o.ChainProvider,
		minFeeCap:     minFeeCap,
		stopCh:        make(chan struct{}),
		cachedGasPrice: &GasSettings{
			GasLimit: uint64(ethutils.SafeGasLimit),
		},
	}

	if err := rpcGasOracle.updateGasPrice(); err != nil {
		return nil, err
	}

	return rpcGasOracle, nil
}

func (g *RPCGasOracle) Stop() {
	g.stopCh <- struct{}{}
}

func (g *RPCGasOracle) Start() {
	ticker := time.NewTicker(rpcUpdateInterval)
	defer ticker.Stop()

	for {
		select {
		case <-g.stopCh:
			g.logg.Debug("stopping rpc gas oracle updater")
			return
		case <-ticker.C:
			if err := g.updateGasPrice(); err != nil {
				g.logg.Error("failed to update rpc gas price", "err", err)
			}

		}
	}
}

func (g *RPCGasOracle) GetSettings() (*GasSettings, error) {
	return g.cachedGasPrice, nil
}

// updateGasPrice recomputes the cached fee cap from the pending block's base
// fee.
//
// The previous formula was eth_gasPrice * 1.2. On Gnosis that turned a 10 wei
// base fee into a 12 wei cap: a "20% bump" worth one or two wei, which the next
// block's base fee could overtake. Transactions signed that way sat unmined
// until the base fee happened to fall back, and one of them stranded the
// relayer's whole nonce sequence on 2026-09-04.
//
// baseFee*2 + tip is the standard one-block-horizon cap, floored at
// minFeeCap. Only gas actually burned is billed, so over-capping costs nothing.
func (g *RPCGasOracle) updateGasPrice() error {
	var (
		header    *types.Header
		newTipCap *big.Int
	)

	if err := g.chainProvider.Client.CallCtx(
		context.Background(),
		eth.HeaderByNumber(nil).Returns(&header),
		eth.GasTipCap().Returns(&newTipCap),
	); err != nil {
		return err
	}

	if newTipCap == nil || newTipCap.Sign() < 0 {
		newTipCap = new(big.Int)
	}

	baseFee := new(big.Int)
	if header != nil && header.BaseFee != nil {
		baseFee.Set(header.BaseFee)
	}

	newFeeCap := new(big.Int).Mul(baseFee, big.NewInt(baseFeeMultiplier))
	newFeeCap.Add(newFeeCap, newTipCap)

	if newFeeCap.Cmp(g.minFeeCap) < 0 {
		newFeeCap.Set(g.minFeeCap)
	}

	g.cachedGasPrice.GasFeeCap = newFeeCap
	g.cachedGasPrice.GasTipCap = newTipCap
	g.logg.Debug("updated rpc gas price",
		"base_fee", baseFee,
		"gas_fee_cap", g.cachedGasPrice.GasFeeCap,
		"gas_tip_cap", g.cachedGasPrice.GasTipCap,
	)

	return nil
}
