package api

import (
	"net/http"

	"github.com/cosmo-local-credit/clc-core/internal/worker"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/riverqueue/river"
)

func (a *API) deployTokenHandler(c *echo.Context) error {
	req := apiresp.DeployTokenRequest{}

	if err := c.Bind(&req); err != nil {
		return handleBindError(c)
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

	pc, err := a.store.GetPlatformContracts(ctx, tx, a.chainID, "erc20")
	if err != nil {
		return handlePostgresError(c, err)
	}

	trackingID := uuid.NewString()

	_, err = a.queueClient.InsertTx(ctx, tx, worker.DeployTokenArgs{
		TrackingID:            trackingID,
		Name:                  req.Name,
		Symbol:                req.Symbol,
		Decimals:              req.Decimals,
		InitialSupply:         req.InitialSupply,
		InitialMintee:         req.InitialMintee,
		Owner:                 req.Owner,
		ExpiryTimestamp:       req.ExpiryTimestamp,
		FactoryAddress:        pc.FactoryAddress,
		ImplementationAddress: pc.ImplementationAddress,
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
