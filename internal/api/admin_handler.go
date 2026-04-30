package api

import (
	"net/http"
	"strings"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/labstack/echo/v5"
)

func (a *API) adminListUsersHandler(c *echo.Context) error {
	status := strings.ToUpper(c.QueryParam("status"))
	ctx := c.Request().Context()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var result any
	var queryErr error

	if status != "" {
		result, queryErr = a.store.ListUsersByStatus(ctx, tx, status)
	} else {
		result, queryErr = a.store.ListUsers(ctx, tx)
	}
	if queryErr != nil {
		return handlePostgresError(c, queryErr)
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Users list",
		Result: map[string]any{
			"users": result,
		},
	})
}

func (a *API) adminApproveUserHandler(c *echo.Context) error {
	return a.adminSetUserStatus(c, "APPROVED")
}

func (a *API) adminRejectUserHandler(c *echo.Context) error {
	return a.adminSetUserStatus(c, "REJECTED")
}

func (a *API) adminSetUserStatus(c *echo.Context, status string) error {
	var param apiresp.EthAddressParam
	if err := c.Bind(&param); err != nil {
		return handleBindError(c, err)
	}
	if !common.IsHexAddress(param.Address) {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "invalid Ethereum address",
		})
	}

	address := strings.ToLower(param.Address)
	ctx := c.Request().Context()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	user, err := a.store.UpdateUserStatus(ctx, tx, address, status)
	if err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "User status updated to " + status,
		Result: map[string]any{
			"user": user,
		},
	})
}

func (a *API) adminUpdateUserLimitsHandler(c *echo.Context) error {
	var param apiresp.EthAddressParam
	if err := c.Bind(&param); err != nil {
		return handleBindError(c, err)
	}
	if !common.IsHexAddress(param.Address) {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "invalid Ethereum address",
		})
	}

	var req apiresp.UpdateUserLimitsRequest
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	address := strings.ToLower(param.Address)
	ctx := c.Request().Context()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	user, err := a.store.UpdateUserLimits(ctx, tx, address, req.MaxPools, req.MaxTokens)
	if err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "User limits updated",
		Result: map[string]any{
			"user": user,
		},
	})
}

func (a *API) adminListTokensHandler(c *echo.Context) error {
	ctx := c.Request().Context()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tokens, err := a.store.ListAllTokens(ctx, tx)
	if err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "All deployed tokens",
		Result: map[string]any{
			"tokens": tokens,
		},
	})
}

func (a *API) adminListPoolsHandler(c *echo.Context) error {
	ctx := c.Request().Context()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	pools, err := a.store.ListAllPools(ctx, tx)
	if err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "All deployed pools",
		Result: map[string]any{
			"pools": pools,
		},
	})
}

func (a *API) adminListDeploymentsHandler(c *echo.Context) error {
	ctx := c.Request().Context()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	deployments, err := a.store.ListAllDeployments(ctx, tx)
	if err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "All deployment history",
		Result: map[string]any{
			"deployments": deployments,
		},
	})
}
