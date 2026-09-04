package worker

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBumpFeeClearsThresholdAtWeiScale is the Gnosis case: 12.5% of a
// single- or double-digit wei value rounds back to itself, so a percentage-only
// bump produces an identical fee cap and reth rejects the replacement as
// "replacement transaction underpriced".
func TestBumpFeeClearsThresholdAtWeiScale(t *testing.T) {
	for _, prev := range []int64{1, 2, 7, 8, 10, 12, 13, 15, 19} {
		bumped := bumpFee(big.NewInt(prev))
		assert.Greater(t, bumped.Int64(), prev,
			"bump of %d must strictly increase, got %d", prev, bumped.Int64())
	}
}

func TestBumpFeeUsesPercentageAtGweiScale(t *testing.T) {
	// 1 gwei -> 1.125 gwei, not 1 gwei + 1 wei.
	bumped := bumpFee(big.NewInt(1_000_000_000))
	assert.Equal(t, int64(1_125_000_000), bumped.Int64())
}

func TestBumpFeeHandlesZeroAndNil(t *testing.T) {
	assert.Equal(t, int64(1), bumpFee(nil).Int64())
	assert.Equal(t, int64(1), bumpFee(big.NewInt(0)).Int64())
}

func TestRebuildDynamicFeeTxPreservesPayload(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)

	chainID := big.NewInt(100)
	signer := types.LatestSignerForChainID(chainID)
	to := common.HexToAddress("0xb1ec3942dc9258de62056febb0dc544d9eb1ed1a")
	data := common.FromHex("0x0a3b0a4f00000000000000000000000088b73a6963498f82b4910a00ba579554cf46fb9e")

	prev, err := types.SignNewTx(key, signer, &types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     3,
		GasTipCap: big.NewInt(2),
		GasFeeCap: big.NewInt(12),
		Gas:       350_000,
		To:        &to,
		Value:     big.NewInt(0),
		Data:      data,
	})
	require.NoError(t, err)

	bumped, err := rebuildAtHigherFee(signer, prev, nil, nil, key)
	require.NoError(t, err)

	assert.Equal(t, prev.Nonce(), bumped.Nonce(), "a replacement must reuse the nonce")
	assert.Equal(t, prev.Gas(), bumped.Gas())
	assert.Equal(t, prev.Data(), bumped.Data())
	assert.Equal(t, prev.To().Hex(), bumped.To().Hex())
	assert.Equal(t, prev.Type(), bumped.Type())
	assert.Greater(t, bumped.GasFeeCap().Int64(), prev.GasFeeCap().Int64())
	assert.NotEqual(t, prev.Hash(), bumped.Hash())

	from, err := types.Sender(signer, bumped)
	require.NoError(t, err)
	assert.Equal(t, crypto.PubkeyToAddress(key.PublicKey), from)
}

// TestRebuildSetCodeTxPreservesAuthorizations — the authorization signatures
// inside a type-4 transaction are over the authority's own tuple, not the outer
// transaction, so re-signing at a higher fee must carry them through untouched.
// Losing them would turn a delegation into a no-op transfer.
func TestRebuildSetCodeTxPreservesAuthorizations(t *testing.T) {
	relayerKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	userKey, err := crypto.GenerateKey()
	require.NoError(t, err)

	chainID := big.NewInt(100)
	signer := types.LatestSignerForChainID(chainID)
	calibur := common.HexToAddress("0x48a910c8e9ff0b14051b78d0c96db069e57f0729")
	userAddr := crypto.PubkeyToAddress(userKey.PublicKey)

	auth, err := types.SignSetCode(userKey, types.SetCodeAuthorization{
		ChainID: *uint256.MustFromBig(chainID),
		Address: calibur,
		Nonce:   0,
	})
	require.NoError(t, err)

	prev, err := types.SignNewTx(relayerKey, signer, &types.SetCodeTx{
		ChainID:   uint256.MustFromBig(chainID),
		Nonce:     15,
		GasTipCap: uint256.NewInt(2),
		GasFeeCap: uint256.NewInt(13),
		Gas:       200_000,
		To:        userAddr,
		Value:     uint256.NewInt(0),
		AuthList:  []types.SetCodeAuthorization{auth},
	})
	require.NoError(t, err)

	bumped, err := rebuildAtHigherFee(signer, prev, nil, nil, relayerKey)
	require.NoError(t, err)

	assert.Equal(t, types.SetCodeTxType, int(bumped.Type()))
	assert.Equal(t, uint64(15), bumped.Nonce())
	require.Len(t, bumped.SetCodeAuthorizations(), 1)

	got := bumped.SetCodeAuthorizations()[0]
	assert.Equal(t, calibur, got.Address)
	assert.Equal(t, uint64(0), got.Nonce)

	// The authority must still recover to the user, not the relayer.
	authority, err := got.Authority()
	require.NoError(t, err)
	assert.Equal(t, userAddr, authority)

	assert.Greater(t, bumped.GasFeeCap().Int64(), prev.GasFeeCap().Int64())
}

// TestRebuildHonoursOracleTarget — when the oracle already wants more than the
// replacement threshold, take the oracle's number.
func TestRebuildHonoursOracleTarget(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)

	chainID := big.NewInt(100)
	signer := types.LatestSignerForChainID(chainID)
	to := common.HexToAddress("0xb1ec3942dc9258de62056febb0dc544d9eb1ed1a")

	prev, err := types.SignNewTx(key, signer, &types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     1,
		GasTipCap: big.NewInt(2),
		GasFeeCap: big.NewInt(12),
		Gas:       21_000,
		To:        &to,
		Value:     big.NewInt(0),
	})
	require.NoError(t, err)

	bumped, err := rebuildAtHigherFee(signer, prev, big.NewInt(1_000_000_000), big.NewInt(1), key)
	require.NoError(t, err)
	assert.Equal(t, int64(1_000_000_000), bumped.GasFeeCap().Int64())
}

// TestRebuildClampsTipToFeeCap — a tip above the cap is rejected outright as
// "max priority fee per gas higher than max fee per gas".
func TestRebuildClampsTipToFeeCap(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)

	chainID := big.NewInt(100)
	signer := types.LatestSignerForChainID(chainID)
	to := common.HexToAddress("0xb1ec3942dc9258de62056febb0dc544d9eb1ed1a")

	prev, err := types.SignNewTx(key, signer, &types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     1,
		GasTipCap: big.NewInt(2),
		GasFeeCap: big.NewInt(12),
		Gas:       21_000,
		To:        &to,
		Value:     big.NewInt(0),
	})
	require.NoError(t, err)

	// Oracle tip far above the cap it also reports.
	bumped, err := rebuildAtHigherFee(signer, prev, big.NewInt(20), big.NewInt(9_000), key)
	require.NoError(t, err)
	assert.LessOrEqual(t, bumped.GasTipCap().Int64(), bumped.GasFeeCap().Int64())
}

// TestReceiptCheckerImplementsRiverNextRetry is the guard for the bug that let
// the poller drift onto River's attempt^4 backoff: WorkerDefaults supplies a
// no-op NextRetry, so a misnamed override compiles and is silently ignored.
// Asserting the interface alone would not catch it — the schedule has to be
// observed.
func TestReceiptCheckerImplementsRiverNextRetry(t *testing.T) {
	var w river.Worker[ReceiptCheckerArgs] = &ReceiptCheckerWorker{}

	retryAt := func(attempt int) time.Time {
		return w.NextRetry(&river.Job[ReceiptCheckerArgs]{
			JobRow: &rivertype.JobRow{Attempt: attempt, MaxAttempts: receiptCheckerAttempts},
		})
	}

	// A zero time is what WorkerDefaults returns, and it makes River fall back
	// to DefaultClientRetryPolicy's attempt^4 seconds — 21 minutes at attempt
	// 6, which is how a mined transaction sat at IN_NETWORK for 34 minutes.
	for _, attempt := range []int{1, 6, 7, 15, 16} {
		assert.False(t, retryAt(attempt).IsZero(),
			"NextRetry(attempt=%d) must return a real time", attempt)
	}

	// Early attempts track the block time; the tail backs off but stays far
	// under attempt^4.
	assert.WithinDuration(t, time.Now().Add(5*time.Second), retryAt(1), time.Second)
	assert.WithinDuration(t, time.Now().Add(15*time.Second), retryAt(7), time.Second)
	assert.WithinDuration(t, time.Now().Add(60*time.Second), retryAt(16), time.Second)
}
