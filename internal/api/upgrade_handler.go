package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/cosmo-local-credit/clc-core/internal/worker"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/grassrootseconomics/ethutils"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
	"github.com/lmittmann/w3/module/eth"
	"github.com/riverqueue/river"
)

var (
	// keccak256("eip1967.proxy.implementation") - 1, the slot ERC1967Factory
	// writes a proxy's implementation to.
	eip1967ImplementationSlot = common.HexToHash("0x360894a13ba1a3210667c828492db98dca3e2076cc3735a920a3ca505d382bbc")
)

type (
	upgradeTask struct {
		ContractType   string
		ProxyAddress   string
		NewImplAddress string
		FactoryAddress string
		Version        string
	}

	// skippedUpgrade reports a contract the caller might have expected to be
	// upgraded. An upgrade endpoint that silently drops a contract reads as a
	// success, and the pool stays on an implementation nobody is tracking.
	skippedUpgrade struct {
		Type    string `json:"type"`
		Address string `json:"address"`
		Reason  string `json:"reason"`
	}
)

// quoterContractType maps a pool's recorded quoter_type to the platform
// contract whose implementation that proxy may be upgraded to. The shared
// decimal quoter is stateless and not deployed behind a proxy, so it has no
// entry; neither does an absent or unrecognized type.
func quoterContractType(quoterType *string) string {
	if quoterType == nil {
		return ""
	}
	switch *quoterType {
	case "oracle":
		return "ORACLE_QUOTER"
	case "relative":
		return "RELATIVE_QUOTER"
	default:
		return ""
	}
}

// dropCurrentImplementations removes tasks whose proxy already runs the target
// implementation. The read is best effort: a proxy whose slot cannot be read is
// left in the batch, since upgrading it again is wasteful but harmless, while
// skipping it on a failed read would silently leave it behind.
func (a *API) dropCurrentImplementations(ctx context.Context, tasks []upgradeTask) ([]upgradeTask, []skippedUpgrade) {
	if a.chainProvider == nil || a.chainProvider.Client == nil {
		return tasks, nil
	}

	var (
		kept    []upgradeTask
		skipped []skippedUpgrade
	)
	for _, task := range tasks {
		var current common.Hash
		if err := a.chainProvider.Client.CallCtx(ctx,
			eth.StorageAt(common.HexToAddress(task.ProxyAddress), eip1967ImplementationSlot, nil).Returns(&current),
		); err != nil {
			a.logg.Warn("upgrade: could not read implementation slot", "proxy", task.ProxyAddress, "error", err)
			kept = append(kept, task)
			continue
		}

		if common.BytesToAddress(current.Bytes()) == common.HexToAddress(task.NewImplAddress) {
			skipped = append(skipped, skippedUpgrade{
				Type:    task.ContractType,
				Address: task.ProxyAddress,
				Reason:  "already on the current implementation",
			})
			continue
		}
		kept = append(kept, task)
	}

	return kept, skipped
}

// skippedOrEmpty keeps the field a JSON array rather than null, so a client can
// iterate it without a nil check.
func skippedOrEmpty(skipped []skippedUpgrade) []skippedUpgrade {
	if skipped == nil {
		return []skippedUpgrade{}
	}
	return skipped
}

// contractsVersionHandler handles POST /contracts/version.
// Updates platform_contracts rows with new implementation addresses and version string.
func (a *API) contractsVersionHandler(c *echo.Context) error {
	serviceKey, _ := c.Get("service").(bool)
	if !serviceKey {
		return c.JSON(http.StatusForbidden, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "Insufficient permissions: service token required",
		})
	}

	req := apiresp.RegisterVersionRequest{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	// Validate implementation address is a non-zero hex address.
	if !common.IsHexAddress(req.ImplementationAddress) {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "Validation failed: implementationAddress is not a valid hex address: " + req.ImplementationAddress,
		})
	}
	if common.HexToAddress(req.ImplementationAddress) == ethutils.ZeroAddress {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "Validation failed: implementationAddress must not be the zero address",
		})
	}

	ctx := c.Request().Context()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	pc, err := a.store.SetPlatformContracts(ctx, tx, a.chainID, req.ContractType, req.ImplementationAddress, req.Version)
	if err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Contract version updated",
		Result: map[string]any{
			"contractType":          pc.ContractType,
			"implementationAddress": pc.ImplementationAddress,
			"version":               pc.Version,
		},
	})
}

// upgradeTokenHandler handles POST /contracts/upgrade/token.
// Upgrades a single token proxy to the current ERC20 implementation in platform_contracts.
func (a *API) upgradeTokenHandler(c *echo.Context) error {
	serviceKey, _ := c.Get("service").(bool)
	callerRole, _ := c.Get("callerRole").(string)
	if !serviceKey && callerRole != "SUPERADMIN" {
		return handleForbiddenError(c, "superadmin access or service token required")
	}

	req := apiresp.UpgradeTokenRequest{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	ctx := c.Request().Context()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Verify token exists.
	_, err = a.store.GetDeployedTokenByAddress(ctx, tx, req.Address)
	if err != nil {
		return handlePostgresError(c, err)
	}

	// Get current ERC20 platform contract.
	pc, err := a.store.GetPlatformContracts(ctx, tx, a.chainID, "ERC20")
	if err != nil {
		return handlePostgresError(c, err)
	}

	trackingID := uuid.NewString()

	_, err = a.queueClient.InsertTx(ctx, tx, worker.UpgradeProxyArgs{
		TrackingID:     trackingID,
		ProxyAddress:   req.Address,
		NewImplAddress: pc.ImplementationAddress,
		FactoryAddress: pc.FactoryAddress,
		ContractType:   "ERC20",
		Version:        pc.Version,
	}, &river.InsertOpts{MaxAttempts: 1})
	if err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Token upgrade request accepted",
		Result: map[string]any{
			"trackingId": trackingID,
			"address":    req.Address,
		},
	})
}

// upgradePoolHandler handles POST /contracts/upgrade/pool.
// Upgrades a single pool proxy and its sub-contracts to their current implementations.
func (a *API) upgradePoolHandler(c *echo.Context) error {
	serviceKey, _ := c.Get("service").(bool)
	callerRole, _ := c.Get("callerRole").(string)
	if !serviceKey && callerRole != "SUPERADMIN" {
		return handleForbiddenError(c, "superadmin access or service token required")
	}

	req := apiresp.UpgradePoolRequest{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	ctx := c.Request().Context()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Verify pool exists.
	pool, err := a.store.GetDeployedPoolByAddress(ctx, tx, req.Address)
	if err != nil {
		return handlePostgresError(c, err)
	}

	// Get all platform contracts for this chain.
	allPCs, err := a.store.ListPlatformContracts(ctx, tx, a.chainID)
	if err != nil {
		return handlePostgresError(c, err)
	}

	pcMap := make(map[string]struct {
		ImplAddress    string
		FactoryAddress string
		Version        string
	})
	for _, pc := range allPCs {
		pcMap[pc.ContractType] = struct {
			ImplAddress    string
			FactoryAddress string
			Version        string
		}{pc.ImplementationAddress, pc.FactoryAddress, pc.Version}
	}

	// Resolve the decimal_quoter address to detect non-proxy quoters.
	decimalQuoterAddr, err := a.store.GetProtocolAddress(ctx, tx, "decimal_quoter")
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return handlePostgresError(c, err)
	}

	var (
		tasks   []upgradeTask
		skipped []skippedUpgrade
	)

	// SWAPPOOL — the pool proxy itself.
	if pc, ok := pcMap["SWAPPOOL"]; ok {
		tasks = append(tasks, upgradeTask{
			ContractType:   "SWAPPOOL",
			ProxyAddress:   req.Address,
			NewImplAddress: pc.ImplAddress,
			FactoryAddress: pc.FactoryAddress,
			Version:        pc.Version,
		})
	}

	// TOKEN_UNIQUE_SYMBOL_INDEX
	if pc, ok := pcMap["TOKEN_UNIQUE_SYMBOL_INDEX"]; ok && pool.TokenRegistryAddress != "" {
		tasks = append(tasks, upgradeTask{
			ContractType:   "TOKEN_UNIQUE_SYMBOL_INDEX",
			ProxyAddress:   pool.TokenRegistryAddress,
			NewImplAddress: pc.ImplAddress,
			FactoryAddress: pc.FactoryAddress,
			Version:        pc.Version,
		})
	}

	// LIMITER
	if pc, ok := pcMap["LIMITER"]; ok && pool.LimiterAddress != "" {
		tasks = append(tasks, upgradeTask{
			ContractType:   "LIMITER",
			ProxyAddress:   pool.LimiterAddress,
			NewImplAddress: pc.ImplAddress,
			FactoryAddress: pc.FactoryAddress,
			Version:        pc.Version,
		})
	}

	// FEE_POLICY
	if pc, ok := pcMap["FEE_POLICY"]; ok && pool.FeePolicyAddress != "" {
		tasks = append(tasks, upgradeTask{
			ContractType:   "FEE_POLICY",
			ProxyAddress:   pool.FeePolicyAddress,
			NewImplAddress: pc.ImplAddress,
			FactoryAddress: pc.FactoryAddress,
			Version:        pc.Version,
		})
	}

	// Quoter — the pool's own quoter_type picks the implementation. Taking
	// whichever of the two rows happened to exist in platform_contracts sent
	// every RelativeQuoter to the OracleQuoter implementation: the layouts
	// disagree from slot 0 (priceIndex vs oracles/baseCurrency), so the
	// upgraded quoter reads an unset oracle for every token and reverts,
	// taking every swap in the pool with it. An unknown type is skipped
	// rather than guessed. The shared decimal_quoter is not a proxy at all.
	if pool.QuoterAddress != "" && !strings.EqualFold(pool.QuoterAddress, decimalQuoterAddr) {
		quoterType := quoterContractType(pool.QuoterType)
		pc, known := pcMap[quoterType]
		switch {
		case quoterType == "":
			skipped = append(skipped, skippedUpgrade{
				Type:    "QUOTER",
				Address: pool.QuoterAddress,
				Reason:  "pool has no recorded quoterType; upgrade it explicitly",
			})
		case known:
			tasks = append(tasks, upgradeTask{
				ContractType:   quoterType,
				ProxyAddress:   pool.QuoterAddress,
				NewImplAddress: pc.ImplAddress,
				FactoryAddress: pc.FactoryAddress,
				Version:        pc.Version,
			})
		}
	}

	// Re-pointing a proxy at the implementation it already runs costs a
	// transaction and a nonce to change nothing. A pool fans out to five
	// contracts, so a version bump that touches one of them would otherwise
	// spend four wasted transactions per pool.
	tasks, alreadyCurrent := a.dropCurrentImplementations(ctx, tasks)
	skipped = append(skipped, alreadyCurrent...)

	if len(tasks) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return handlePostgresError(c, err)
		}
		return c.JSON(http.StatusOK, apiresp.OKResponse{
			Ok:          true,
			Description: "No upgrade tasks found for this pool",
			Result: map[string]any{
				"upgrades": []any{},
				"skipped":  skippedOrEmpty(skipped),
			},
		})
	}

	trackingID := uuid.NewString()

	insertParams := make([]river.InsertManyParams, 0, len(tasks))
	for _, task := range tasks {
		insertParams = append(insertParams, river.InsertManyParams{
			Args: worker.UpgradeProxyArgs{
				TrackingID:     trackingID,
				ProxyAddress:   task.ProxyAddress,
				NewImplAddress: task.NewImplAddress,
				FactoryAddress: task.FactoryAddress,
				ContractType:   task.ContractType,
				Version:        task.Version,
			},
			InsertOpts: &river.InsertOpts{MaxAttempts: 1},
		})
	}

	if _, err := a.queueClient.InsertManyTx(ctx, tx, insertParams); err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	type upgradeResult struct {
		Type    string `json:"type"`
		Address string `json:"address"`
	}
	upgradeResults := make([]upgradeResult, 0, len(tasks))
	for _, task := range tasks {
		upgradeResults = append(upgradeResults, upgradeResult{
			Type:    task.ContractType,
			Address: task.ProxyAddress,
		})
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Pool upgrade request accepted",
		Result: map[string]any{
			"trackingId": trackingID,
			"upgrades":   upgradeResults,
			"skipped":    skippedOrEmpty(skipped),
		},
	})
}
