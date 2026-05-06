package api

import (
	"fmt"
	"net/http"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/labstack/echo/v5"
)

func reverseItems[T any](items []T) {
	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}
}

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

	req := apiresp.CursorPaginationRequest{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	p := normalizePagination(req)

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var (
		tokens   []store.DeployedToken
		queryErr error
	)

	if p.FirstPage {
		tokens, queryErr = a.store.ListTokensByUser(ctx, tx, userID, p.PerPage)
	} else if p.Next {
		tokens, queryErr = a.store.ListTokensByUserNext(ctx, tx, userID, p.Cursor, p.PerPage)
		reverseItems(tokens)
	} else {
		tokens, queryErr = a.store.ListTokensByUserPrevious(ctx, tx, userID, p.Cursor, p.PerPage)
	}
	if queryErr != nil {
		return handlePostgresError(c, queryErr)
	}

	var first, last uint64
	if len(tokens) > 0 {
		first = tokens[0].ID
		last = tokens[len(tokens)-1].ID
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: fmt.Sprintf("Confirmed tokens for user %s", userID),
		Result: map[string]any{
			"tokens": tokens,
			"first":  first,
			"last":   last,
		},
	})
}

func (a *API) userPoolsHandler(c *echo.Context) error {
	userID, _, _ := getUserClaims(c)
	ctx := c.Request().Context()

	req := apiresp.CursorPaginationRequest{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	p := normalizePagination(req)

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var (
		pools    []store.DeployedPool
		queryErr error
	)

	if p.FirstPage {
		pools, queryErr = a.store.ListPoolsByUser(ctx, tx, userID, p.PerPage)
	} else if p.Next {
		pools, queryErr = a.store.ListPoolsByUserNext(ctx, tx, userID, p.Cursor, p.PerPage)
		reverseItems(pools)
	} else {
		pools, queryErr = a.store.ListPoolsByUserPrevious(ctx, tx, userID, p.Cursor, p.PerPage)
	}
	if queryErr != nil {
		return handlePostgresError(c, queryErr)
	}

	var first, last uint64
	if len(pools) > 0 {
		first = pools[0].ID
		last = pools[len(pools)-1].ID
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: fmt.Sprintf("Confirmed pools for user %s", userID),
		Result: map[string]any{
			"pools": pools,
			"first": first,
			"last":  last,
		},
	})
}

func (a *API) userTokenByAddressHandler(c *echo.Context) error {
	ctx := c.Request().Context()
	address := c.Param("address")

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	token, err := a.store.GetDeployedTokenByAddress(ctx, tx, address)
	if err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: fmt.Sprintf("Token at address %s", address),
		Result: map[string]any{
			"token": token,
		},
	})
}

func (a *API) userPoolByAddressHandler(c *echo.Context) error {
	ctx := c.Request().Context()
	address := c.Param("address")

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	pool, err := a.store.GetDeployedPoolByAddress(ctx, tx, address)
	if err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: fmt.Sprintf("Pool at address %s", address),
		Result: map[string]any{
			"pool": pool,
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
