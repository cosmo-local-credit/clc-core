package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/ethereum/go-ethereum/common"
	"github.com/grassrootseconomics/ethutils"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type (
	retirementFederation struct {
		store.FederationStore

		pool *pgxpool.Pool
	}
)

func (f retirementFederation) Pool() *pgxpool.Pool { return f.pool }

func TestRetirementPoolPreviewCompleteKnownInventory(t *testing.T) {
	a, _ := newOfferingAPI(t)
	fx, cleanup := seedOfferingFixtures(t, a)
	defer cleanup()
	ctx := context.Background()
	tx, err := a.store.Pool().Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	var poolID uint64
	poolAddress := "0x3333333333333333333333333333333333333333"
	require.NoError(t, tx.QueryRow(ctx, `INSERT INTO pools(chain_id,address,owner_address) VALUES(100,$1,$2) RETURNING id`, poolAddress, offeringOwnerAddress).Scan(&poolID))
	_, err = tx.Exec(ctx, `INSERT INTO pool_tokens(pool_id,voucher_id,is_allowed) SELECT $1,id,false FROM vouchers WHERE address=$2`, poolID, fx.inactiveAddress)
	require.NoError(t, err)
	// A one-connection federation pool keeps these tables private to this test.
	config, err := pgxpool.ParseConfig(os.Getenv("CORE_TEST_DSN"))
	require.NoError(t, err)
	config.MaxConns = 1
	federation, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err)
	defer federation.Close()
	_, err = federation.Exec(ctx, `CREATE TEMP TABLE tx(id INT,success BOOL); CREATE TEMP TABLE token_transfer(tx_id INT,sender_address TEXT,recipient_address TEXT,contract_address TEXT); INSERT INTO tx VALUES(1,true)`)
	require.NoError(t, err)
	tokens := make([]string, 130)
	for i := range tokens {
		tokens[i] = common.BigToAddress(big.NewInt(int64(1000 + i))).Hex()
	}
	_, err = federation.Exec(ctx, `INSERT INTO token_transfer SELECT 1,$1,$2,token FROM unnest($3::text[]) token`, offeringOwnerAddress, poolAddress, tokens)
	require.NoError(t, err)
	// Duplicate transfer rows and retired/disallowed membership must not alter counts.
	_, err = federation.Exec(ctx, `INSERT INTO token_transfer VALUES(1,$1,$2,$3)`, offeringOwnerAddress, poolAddress, tokens[129])
	require.NoError(t, err)
	registry := "0x4444444444444444444444444444444444444444"
	registryOnly := "0x5555555555555555555555555555555555555555"
	entries := []string{tokens[129], registryOnly}
	var failBalance atomic.Bool
	client := retirementRPCClient(t, func(request retirementRPCRequest) (string, error) {
		if request.Method == "eth_chainId" {
			return "0x64", nil
		}
		if request.Method == "eth_blockNumber" {
			return "0x2a", nil
		}
		if request.Method != "eth_call" {
			return "", fmt.Errorf("unexpected method %s", request.Method)
		}
		var call map[string]string
		if err := json.Unmarshal(request.Params[0], &call); err != nil {
			return "", err
		}
		var block string
		if err := json.Unmarshal(request.Params[1], &block); err != nil {
			return "", err
		}
		if block != "0x2a" {
			return "", errors.New("balance read used a different block")
		}
		input := call["data"]
		if input == "" {
			input = call["input"]
		}
		if len(input) < 10 {
			return "", errors.New("missing selector")
		}
		addressResult := func(address string) string {
			return "0x" + hex.EncodeToString(common.LeftPadBytes(common.HexToAddress(address).Bytes(), 32))
		}
		numberResult := func(n int64) string { return "0x" + hex.EncodeToString(common.LeftPadBytes(big.NewInt(n).Bytes(), 32)) }
		ownerCall, _ := retirementOwnerFunc.EncodeArgs()
		registryCall, _ := retirementRegistryFunc.EncodeArgs()
		countCall, _ := retirementEntryCountFunc.EncodeArgs()
		entryCall, _ := retirementEntryFunc.EncodeArgs(big.NewInt(0))
		switch input[:10] {
		case "0x" + hex.EncodeToString(ownerCall[:4]):
			return addressResult(offeringOwnerAddress), nil
		case "0x" + hex.EncodeToString(registryCall[:4]):
			return addressResult(registry), nil
		case "0x" + hex.EncodeToString(countCall[:4]):
			return numberResult(int64(len(entries))), nil
		case "0x" + hex.EncodeToString(entryCall[:4]):
			index, ok := new(big.Int).SetString(input[10:], 16)
			if !ok || !index.IsInt64() || index.Int64() >= int64(len(entries)) {
				return "", errors.New("bad entry")
			}
			return addressResult(entries[index.Int64()]), nil
		case "0x70a08231":
			if failBalance.Load() {
				return "", errors.New("token unavailable")
			}
			for _, positive := range []string{tokens[129], fx.inactiveAddress, registryOnly} {
				if strings.EqualFold(call["to"], positive) {
					return numberResult(1), nil
				}
			}
			return numberResult(0), nil
		}
		return "", fmt.Errorf("unexpected selector %s", input[:10])
	})
	a.chainProvider = &ethutils.Provider{Client: client}
	a.federationStore = retirementFederation{pool: federation}
	resource, err := store.GetRetirementResource(ctx, tx, "pool", poolAddress, 100, false)
	require.NoError(t, err)
	preview, err := a.retirementPreview(ctx, tx, resource)
	require.NoError(t, err)
	require.NotNil(t, preview.PositiveTokenCount)
	require.Equal(t, 3, *preview.PositiveTokenCount)
	require.True(t, preview.HasPositiveBalances)
	require.Equal(t, "known-tokens", preview.Coverage)
	require.NotNil(t, preview.CoverageNote)
	failBalance.Store(true)
	_, err = a.retirementPreview(ctx, tx, resource)
	require.Error(t, err, "an unreadable token must not become a zero balance")
}
