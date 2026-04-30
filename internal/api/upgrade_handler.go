package api

import (
	"errors"
	"net/http"

	"github.com/cosmo-local-credit/clc-core/internal/worker"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/grassrootseconomics/ethutils"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
	"github.com/riverqueue/river"
)

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
	if !serviceKey {
		return c.JSON(http.StatusForbidden, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "Insufficient permissions: service token required",
		})
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
	if !serviceKey {
		return c.JSON(http.StatusForbidden, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "Insufficient permissions: service token required",
		})
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
	decimalQuoterAddr, err := a.store.GetPoolAddress(ctx, tx, "decimal_quoter")
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return handlePostgresError(c, err)
	}

	type upgradeTask struct {
		ContractType   string
		ProxyAddress   string
		NewImplAddress string
		FactoryAddress string
		Version        string
	}

	var tasks []upgradeTask

	// SWAPPOOL — the pool proxy itself.
	if pc, ok := pcMap["SWAPPOOL"]; ok {
		tasks = append(tasks, upgradeTask{
			ContractType:   "SWAPPOOL",
			ProxyAddress:   pool.Address,
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

	// Quoter — skip if it's the shared decimal_quoter singleton (not a proxy).
	if pool.QuoterAddress != "" && pool.QuoterAddress != decimalQuoterAddr {
		// Check ORACLE_QUOTER first, then RELATIVE_QUOTER.
		if pc, ok := pcMap["ORACLE_QUOTER"]; ok {
			tasks = append(tasks, upgradeTask{
				ContractType:   "ORACLE_QUOTER",
				ProxyAddress:   pool.QuoterAddress,
				NewImplAddress: pc.ImplAddress,
				FactoryAddress: pc.FactoryAddress,
				Version:        pc.Version,
			})
		} else if pc, ok := pcMap["RELATIVE_QUOTER"]; ok {
			tasks = append(tasks, upgradeTask{
				ContractType:   "RELATIVE_QUOTER",
				ProxyAddress:   pool.QuoterAddress,
				NewImplAddress: pc.ImplAddress,
				FactoryAddress: pc.FactoryAddress,
				Version:        pc.Version,
			})
		}
	}

	if len(tasks) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return handlePostgresError(c, err)
		}
		return c.JSON(http.StatusOK, apiresp.OKResponse{
			Ok:          true,
			Description: "No upgrade tasks found for this pool",
			Result: map[string]any{
				"upgrades": []any{},
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
		},
	})
}
