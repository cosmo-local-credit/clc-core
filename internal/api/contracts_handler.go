package api

import (
	"net/http"

	"github.com/cosmo-local-credit/clc-core/internal/worker"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/riverqueue/river"
)

func (a *API) deployPoolHandler(c *echo.Context) error {
	req := apiresp.DeployPoolRequest{}

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

	swapPoolPC, err := a.store.GetPlatformContracts(ctx, tx, a.chainID, "SWAPPOOL")
	if err != nil {
		return handlePostgresError(c, err)
	}

	tokenIndexPC, err := a.store.GetPlatformContracts(ctx, tx, a.chainID, "TOKEN_UNIQUE_SYMBOL_INDEX")
	if err != nil {
		return handlePostgresError(c, err)
	}

	limiterPC, err := a.store.GetPlatformContracts(ctx, tx, a.chainID, "LIMITER")
	if err != nil {
		return handlePostgresError(c, err)
	}

	feePolicyPC, err := a.store.GetPlatformContracts(ctx, tx, a.chainID, "FEE_POLICY")
	if err != nil {
		return handlePostgresError(c, err)
	}

	protocolFeeController, err := a.store.GetPoolAddress(ctx, tx, "protocol_fee_controller")
	if err != nil {
		return handlePostgresError(c, err)
	}

	decimalQuoterAddr, err := a.store.GetPoolAddress(ctx, tx, "decimal_quoter")
	if err != nil {
		return handlePostgresError(c, err)
	}

	trackingID := uuid.NewString()

	args := worker.DeployPoolArgs{
		TrackingID:                 trackingID,
		PoolName:                   req.Name,
		PoolSymbol:                 req.Symbol,
		PoolDecimals:               req.Decimals,
		Owner:                      req.Owner,
		QuoterType:                 req.QuoterType,
		FeesDecoupled:              req.FeesDecoupled,
		FeePolicyDefaultFee:        req.FeePolicyDefault,
		BaseCurrency:               req.BaseCurrency,
		FactoryAddress:             swapPoolPC.FactoryAddress,
		SwapPoolImplAddress:        swapPoolPC.ImplementationAddress,
		TokenIndexImplAddress:      tokenIndexPC.ImplementationAddress,
		LimiterImplAddress:         limiterPC.ImplementationAddress,
		FeePolicyImplAddress:       feePolicyPC.ImplementationAddress,
		DecimalQuoterAddress:       decimalQuoterAddr,
		ProtocolFeeController:      protocolFeeController,
		SwapPoolPlatformContractID: swapPoolPC.ID,
	}

	switch req.QuoterType {
	case "oracle":
		quoterPC, err := a.store.GetPlatformContracts(ctx, tx, a.chainID, "ORACLE_QUOTER")
		if err != nil {
			return handlePostgresError(c, err)
		}
		args.QuoterImplAddress = quoterPC.ImplementationAddress
	case "relative":
		quoterPC, err := a.store.GetPlatformContracts(ctx, tx, a.chainID, "RELATIVE_QUOTER")
		if err != nil {
			return handlePostgresError(c, err)
		}
		args.QuoterImplAddress = quoterPC.ImplementationAddress
	}

	_, err = a.queueClient.InsertTx(ctx, tx, args, &river.InsertOpts{MaxAttempts: 1})
	if err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Pool deploy request accepted",
		Result: map[string]any{
			"trackingId": trackingID,
		},
	})
}

func (a *API) deployTokenHandler(c *echo.Context) error {
	req := apiresp.DeployTokenRequest{}

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

	pc, err := a.store.GetPlatformContracts(ctx, tx, a.chainID, "ERC20")
	if err != nil {
		return handlePostgresError(c, err)
	}

	trackingID := uuid.NewString()

	_, err = a.queueClient.InsertTx(ctx, tx, worker.DeployTokenArgs{
		TrackingID:            trackingID,
		Name:                  req.Name,
		Symbol:                req.Symbol,
		Decimals:              req.Decimals,
		Owner:                 req.Owner,
		ExpiryTimestamp:       req.ExpiryTimestamp,
		FactoryAddress:        pc.FactoryAddress,
		ImplementationAddress: pc.ImplementationAddress,
		PlatformContractID:    pc.ID,
	}, &river.InsertOpts{MaxAttempts: 1})
	if err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Token deploy request accepted",
		Result: map[string]any{
			"trackingId": trackingID,
		},
	})
}
