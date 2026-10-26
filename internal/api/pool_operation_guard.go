package api

import (
	"bytes"
	"slices"
	"strings"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// Pool operation targets include transfers to a pool as well as every swap or
// deposit hop. Calldata is inspected per call, not only for a single frontend
// batch shape, so custom-address sends and multi-hop swaps obey retirement too.
// Withdrawals release existing funds and remain available under contract checks.
func poolOperationTargets(batch apiresp.CaliburBatchedCall) []string {
	targets := make(map[string]struct{})
	add := func(address string) {
		if common.IsHexAddress(address) {
			targets[strings.ToLower(address)] = struct{}{}
		}
	}
	for _, call := range batch.Calls {
		data, err := hexutil.Decode(call.Data)
		if err != nil || len(data) < 4 {
			continue
		}
		selector := data[:4]
		switch {
		case bytes.Equal(selector, functionSelector("deposit(address,uint256)")),
			bytes.Equal(selector, poolWithdrawSelector):
			add(call.To)
		case bytes.Equal(selector, poolWithdrawRecipientSelector),
			bytes.Equal(selector, functionSelector("withdraw(address,address,uint256,address,uint256,uint256)")):
			add(call.To)
			if len(data) >= 132 {
				add(common.BytesToAddress(data[112:132]).Hex())
			}
		case bytes.Equal(selector, functionSelector("withdrawLiquidity(address,address,uint256)")) && len(data) >= 100:
			// Recovery from a retired pool remains available, but a recipient
			// pool must still accept new deposits.
			add(common.BytesToAddress(data[48:68]).Hex())
		case bytes.Equal(selector, erc20TransferSelector) && len(data) >= 68:
			add(common.BytesToAddress(data[16:36]).Hex())
		case bytes.Equal(selector, functionSelector("transferFrom(address,address,uint256)")) && len(data) >= 100:
			add(common.BytesToAddress(data[48:68]).Hex())
		}
	}
	addresses := make([]string, 0, len(targets))
	for address := range targets {
		addresses = append(addresses, address)
	}
	slices.Sort(addresses)
	return addresses
}
