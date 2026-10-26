package api

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/ethereum/go-ethereum/common"
	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
	"github.com/lmittmann/w3"
	"github.com/lmittmann/w3/module/eth"
	"github.com/lmittmann/w3/w3types"
)

const (
	retirementMaxTokens = 5000
)

var (
	errRetirementOwnerChanged = errors.New("resource ownership changed; refresh the resource before requesting retirement")
	retirementOwnerFunc       = w3.MustNewFunc("owner()", "address")
	retirementSupplyFunc      = w3.MustNewFunc("totalSupply()", "uint256")
	retirementBalanceFunc     = w3.MustNewFunc("balanceOf(address)", "uint256")
	retirementDecimalsFunc    = w3.MustNewFunc("decimals()", "uint8")
	retirementRegistryFunc    = w3.MustNewFunc("tokenRegistry()", "address")
	retirementEntryCountFunc  = w3.MustNewFunc("entryCount()", "uint256")
	retirementEntryFunc       = w3.MustNewFunc("entry(uint256)", "address")
)

// All contract reads use one explicit block. This is a warning snapshot, not a
// promise that transfers stop while an administrator considers the request.
func (a *API) retirementPreview(ctx context.Context, tx pgx.Tx, resource store.RetirementResource) (store.RetirementPreview, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if a.retirementPreviewReader != nil {
		return a.retirementPreviewReader(ctx, tx, resource)
	}
	preview := store.RetirementPreview{ResourceType: resource.ResourceType, ResourceAddress: resource.Address,
		ChainID: resource.ChainID, OwnerAddress: resource.OwnerAddress, Active: resource.Active, Symbol: resource.Symbol,
		Decimals: resource.Decimals, CheckedAt: time.Now().UTC()}
	if a.chainProvider == nil || a.chainProvider.Client == nil {
		return preview, errors.New("chain balance checks are unavailable")
	}
	var chainID uint64
	var block *big.Int
	if err := a.chainProvider.Client.CallCtx(ctx, eth.ChainID().Returns(&chainID), eth.BlockNumber().Returns(&block)); err != nil {
		return preview, err
	}
	if resource.ChainID < 1 || chainID != uint64(resource.ChainID) {
		return preview, errors.New("balance provider is on a different chain")
	}
	preview.BlockNumber = block.String()
	var owner common.Address
	address := common.HexToAddress(resource.Address)
	if err := a.chainProvider.Client.CallCtx(ctx, eth.CallFunc(address, retirementOwnerFunc).AtBlock(block).Returns(&owner)); err != nil {
		return preview, fmt.Errorf("check current owner: %w", err)
	}
	if owner == (common.Address{}) || !strings.EqualFold(owner.Hex(), resource.OwnerAddress) {
		return preview, errRetirementOwnerChanged
	}
	if resource.ResourceType == "voucher" {
		var supply, balance *big.Int
		var decimals uint8
		if err := a.chainProvider.Client.CallCtx(ctx,
			eth.CallFunc(address, retirementSupplyFunc).AtBlock(block).Returns(&supply),
			eth.CallFunc(address, retirementBalanceFunc, owner).AtBlock(block).Returns(&balance),
			eth.CallFunc(address, retirementDecimalsFunc).AtBlock(block).Returns(&decimals),
		); err != nil {
			return preview, err
		}
		if supply == nil || balance == nil {
			return preview, errors.New("balance check returned no value")
		}
		if balance.Cmp(supply) > 0 {
			return preview, errors.New("owner balance exceeds total supply")
		}
		amount := new(big.Int).Sub(supply, balance).String()
		preview.AmountOutsideOwnerRaw = &amount
		preview.Decimals = int(decimals)
		preview.HasPositiveBalances = amount != "0"
		preview.Coverage = "total-supply"
		return preview, nil
	}
	addresses, err := a.retirementPoolTokenAddresses(ctx, tx, resource, block)
	if err != nil {
		return preview, err
	}
	count := 0
	// Bounded batches keep RPC requests small without truncating the inventory.
	for start := 0; start < len(addresses); start += 100 {
		batch := addresses[start:min(start+100, len(addresses))]
		balances := make([]*big.Int, len(batch))
		calls := make([]w3types.RPCCaller, len(batch))
		for i, token := range batch {
			calls[i] = eth.CallFunc(common.HexToAddress(token), retirementBalanceFunc, address).AtBlock(block).Returns(&balances[i])
		}
		if err := a.chainProvider.Client.CallCtx(ctx, calls...); err != nil {
			return preview, fmt.Errorf("read pool token balances: %w", err)
		}
		for i := range balances {
			if balances[i] == nil {
				return preview, errors.New("pool balance check returned no value")
			}
			if balances[i].Sign() > 0 {
				count++
			}
		}
	}
	preview.PositiveTokenCount = &count
	preview.HasPositiveBalances = count > 0
	preview.Coverage = "known-tokens"
	note := "Counts registered vouchers and tokens found in indexed transfers. Untracked tokens or transfers not yet indexed may be missing."
	preview.CoverageNote = &note
	return preview, nil
}

func (a *API) retirementPoolTokenAddresses(ctx context.Context, tx pgx.Tx, resource store.RetirementResource, block *big.Int) ([]string, error) {
	if a.federationStore == nil {
		return nil, errors.New("pool token discovery is unavailable")
	}
	addresses, err := store.RetirementPoolTokens(ctx, tx, resource.ID)
	if err != nil {
		return nil, err
	}
	// Unlike visible holdings, this reads every distinct observed token, including
	// historical zero/negative indexed sums. Current balances come from the RPC.
	// The federation schema has no chain column. Deployment configuration must
	// connect the indexer for the same chain as Core's RPC; RPC chain validation
	// alone cannot prove that the federation connection is correct.
	var indexed []string
	err = pgxscan.Select(ctx, a.federationStore.Pool(), &indexed, `SELECT DISTINCT contract_address FROM (
        SELECT t.contract_address FROM token_transfer t JOIN tx ON tx.id=t.tx_id
        WHERE t.recipient_address=$1 AND tx.success
        UNION
        SELECT t.contract_address FROM token_transfer t JOIN tx ON tx.id=t.tx_id
        WHERE t.sender_address=$1 AND tx.success
    ) known LIMIT $2`, common.HexToAddress(resource.Address).Hex(), retirementMaxTokens+1)
	if err != nil {
		return nil, err
	}
	if len(indexed) > retirementMaxTokens {
		return nil, errors.New("pool token discovery exceeds the supported limit")
	}
	addresses = append(addresses, indexed...)
	var registry common.Address
	if err = a.chainProvider.Client.CallCtx(ctx, eth.CallFunc(common.HexToAddress(resource.Address), retirementRegistryFunc).AtBlock(block).Returns(&registry)); err != nil {
		return nil, err
	}
	if registry != (common.Address{}) {
		var count *big.Int
		if err = a.chainProvider.Client.CallCtx(ctx, eth.CallFunc(registry, retirementEntryCountFunc).AtBlock(block).Returns(&count)); err != nil {
			return nil, err
		}
		if count == nil || !count.IsInt64() || count.Sign() < 0 || count.Int64() > retirementMaxTokens {
			return nil, errors.New("pool registry exceeds the supported limit")
		}
		for start := 0; start < int(count.Int64()); start += 100 {
			n := min(100, int(count.Int64())-start)
			entries := make([]common.Address, n)
			calls := make([]w3types.RPCCaller, n)
			for i := range n {
				calls[i] = eth.CallFunc(registry, retirementEntryFunc, big.NewInt(int64(start+i))).AtBlock(block).Returns(&entries[i])
			}
			if err = a.chainProvider.Client.CallCtx(ctx, calls...); err != nil {
				return nil, err
			}
			for _, entry := range entries {
				addresses = append(addresses, entry.Hex())
			}
		}
	}
	unique := map[string]bool{}
	for _, address := range addresses {
		if !common.IsHexAddress(address) {
			return nil, errors.New("pool token discovery returned an invalid address")
		}
		if common.HexToAddress(address) == (common.Address{}) {
			continue
		}
		unique[strings.ToLower(address)] = true
	}
	if len(unique) > retirementMaxTokens {
		return nil, errors.New("pool token discovery exceeds the supported limit")
	}
	addresses = make([]string, 0, len(unique))
	for address := range unique {
		addresses = append(addresses, address)
	}
	slices.Sort(addresses)
	return addresses, nil
}
