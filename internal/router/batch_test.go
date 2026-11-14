package router

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	walletAddr = common.HexToAddress("0x1000000000000000000000000000000000000001")
	poolOne    = common.HexToAddress("0x2000000000000000000000000000000000000002")
	poolTwo    = common.HexToAddress("0x2000000000000000000000000000000000000003")
	tokenA     = common.HexToAddress("0x3000000000000000000000000000000000000004")
	tokenB     = common.HexToAddress("0x3000000000000000000000000000000000000005")
	tokenC     = common.HexToAddress("0x3000000000000000000000000000000000000006")
)

func twoHopPlan() []PlannedHop {
	return []PlannedHop{
		{Pool: poolOne, TokenIn: tokenA, TokenOut: tokenB, AmountIn: big.NewInt(1_000_000), MinOut: big.NewInt(999_800), Recipient: walletAddr},
		{Pool: poolTwo, TokenIn: tokenB, TokenOut: tokenC, AmountIn: big.NewInt(999_800), MinOut: big.NewInt(999_600), Recipient: walletAddr},
	}
}

func TestBuildDecodeRoundTrip(t *testing.T) {
	plan := twoHopPlan()
	calls := BuildRouteCalls(plan, 1_794_567_890)
	require.Len(t, calls, 6)

	hops, ok := DecodeRouteShape(calls)
	require.True(t, ok)
	require.Len(t, hops, 2)
	for i, hop := range hops {
		assert.Equal(t, plan[i].Pool, hop.Pool)
		assert.Equal(t, plan[i].TokenIn, hop.TokenIn)
		assert.Equal(t, plan[i].TokenOut, hop.TokenOut)
		assert.Equal(t, 0, plan[i].AmountIn.Cmp(hop.Amount))
		assert.Equal(t, 0, plan[i].AmountIn.Cmp(hop.ApprovedAmount))
		assert.Equal(t, 0, plan[i].MinOut.Cmp(hop.MinOut))
		assert.Equal(t, plan[i].Recipient, hop.Recipient)
		assert.True(t, hop.Bounded)
		assert.Equal(t, uint64(1_794_567_890), hop.Deadline.Uint64())
		assert.Equal(t, "909a6e32", common.Bytes2Hex(calls[3*i+2].Data[:4]), "the bounded overload")
	}
	require.NoError(t, ValidateRoute(hops, walletAddr, 1_794_567_890-300, 3600))
}

func TestValidateRouteRejectsTampering(t *testing.T) {
	now := uint64(1_794_567_590)
	for _, tt := range []struct {
		name   string
		tamper func([]Call)
	}{
		{"approval larger than the withdrawal", func(c []Call) {
			c[1].Data = encodeApprove(poolOne, big.NewInt(2_000_000))
		}},
		{"approval to another spender", func(c []Call) {
			c[1].Data = encodeApprove(poolTwo, big.NewInt(1_000_000))
		}},
		{"reset that is not zero", func(c []Call) {
			c[0].Data = encodeApprove(poolOne, big.NewInt(1))
		}},
		{"intermediate output paid elsewhere", func(c []Call) {
			c[2].Data = encodeBoundedWithdraw(tokenB, tokenA, big.NewInt(1_000_000),
				common.HexToAddress("0xdead000000000000000000000000000000000000"), big.NewInt(1), big.NewInt(int64(now+60)))
		}},
		{"deadline beyond the horizon", func(c []Call) {
			c[5].Data = encodeBoundedWithdraw(tokenC, tokenB, big.NewInt(999_800), walletAddr,
				big.NewInt(999_600), big.NewInt(int64(now+86_400)))
		}},
		{"approval of the wrong token", func(c []Call) {
			c[4].To = tokenA
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := BuildRouteCalls(twoHopPlan(), now+300)
			tt.tamper(calls)
			hops, ok := DecodeRouteShape(calls)
			require.True(t, ok, "still route-shaped")
			err := ValidateRoute(hops, walletAddr, now, 3600)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrInvalidRoute))
		})
	}
}

func TestDecodeRouteShapeIgnoresOtherBatches(t *testing.T) {
	transfer := Call{To: tokenA, Value: new(big.Int), Data: append(selector("transfer(address,uint256)"), make([]byte, 64)...)}
	_, ok := DecodeRouteShape([]Call{transfer})
	assert.False(t, ok)

	calls := BuildRouteCalls(twoHopPlan(), 1)
	calls[2].Value = big.NewInt(1)
	_, ok = DecodeRouteShape(calls)
	assert.False(t, ok, "a call carrying value is not a swap hop")

	_, ok = DecodeRouteShape(calls[:5])
	assert.False(t, ok)
}

func TestDecodeRouteShapeAcceptsLegacyOverloads(t *testing.T) {
	amount := big.NewInt(500)
	withdraw := slicesConcat(selectorWithdraw, addressWord(tokenB), addressWord(tokenA), word(amount))
	calls := []Call{
		{To: tokenA, Value: new(big.Int), Data: encodeApprove(poolOne, new(big.Int))},
		{To: tokenA, Value: new(big.Int), Data: encodeApprove(poolOne, amount)},
		{To: poolOne, Value: new(big.Int), Data: withdraw},
	}
	hops, ok := DecodeRouteShape(calls)
	require.True(t, ok)
	assert.False(t, hops[0].Bounded)
	assert.Equal(t, walletAddr, hops[0].RecipientOr(walletAddr))
	require.NoError(t, ValidateRoute(hops, walletAddr, 0, 3600))
}

func TestCaliburNonceMatchesRelayer(t *testing.T) {
	// The relayer and the app pack (low 192 bits of keyHash << 64) | seq; so
	// must the quote.
	keyHash := common.HexToHash("0xb846053507b7089dbb6964b83e8f875c8a86acd52530e21fe2e337a10b390f90")
	nonce := CaliburNonce(keyHash, 7)
	want, _ := new(big.Int).SetString("bb6964b83e8f875c8a86acd52530e21fe2e337a10b390f900000000000000007", 16)
	assert.Equal(t, 0, want.Cmp(nonce))
	assert.Equal(t, 0, big.NewInt(3).Cmp(CaliburNonce(common.Hash{}, 3)))
}

// The digest of a router-built two-hop batch, computed independently by the
// app's computeEIP712Digest (clc-app packages/core webauthn/logic.ts, viem
// hashTypedData). The quote is directly signable only if both sides agree.
func TestDigestMatchesTheAppVector(t *testing.T) {
	keyHash := common.HexToHash("0xb846053507b7089dbb6964b83e8f875c8a86acd52530e21fe2e337a10b390f90")
	batch := SignedBatch{
		Calls:           BuildRouteCalls(twoHopPlan(), 1_794_567_890),
		RevertOnFailure: true,
		Nonce:           CaliburNonce(keyHash, 7),
		KeyHash:         keyHash,
		Executor:        common.HexToAddress("0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC"),
		Deadline:        1_794_567_890,
	}
	digest := batch.Digest(big.NewInt(100), walletAddr, common.HexToAddress("0x1289fE793392fb1a905f78Ea23f632EDD3d15b85"))
	assert.Equal(t, "0xd511c4e41329c047c7c59d95ab025f4025c4c23cdf5e996fe089536fe58e7437", digest.Hex())
}
