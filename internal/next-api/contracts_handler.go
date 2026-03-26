package nextapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	nextstore "github.com/cosmo-local-credit/clc-core/internal/next-store"
	nextworker "github.com/cosmo-local-credit/clc-core/internal/next-worker"
	"github.com/cosmo-local-credit/clc-core/internal/worker"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/grassrootseconomics/ethutils"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
	"github.com/lmittmann/w3"
	"github.com/lmittmann/w3/module/eth"
	"github.com/riverqueue/river"
)

func (a *API) contractsERC20Handler(c *echo.Context) error {
	req := apiresp.NextERC20DeployRequest{}

	if err := c.Bind(&req); err != nil {
		return handleBindError(c)
	}

	if err := c.Validate(req); err != nil {
		return handleValidateError(c)
	}

	tx, err := a.store.Pool().Begin(c.Request().Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(c.Request().Context())

	existingWorkflow, err := a.store.GetLatestNextERC20WorkflowBySymbol(c.Request().Context(), tx, req.Symbol)
	if err == nil && existingWorkflow.Status != nextstore.ERC20StatusFailed {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			Description: "Token with this symbol already has an active or completed next deployment",
			ErrCode:     apiresp.ErrSymbolAlreadyExists,
		})
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return handlePostgresError(c, err)
	}

	exists, err := a.alreadyExists(c.Request().Context(), a.registry[ethutils.TokenIndex], req.Symbol)
	if err != nil {
		return err
	}
	if exists {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			Description: "Token with this symbol already exists",
			ErrCode:     apiresp.ErrSymbolAlreadyExists,
		})
	}

	trackingID := uuid.NewString()

	initialSupply, err := strconv.ParseInt(req.InitialSupply, 10, 64)
	if err != nil {
		return handleValidateError(c)
	}

	expiryTimestamp := int64(0)
	if req.ExpiryTimestamp != "" {
		expiryTimestamp, err = strconv.ParseInt(req.ExpiryTimestamp, 10, 64)
		if err != nil {
			return handleValidateError(c)
		}
	}

	err = a.store.InsertNextERC20Workflow(c.Request().Context(), tx, nextstore.ERC20Workflow{
		TrackingID:      trackingID,
		Action:          nextstore.ERC20ActionDeploy,
		Status:          nextstore.ERC20StatusPending,
		Name:            req.Name,
		Symbol:          req.Symbol,
		Decimals:        int(req.Decimals),
		InitialSupply:   initialSupply,
		InitialMintee:   req.InitialMintee,
		Owner:           req.Owner,
		ExpiryTimestamp: expiryTimestamp,
		FactoryAddress:  req.FactoryAddress,
		AdminAddress:    req.AdminAddress,
		Version:         1,
	})
	if err != nil {
		return handlePostgresError(c, err)
	}

	_, err = a.queueClient.InsertTx(c.Request().Context(), tx, nextworker.DeployERC20Args{
		TrackingID: trackingID,
	}, &river.InsertOpts{MaxAttempts: 1})
	if err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(c.Request().Context()); err != nil {
		return handlePostgresError(c, err)
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "ERC20 deploy request successfully created",
		Result: map[string]any{
			"trackingId": trackingID,
		},
	})
}

func (a *API) contractsERC20UpgradeHandler(c *echo.Context) error {
	req := apiresp.NextERC20UpgradeRequest{}

	if err := c.Bind(&req); err != nil {
		return handleBindError(c)
	}

	if err := c.Validate(req); err != nil {
		return handleValidateError(c)
	}

	tx, err := a.store.Pool().Begin(c.Request().Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(c.Request().Context())

	latestWorkflow, err := a.store.GetLatestNextERC20WorkflowByProxy(c.Request().Context(), tx, req.ProxyAddress)
	if err != nil {
		return handlePostgresError(c, err)
	}

	if latestWorkflow.Status == nextstore.ERC20StatusPending || latestWorkflow.Status == nextstore.ERC20StatusRunning {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			Description: "Proxy already has an in-flight next workflow",
			ErrCode:     apiresp.ErrCodeValidationFailed,
		})
	}

	currentWorkflow, err := a.store.GetLatestSuccessfulNextERC20WorkflowByProxy(c.Request().Context(), tx, req.ProxyAddress)
	if err != nil {
		return handlePostgresError(c, err)
	}

	trackingID := uuid.NewString()
	nextVersion := currentWorkflow.Version + 1

	err = a.store.InsertNextERC20Workflow(c.Request().Context(), tx, nextstore.ERC20Workflow{
		TrackingID:                    trackingID,
		Action:                        nextstore.ERC20ActionUpgrade,
		Status:                        nextstore.ERC20StatusPending,
		Name:                          currentWorkflow.Name,
		Symbol:                        currentWorkflow.Symbol,
		Decimals:                      currentWorkflow.Decimals,
		InitialSupply:                 currentWorkflow.InitialSupply,
		InitialMintee:                 currentWorkflow.InitialMintee,
		Owner:                         currentWorkflow.Owner,
		ExpiryTimestamp:               currentWorkflow.ExpiryTimestamp,
		FactoryAddress:                currentWorkflow.FactoryAddress,
		AdminAddress:                  currentWorkflow.AdminAddress,
		ProxyAddress:                  req.ProxyAddress,
		PreviousImplementationAddress: currentWorkflow.ImplementationAddress,
		Version:                       nextVersion,
		UpgradeCalldata:               strings.TrimSpace(req.UpgradeCalldata),
	})
	if err != nil {
		return handlePostgresError(c, err)
	}

	_, err = a.queueClient.InsertTx(c.Request().Context(), tx, nextworker.UpgradeERC20Args{
		TrackingID: trackingID,
	}, &river.InsertOpts{MaxAttempts: 1})
	if err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(c.Request().Context()); err != nil {
		return handlePostgresError(c, err)
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "ERC20 upgrade request successfully created",
		Result: map[string]any{
			"trackingId": trackingID,
		},
	})
}

func (a *API) contractsDemurrageERC20Handler(c *echo.Context) error {
	req := apiresp.DemurrageERC20DeployRequest{}

	if err := c.Bind(&req); err != nil {
		return handleBindError(c)
	}

	if err := c.Validate(req); err != nil {
		return handleValidateError(c)
	}

	poolIndex := a.registry[ethutils.PoolIndex]
	if a.prod {
		poolIndex = w3.A("0x01eD8Fe01a2Ca44Cb26D00b1309d7D777471D00C")
	}

	exists, err := a.alreadyExists(c.Request().Context(), poolIndex, req.Symbol)
	if err != nil {
		return err
	}
	if exists {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			Description: "Token with this symbol already exists",
			ErrCode:     apiresp.ErrSymbolAlreadyExists,
		})
	}

	tx, err := a.store.Pool().Begin(c.Request().Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(c.Request().Context())

	trackingID := uuid.NewString()

	_, err = a.queueClient.InsertTx(c.Request().Context(), tx, worker.DemurrageTokenDeployArgs{
		TrackingID:      trackingID,
		Name:            req.Name,
		Symbol:          req.Symbol,
		Decimals:        req.Decimals,
		InitialSupply:   req.InitialSupply,
		InitialMintee:   req.InitialMintee,
		Owner:           req.Owner,
		DemurrageRate:   req.DemurrageRate,
		DemurragePeriod: req.DemurragePeriod,
		SinkAddress:     req.SinkAddress,
	}, nil)
	if err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(c.Request().Context()); err != nil {
		return handlePostgresError(c, err)
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Demurrage ERC20 deploy request successfully created",
		Result: map[string]any{
			"trackingId": trackingID,
		},
	})
}

func (a *API) contractsPoolHandler(c *echo.Context) error {
	req := apiresp.PoolDeployRequest{}

	if err := c.Bind(&req); err != nil {
		return handleBindError(c)
	}

	if err := c.Validate(req); err != nil {
		return handleValidateError(c)
	}

	poolIndex := a.registry[ethutils.PoolIndex]
	if a.prod {
		poolIndex = w3.A("0x01eD8Fe01a2Ca44Cb26D00b1309d7D777471D00C")
	}

	exists, err := a.alreadyExists(c.Request().Context(), poolIndex, req.Symbol)
	if err != nil {
		return err
	}
	if exists {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			Description: "Token with this symbol already exists",
			ErrCode:     apiresp.ErrSymbolAlreadyExists,
		})
	}

	tx, err := a.store.Pool().Begin(c.Request().Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(c.Request().Context())

	trackingID := uuid.NewString()

	_, err = a.queueClient.InsertTx(c.Request().Context(), tx, worker.PoolDeployArgs{
		TrackingID: trackingID,
		Name:       req.Name,
		Symbol:     req.Symbol,
		Owner:      req.Owner,
	}, nil)
	if err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(c.Request().Context()); err != nil {
		return handlePostgresError(c, err)
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Pool deploy request successfully created",
		Result: map[string]any{
			"trackingId": trackingID,
		},
	})
}

func (a *API) alreadyExists(ctx context.Context, index common.Address, tokenSymbol string) (bool, error) {
	var address common.Address

	if err := a.chainProvider.Client.CallCtx(
		ctx,
		eth.CallFunc(index, worker.Abi[worker.AddressOf], common.BytesToHash(common.RightPadBytes([]byte(tokenSymbol), 32))).Returns(&address),
	); err != nil {
		return false, err
	}

	return address != common.Address{}, nil
}
