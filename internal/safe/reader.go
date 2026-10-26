package safe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/grassrootseconomics/ethutils"
	"github.com/lmittmann/w3"
	"github.com/lmittmann/w3/module/eth"
	"github.com/lmittmann/w3/w3types"
)

var (
	// ErrNotSafe means the address does not expose a valid Safe-style signer set.
	// VERSION and getThreshold are optional for off-chain metadata permissions.
	ErrNotSafe = errors.New("safe: no compatible multisig signer set")

	versionFunc      = w3.MustNewFunc("VERSION()", "string")
	getOwnersFunc    = w3.MustNewFunc("getOwners()", "address[]")
	getThresholdFunc = w3.MustNewFunc("getThreshold()", "uint256")
	nonceFunc        = w3.MustNewFunc("nonce()", "uint256")
)

type (
	// State is a Safe's signer set as read from chain.
	State struct {
		Owners    []common.Address
		Threshold int
	}

	// Reader reads Safe state over RPC.
	Reader struct {
		provider *ethutils.Provider
	}

	// Caller performs an eth_call against a fixed block.
	Caller func(ctx context.Context, to common.Address, data []byte) ([]byte, error)
)

func NewReader(provider *ethutils.Provider) *Reader {
	return &Reader{provider: provider}
}

// ReadSafe reads capabilities at one block. Delegated EOAs remain ordinary
// accounts. A custom contract can support metadata without exposing VERSION
// or getThreshold; a zero Threshold in State means it could not be read.
func (r *Reader) ReadSafe(ctx context.Context, address common.Address, block uint64) (*State, error) {
	at := new(big.Int).SetUint64(block)
	var code []byte
	if err := r.provider.Client.CallCtx(ctx, eth.Code(address, at).Returns(&code)); err != nil {
		return nil, err
	}
	if len(code) == 0 || (len(code) == 23 && bytes.HasPrefix(code, []byte{0xef, 0x01, 0x00})) {
		return nil, ErrNotSafe
	}
	return ReadSafeVia(ctx, address, func(ctx context.Context, to common.Address, data []byte) ([]byte, error) {
		var result []byte
		err := r.provider.Client.CallCtx(ctx, eth.Call(&w3types.Message{To: &to, Input: data}, at, nil).Returns(&result))
		return result, err
	})
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

// ReadExecState reads the signer set, threshold and next nonce at one head
// block, so an execution is judged against a single consistent state.
func (r *Reader) ReadExecState(ctx context.Context, address common.Address) (*State, uint64, error) {
	var head *big.Int
	if err := r.provider.Client.CallCtx(ctx, eth.BlockNumber().Returns(&head)); err != nil {
		return nil, 0, fmt.Errorf("safe: read head: %w", err)
	}
	state, err := r.ReadSafe(ctx, address, head.Uint64())
	if err != nil {
		return nil, 0, err
	}
	var nonce big.Int
	if err := r.provider.Client.CallCtx(ctx, eth.CallFunc(address, nonceFunc).AtBlock(head).Returns(&nonce)); err != nil {
		return nil, 0, fmt.Errorf("safe: read nonce: %w", err)
	}
	if !nonce.IsUint64() {
		return nil, 0, fmt.Errorf("safe: nonce out of range")
	}
	return state, nonce.Uint64(), nil
}

// UnsupportedCall distinguishes absent/reverting methods from transport
// failures. The latter must be retried rather than silently losing a projection.
func UnsupportedCall(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "execution reverted") || strings.Contains(message, "method not found") || strings.Contains(message, "invalid opcode")
}

// ReadSafeVia reads a Safe-compatible getOwners interface. Missing or invalid
// quorum information does not erase a valid signer list. No version is required.
func ReadSafeVia(ctx context.Context, address common.Address, call Caller) (*State, error) {
	var owners []common.Address
	data, err := call(ctx, address, getOwnersFunc.Selector[:])
	if err != nil {
		if UnsupportedCall(err) {
			return nil, ErrNotSafe
		}
		return nil, err
	}
	if getOwnersFunc.DecodeReturns(data, &owners) != nil || len(owners) == 0 {
		return nil, ErrNotSafe
	}
	seen := make(map[common.Address]bool, len(owners))
	for _, owner := range owners {
		if owner == (common.Address{}) || seen[owner] {
			return nil, ErrNotSafe
		}
		seen[owner] = true
	}
	state := &State{Owners: owners}
	var threshold big.Int
	data, err = call(ctx, address, getThresholdFunc.Selector[:])
	if err != nil {
		return state, nil
	}
	if getThresholdFunc.DecodeReturns(data, &threshold) != nil {
		return state, nil
	}
	if !threshold.IsInt64() || threshold.Int64() < 1 ||
		int(threshold.Int64()) > len(owners) {
		return state, nil
	}
	state.Threshold = int(threshold.Int64())
	return state, nil
}
