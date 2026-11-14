package router

import (
	"context"
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

type (
	// ChainReader is every chain read the router makes. Each method reads at the
	// given block, so one quote sees one consistent state; block 0 means latest.
	// Per-item failures are reported on the item, not as the method's error,
	// which is reserved for the transport.
	ChainReader interface {
		Head(ctx context.Context) (Head, error)
		PoolParams(ctx context.Context, block uint64, pools []common.Address) ([]PoolParams, error)
		PoolStructure(ctx context.Context, block uint64, probes []StructureProbe) ([]PoolStructure, error)
		Balances(ctx context.Context, block uint64, edges []BalanceProbe) ([]BalanceReading, error)
		PairFees(ctx context.Context, block uint64, pairs []PairProbe) ([]PairFeeReading, error)
		Quotes(ctx context.Context, block uint64, probes []AmountProbe) ([]AmountReading, error)
		AmountsOut(ctx context.Context, block uint64, probes []AmountProbe) ([]AmountReading, error)
		AmountsIn(ctx context.Context, block uint64, probes []AmountProbe) ([]AmountReading, error)
		HolderBalances(ctx context.Context, block uint64, holder common.Address, tokens []common.Address) ([]AmountReading, error)
		Wallet(ctx context.Context, block uint64, wallet common.Address, keyHash common.Hash) (WalletReading, error)
		Simulate(ctx context.Context, block uint64, wallet common.Address, calldata []byte, delegateTo *common.Address) (SimResult, error)
	}

	// Head is a block the router pins reads to.
	Head struct {
		Number uint64
		Hash   common.Hash
		Time   uint64
	}

	// PoolParams is what a pool and its fee contracts report about
	// themselves. None of it emits a tracked event.
	PoolParams struct {
		Pool          common.Address
		Registry      common.Address
		Limiter       common.Address
		Quoter        common.Address
		FeePolicy     common.Address
		Controller    common.Address
		FeeAddress    common.Address
		FeesDecoupled bool
		SealState     uint8
		DefaultFeePPM uint64
		// ProtocolFeePPM is what the pool charges: zero when the controller
		// is unset, inactive, set to zero or has no recipient.
		ProtocolFeePPM    uint64
		ProtocolRecipient common.Address
		Err               error
	}

	// StructureProbe asks for a pool's registry members and, for each of
	// them and each Known token, membership, limit and price index.
	StructureProbe struct {
		Pool     common.Address
		Registry common.Address
		Limiter  common.Address
		Quoter   common.Address
		// Relative says the quoter is a RelativeQuoter, so priceIndex is
		// read.
		Relative bool
		Known    []common.Address
		// SkipEntries reads only the Known tokens.
		SkipEntries bool
	}

	// PoolStructure is one pool's registry and per-token settings.
	PoolStructure struct {
		Pool    common.Address
		Members []MemberReading
		Err     error
	}

	// MemberReading is one token's settings in a pool. Limit is nil when the
	// pool has no limiter, PriceIndex when the quoter is not relative.
	MemberReading struct {
		Token      common.Address
		Have       bool
		Limit      *uint256.Int
		PriceIndex *uint256.Int
	}

	BalanceProbe struct {
		Pool, Token common.Address
	}

	BalanceReading struct {
		Pool, Token common.Address
		Balance     uint256.Int
		FeesHeld    uint256.Int
		Err         error
	}

	PairProbe struct {
		Pool, In, Out common.Address
	}

	PairFeeReading struct {
		Pool, In, Out common.Address
		FeePPM        uint64
		Err           error
	}

	// AmountProbe is one call of the form f(out, in, amount) on a pool.
	AmountProbe struct {
		Pool, In, Out common.Address
		Amount        uint256.Int
	}

	AmountReading struct {
		Value uint256.Int
		Err   error
	}

	// WalletReading is what signing a batch needs from the wallet.
	WalletReading struct {
		Code       []byte
		CaliburSeq uint64
		EOANonce   uint64
	}

	// SimResult is the outcome of running the batch before it is signed.
	SimResult struct {
		OK         bool
		RevertData []byte
		// GasEstimate is zero when the provider could not estimate.
		GasEstimate uint64
	}
)

var (
	// ErrNoChain is returned when the router runs without a chain reader.
	ErrNoChain = errors.New("router: no chain reader")
)
