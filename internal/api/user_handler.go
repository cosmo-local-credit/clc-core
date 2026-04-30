package api

import (
	"fmt"
	"net/http"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/labstack/echo/v5"
)

func (a *API) userMeHandler(c *echo.Context) error {
	userID, _, _ := getUserClaims(c)
	ctx := c.Request().Context()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	user, err := a.store.GetUserByID(ctx, tx, userID)
	if err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Current user profile",
		Result: map[string]any{
			"user": user,
		},
	})
}

func (a *API) userTokensHandler(c *echo.Context) error {
	userID, _, _ := getUserClaims(c)
	ctx := c.Request().Context()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tokens, err := a.store.ListTokensByUser(ctx, tx, userID)
	if err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: fmt.Sprintf("Confirmed tokens for user %s", userID),
		Result: map[string]any{
			"tokens": tokens,
		},
	})
}

func (a *API) userPoolsHandler(c *echo.Context) error {
	userID, _, _ := getUserClaims(c)
	ctx := c.Request().Context()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	pools, err := a.store.ListPoolsByUser(ctx, tx, userID)
	if err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: fmt.Sprintf("Confirmed pools for user %s", userID),
		Result: map[string]any{
			"pools": pools,
		},
	})
}

func (a *API) userDeploymentsHandler(c *echo.Context) error {
	userID, _, _ := getUserClaims(c)
	ctx := c.Request().Context()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	deployments, err := a.store.ListUserDeployments(ctx, tx, userID)
	if err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: fmt.Sprintf("Deployment history for user %s", userID),
		Result: map[string]any{
			"deployments": deployments,
		},
	})
}
