package safe

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/grassrootseconomics/ethutils"
	"github.com/lmittmann/w3"
	"github.com/lmittmann/w3/module/eth"
)

// ErrNotSafe means the address is not a supported Safe at the requested block:
// no code, no answer to the probe, or a version outside SupportedVersions.
var ErrNotSafe = errors.New("safe: not a supported Safe")

var (
	// SupportedVersions are the Safe releases whose execTransaction and
	// MultiSend layouts the policy decodes.
	SupportedVersions = []string{"1.3.0", "1.4.1"}

	versionFunc      = w3.MustNewFunc("VERSION()", "string")
	getOwnersFunc    = w3.MustNewFunc("getOwners()", "address[]")
	getThresholdFunc = w3.MustNewFunc("getThreshold()", "uint256")
)

// State is a Safe's signer set as read from chain.
type State struct {
	Owners    []common.Address
	Threshold int
}

// Reader reads Safe state over RPC.
type Reader struct {
	provider *ethutils.Provider
}

func NewReader(provider *ethutils.Provider) *Reader {
	return &Reader{provider: provider}
}

// ReadSafe returns the owners and threshold of address at block. A contract
// that does not answer the probe, or reports an unsupported version, is
// ErrNotSafe; transport failures are returned as-is so the caller retries.
func (r *Reader) ReadSafe(ctx context.Context, address common.Address, block uint64) (*State, error) {
	at := new(big.Int).SetUint64(block)
	var (
		version   string
		owners    []common.Address
		threshold big.Int
	)
	err := r.provider.Client.CallCtx(ctx,
		eth.CallFunc(address, versionFunc).AtBlock(at).Returns(&version),
		eth.CallFunc(address, getOwnersFunc).AtBlock(at).Returns(&owners),
		eth.CallFunc(address, getThresholdFunc).AtBlock(at).Returns(&threshold),
	)
	var callErrs w3.CallErrors
	if errors.As(err, &callErrs) {
		return nil, ErrNotSafe
	}
	if err != nil {
		return nil, fmt.Errorf("safe: read %s at %d: %w", address.Hex(), block, err)
	}
	if !supportedVersion(version) || !threshold.IsInt64() || threshold.Int64() < 1 ||
		int(threshold.Int64()) > len(owners) {
		return nil, ErrNotSafe
	}
	return &State{Owners: owners, Threshold: int(threshold.Int64())}, nil
}

// ReadSafeLatest reads at the current head and returns the block it read, so
// the result can be stored at a real position.
func (r *Reader) ReadSafeLatest(ctx context.Context, address common.Address) (*State, uint64, error) {
	var head *big.Int
	if err := r.provider.Client.CallCtx(ctx, eth.BlockNumber().Returns(&head)); err != nil {
		return nil, 0, fmt.Errorf("safe: read head: %w", err)
	}
	state, err := r.ReadSafe(ctx, address, head.Uint64())
	return state, head.Uint64(), err
}

func supportedVersion(v string) bool {
	for _, s := range SupportedVersions {
		if s == v {
			return true
		}
	}
	return false
}

// Caller performs an eth_call against a fixed block.
type Caller func(ctx context.Context, to common.Address, data []byte) ([]byte, error)

// ReadSafeVia is ReadSafe over a bare eth_call, for commands that hold an
// ethclient instead of the service's provider. Any failed call, including a
// revert, reports ErrNotSafe: the snapshot has already verified the chain and
// block, so an unanswered probe means the owner is not a Safe.
func ReadSafeVia(ctx context.Context, address common.Address, call Caller) (*State, error) {
	var version string
	data, err := call(ctx, address, versionFunc.Selector[:])
	if err != nil || versionFunc.DecodeReturns(data, &version) != nil {
		return nil, ErrNotSafe
	}
	var owners []common.Address
	data, err = call(ctx, address, getOwnersFunc.Selector[:])
	if err != nil || getOwnersFunc.DecodeReturns(data, &owners) != nil {
		return nil, ErrNotSafe
	}
	var threshold big.Int
	data, err = call(ctx, address, getThresholdFunc.Selector[:])
	if err != nil || getThresholdFunc.DecodeReturns(data, &threshold) != nil {
		return nil, ErrNotSafe
	}
	if !supportedVersion(version) || !threshold.IsInt64() || threshold.Int64() < 1 ||
		int(threshold.Int64()) > len(owners) {
		return nil, ErrNotSafe
	}
	return &State{Owners: owners, Threshold: int(threshold.Int64())}, nil
}
