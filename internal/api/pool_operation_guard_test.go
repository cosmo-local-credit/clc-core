package api

import (
	"math/big"
	"strings"
	"testing"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

func TestPoolOperationTargetsIncludesTransfersAndAllSwapHops(t *testing.T) {
	pool := common.HexToAddress("0x3333333333333333333333333333333333333333")
	second := common.HexToAddress("0x4444444444444444444444444444444444444444")
	token := common.HexToAddress("0x1111111111111111111111111111111111111111")
	amount := big.NewInt(5)
	cases := map[string][]apiresp.CaliburCall{
		"send deposit":         {{To: token.Hex(), Data: transferCalldata(pool, amount)}},
		"transferFrom deposit": {{To: token.Hex(), Data: calldata(functionSelector("transferFrom(address,address,uint256)"), addressWord(token), addressWord(pool), uintWord(amount))}},
		"deposit":              {{To: pool.Hex(), Data: calldata(functionSelector("deposit(address,uint256)"), addressWord(token), uintWord(amount))}},
		"swap":                 {{To: pool.Hex(), Data: withdrawCalldata(token, second, amount, nil)}},
	}
	for name, calls := range cases {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, []string{strings.ToLower(pool.Hex())}, poolOperationTargets(apiresp.CaliburBatchedCall{Calls: calls}))
		})
	}
	batch := apiresp.CaliburBatchedCall{Calls: []apiresp.CaliburCall{
		{To: pool.Hex(), Data: withdrawCalldata(token, second, amount, nil)},
		{To: second.Hex(), Data: withdrawCalldata(token, pool, amount, nil)},
	}}
	require.Equal(t, []string{strings.ToLower(pool.Hex()), strings.ToLower(second.Hex())}, poolOperationTargets(batch))
}

func TestPoolOperationTargetsPreservesOwnerWithdrawals(t *testing.T) {
	pool := common.HexToAddress("0x3333333333333333333333333333333333333333")
	token := common.HexToAddress("0x1111111111111111111111111111111111111111")
	amount := big.NewInt(5)
	batch := apiresp.CaliburBatchedCall{Calls: []apiresp.CaliburCall{
		{To: pool.Hex(), Data: calldata(functionSelector("withdraw(address)"), addressWord(token))},
		{To: pool.Hex(), Data: calldata(functionSelector("withdraw(address,uint256)"), addressWord(token), uintWord(amount))},
	}}
	require.Empty(t, poolOperationTargets(batch))
}

func TestPoolOperationTargetsIncludesExplicitRecipients(t *testing.T) {
	pool := common.HexToAddress("0x3333333333333333333333333333333333333333")
	recipient := common.HexToAddress("0x4444444444444444444444444444444444444444")
	token := common.HexToAddress("0x1111111111111111111111111111111111111111")
	amount := big.NewInt(5)
	cases := map[string]struct {
		data string
		want []string
	}{
		"recipient swap": {
			data: withdrawCalldata(token, token, amount, &recipient),
			want: []string{strings.ToLower(pool.Hex()), strings.ToLower(recipient.Hex())},
		},
		"bounded swap": {
			data: calldata(functionSelector("withdraw(address,address,uint256,address,uint256,uint256)"), addressWord(token), addressWord(token), uintWord(amount), addressWord(recipient), uintWord(amount), uintWord(amount)),
			want: []string{strings.ToLower(pool.Hex()), strings.ToLower(recipient.Hex())},
		},
		"liquidity withdrawal preserves source": {
			data: calldata(functionSelector("withdrawLiquidity(address,address,uint256)"), addressWord(token), addressWord(recipient), uintWord(amount)),
			want: []string{strings.ToLower(recipient.Hex())},
		},
	}
	for name, tt := range cases {
		t.Run(name, func(t *testing.T) {
			batch := apiresp.CaliburBatchedCall{Calls: []apiresp.CaliburCall{{To: pool.Hex(), Data: tt.data}}}
			require.Equal(t, tt.want, poolOperationTargets(batch))
		})
	}
}
