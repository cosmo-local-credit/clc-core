package api

import (
	"fmt"
	"math/big"
	"net/http"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/grassrootseconomics/ethutils"
	"github.com/labstack/echo/v5"
	"github.com/lmittmann/w3/module/eth"
)

func (a *API) systemHandler(c *echo.Context) error {
	ctx := c.Request().Context()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	systemKey, err := a.store.LoadMasterSignerKey(ctx, tx)
	if err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Current system information",
		Result: map[string]any{
			"systemSigner": systemKey.Public,
			"build":        a.build,
		},
	})
}

// trackOTXHandler godoc
//
//	@Summary		Track an OTX's (Origin transaction) chain status
//	@Description	Track an OTX's (Origin transaction) chain status
//	@Tags			OTX
//	@Accept			*/*
//	@Produce		json
//	@Param			trackingId	path		string	true	"Tracking ID"
//	@Success		200			{object}	apiresp.OKResponse
//	@Failure		500			{object}	apiresp.ErrResponse
//	@Security		ApiKeyAuth
//	@Router			/otx/track/{trackingId} [get]
func (a *API) trackOTXHandler(c *echo.Context) error {
	ctx := c.Request().Context()
	trackingID := c.Param("trackingId")

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	otxs, err := a.store.GetOTXByTrackingID(ctx, tx, trackingID)
	if err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: fmt.Sprintf("OTX chain for tracking ID %s", trackingID),
		Result: map[string]any{
			"otx": otxs,
		},
	})
}

func (a *API) accountOTXHandler(c *echo.Context) error {
	ctx := c.Request().Context()

	req := apiresp.OTXByAccountRequest{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	p := normalizePagination(req)

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var otxs []*store.OTX
	var queryErr error

	if p.FirstPage {
		otxs, queryErr = a.store.GetOTXByAccount(ctx, tx, req.Address, p.PerPage)
	} else if p.Next {
		otxs, queryErr = a.store.GetOTXByAccountNext(ctx, tx, req.Address, p.Cursor, p.PerPage)
	} else {
		otxs, queryErr = a.store.GetOTXByAccountPrevious(ctx, tx, req.Address, p.Cursor, p.PerPage)
	}
	if queryErr != nil {
		return handlePostgresError(c, queryErr)
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	var first, last uint64
	if len(otxs) > 0 {
		first = otxs[0].ID
		last = otxs[len(otxs)-1].ID
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: fmt.Sprintf("Successfully fetched OTX for %s", req.Address),
		Result: map[string]any{
			"otx":   otxs,
			"first": first,
			"last":  last,
		},
	})
}

func (a *API) accountStatusHandler(c *echo.Context) error {
	ctx := c.Request().Context()

	req := apiresp.AccountAddressParam{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	var (
		gasBalance   *big.Int
		networkNonce uint64
	)

	accountAddress := ethutils.HexToAddress(req.Address)

	if err := a.chainProvider.Client.CallCtx(
		ctx,
		eth.Nonce(accountAddress, nil).Returns(&networkNonce),
		eth.Balance(accountAddress, nil).Returns(&gasBalance),
	); err != nil {
		return err
	}

	if networkNonce > 0 {
		networkNonce--
	}

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	internalNonce, err := a.store.PeekNonce(ctx, tx, req.Address)
	if err != nil {
		return handlePostgresError(c, err)
	}

	active, err := a.store.CheckKeypair(ctx, tx, req.Address)
	if err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Custodial account status",
		Result: map[string]any{
			"gasBalance":    gasBalance.String(),
			"networkNonce":  networkNonce,
			"internalNonce": internalNonce,
			"active":        active,
		},
	})
}

type paginationParams struct {
	apiresp.OTXByAccountRequest
	FirstPage bool
}

func normalizePagination(q apiresp.OTXByAccountRequest) paginationParams {
	if q.PerPage == 0 || q.PerPage > 100 {
		q.PerPage = 100
	}
	return paginationParams{
		OTXByAccountRequest: q,
		FirstPage:           !q.Next && q.Cursor < 1,
	}
}
