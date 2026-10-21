// Reconcile catalog authority from a finalized on-chain snapshot. Dry-run is
// the default; CORE_RECONCILE_DSN keeps credentials out of process arguments.
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/big"
	"os"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/jackc/pgx/v5/pgxpool"
)

type (
	snapshot struct {
		Address       string  `json:"address"`
		PreviousOwner *string `json:"previousOwner"`
		Owner         string  `json:"owner"`
		Block         uint64  `json:"block"`
		BlockHash     string  `json:"blockHash"`
	}
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	rpcURL := flag.String("rpc", "", "RPC for the catalog chain (archive reads required)")
	chainID := flag.Int64("chain", 0, "configured catalog chain ID")
	block := flag.Uint64("block", 0, "explicit finalized snapshot block")
	apply := flag.Bool("apply", false, "apply the snapshot atomically; default is dry-run")
	flag.Parse()
	if *rpcURL == "" || *chainID <= 0 || *block == 0 || *block > math.MaxInt64 || os.Getenv("CORE_RECONCILE_DSN") == "" {
		return fmt.Errorf("--rpc, --chain, --block and CORE_RECONCILE_DSN are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	client, err := ethclient.DialContext(ctx, *rpcURL)
	if err != nil {
		return err
	}
	defer client.Close()
	actualChain, err := client.ChainID(ctx)
	if err != nil {
		return err
	}
	if actualChain.Cmp(big.NewInt(*chainID)) != 0 {
		return fmt.Errorf("RPC chain does not match --chain")
	}
	finalized, err := client.HeaderByNumber(ctx, big.NewInt(int64(rpc.FinalizedBlockNumber)))
	if err != nil {
		return fmt.Errorf("read finalized head: %w", err)
	}
	if finalized.Number.Uint64() < *block {
		return fmt.Errorf("snapshot block is not finalized")
	}
	height := new(big.Int).SetUint64(*block)
	header, err := client.HeaderByNumber(ctx, height)
	if err != nil {
		return err
	}
	db, err := pgxpool.New(ctx, os.Getenv("CORE_RECONCILE_DSN"))
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.Query(ctx, `SELECT address, owner_address FROM vouchers WHERE chain_id = $1
        UNION SELECT address, owner_address FROM pools WHERE chain_id = $1 ORDER BY address`, *chainID)
	if err != nil {
		return err
	}
	var assets []snapshot
	for rows.Next() {
		var asset snapshot
		if err := rows.Scan(&asset.Address, &asset.PreviousOwner); err != nil {
			rows.Close()
			return err
		}
		asset.Block, asset.BlockHash = *block, header.Hash().Hex()
		assets = append(assets, asset)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range assets {
		asset := &assets[i]
		if !common.IsHexAddress(asset.Address) {
			return fmt.Errorf("invalid catalog address %q", asset.Address)
		}
		address := common.HexToAddress(asset.Address)
		data, err := client.CallContract(ctx, ethereum.CallMsg{To: &address, Data: common.FromHex("0x8da5cb5b")}, height)
		if err != nil {
			return fmt.Errorf("owner() for %s: %w", asset.Address, err)
		}
		if len(data) != 32 || hex.EncodeToString(data[:12]) != "000000000000000000000000" {
			return fmt.Errorf("invalid owner() response for %s", asset.Address)
		}
		asset.Owner = common.BytesToAddress(data[12:]).Hex()
		if err := json.NewEncoder(os.Stdout).Encode(asset); err != nil {
			return err
		}
	}
	again, err := client.HeaderByNumber(ctx, height)
	if err != nil {
		return err
	}
	if again.Hash() != header.Hash() {
		return fmt.Errorf("snapshot block changed; refusing reconciliation")
	}
	if !*apply {
		fmt.Fprintf(os.Stderr, "Dry-run: %d assets at block %d (%s). Use --apply to reconcile.\n", len(assets), *block, header.Hash())
		return nil
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	pg := &store.Pg{}
	for _, asset := range assets {
		// End-of-block snapshot dominates every log at this height, but never
		// overwrites a later event that the subscriber has already projected.
		if _, err := pg.ProjectCatalogOwnership(ctx, tx, *chainID, asset.Address, asset.Owner, *block, math.MaxInt64); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Reconciled %d assets at block %d (%s).\n", len(assets), *block, header.Hash())
	return nil
}
