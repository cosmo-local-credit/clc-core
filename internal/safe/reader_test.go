package safe

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

func TestCompatibleMultisigWithoutVersionOrThreshold(t *testing.T) {
	encoded, err := abi.Arguments{{Type: mustType(t, "address[]")}}.Pack([]common.Address{holder, assetAddr})
	require.NoError(t, err)
	caller := func(_ context.Context, _ common.Address, data []byte) ([]byte, error) {
		if [4]byte(data) == getOwnersFunc.Selector {
			return encoded, nil
		}
		return nil, errors.New("execution reverted")
	}
	state, err := ReadSafeVia(context.Background(), safeAddr, caller)
	require.NoError(t, err)
	require.ElementsMatch(t, []common.Address{holder, assetAddr}, state.Owners)
	require.Zero(t, state.Threshold, "unknown threshold must not prevent single-signer metadata edits")
}

func TestMultisigReadPreservesTransportErrors(t *testing.T) {
	outage := errors.New("connection reset by peer")
	_, err := ReadSafeVia(context.Background(), safeAddr, func(context.Context, common.Address, []byte) ([]byte, error) { return nil, outage })
	require.ErrorIs(t, err, outage)
}

func TestOptionalThresholdOutageDoesNotEraseVerifiedSigners(t *testing.T) {
	encoded, err := abi.Arguments{{Type: mustType(t, "address[]")}}.Pack([]common.Address{holder})
	require.NoError(t, err)
	state, err := ReadSafeVia(context.Background(), safeAddr, func(_ context.Context, _ common.Address, data []byte) ([]byte, error) {
		if [4]byte(data) == getOwnersFunc.Selector {
			return encoded, nil
		}
		return nil, errors.New("connection reset by peer")
	})
	require.NoError(t, err)
	require.Equal(t, []common.Address{holder}, state.Owners)
	require.Zero(t, state.Threshold)
}

func TestMultisigRejectsInvalidSignerSets(t *testing.T) {
	for _, owners := range [][]common.Address{{}, {holder, holder}, {{}}} {
		encoded, err := abi.Arguments{{Type: mustType(t, "address[]")}}.Pack(owners)
		require.NoError(t, err)
		threshold, err := abi.Arguments{{Type: mustType(t, "uint256")}}.Pack(big.NewInt(1))
		require.NoError(t, err)
		_, err = ReadSafeVia(context.Background(), safeAddr, func(_ context.Context, _ common.Address, data []byte) ([]byte, error) {
			if [4]byte(data) == getOwnersFunc.Selector {
				return encoded, nil
			}
			return threshold, nil
		})
		require.ErrorIs(t, err, ErrNotSafe)
	}
}
