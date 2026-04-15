package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/internal/worker"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/riverqueue/river"
)

func (a *API) contractsERC20Handler(c *echo.Context) error {
	req := apiresp.NextERC20DeployRequest{}

	if err := c.Bind(&req); err != nil {
		return handleBindError(c)
	}

	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	tx, err := a.store.Pool().Begin(c.Request().Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(c.Request().Context())

	trackingID := uuid.NewString()

	initialSupply, err := strconv.ParseInt(req.InitialSupply, 10, 64)
	if err != nil {
		return a.handleValidateError(c, err)
	}

	expiryTimestamp := int64(0)
	if req.ExpiryTimestamp != "" {
		expiryTimestamp, err = strconv.ParseInt(req.ExpiryTimestamp, 10, 64)
		if err != nil {
			return a.handleValidateError(c, err)
		}
	}

	err = a.store.InsertNextERC20Workflow(c.Request().Context(), tx, store.NextERC20Workflow{
		TrackingID:            trackingID,
		Action:                store.NextERC20ActionDeploy,
		Status:                store.NextERC20StatusPending,
		Name:                  req.Name,
		Symbol:                req.Symbol,
		Decimals:              int(req.Decimals),
		InitialSupply:         initialSupply,
		InitialMintee:         req.InitialMintee,
		Owner:                 req.Owner,
		ExpiryTimestamp:       expiryTimestamp,
		FactoryAddress:        req.FactoryAddress,
		AdminAddress:          req.ProxyAdminAddress,
		ImplementationAddress: req.ImplementationAddress,
		Version:               1,
	})
	if err != nil {
		return handlePostgresError(c, err)
	}

	_, err = a.queueClient.InsertTx(c.Request().Context(), tx, worker.DeployERC20Args{
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
		return a.handleValidateError(c, err)
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

	if latestWorkflow.Status == store.NextERC20StatusPending || latestWorkflow.Status == store.NextERC20StatusRunning {
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

	err = a.store.InsertNextERC20Workflow(c.Request().Context(), tx, store.NextERC20Workflow{
		TrackingID:                    trackingID,
		Action:                        store.NextERC20ActionUpgrade,
		Status:                        store.NextERC20StatusPending,
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
		ImplementationAddress:         req.ImplementationAddress,
		PreviousImplementationAddress: currentWorkflow.ImplementationAddress,
		Version:                       nextVersion,
		UpgradeCalldata:               strings.TrimSpace(req.UpgradeCalldata),
	})
	if err != nil {
		return handlePostgresError(c, err)
	}

	_, err = a.queueClient.InsertTx(c.Request().Context(), tx, worker.UpgradeERC20Args{
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
