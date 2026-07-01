package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

type (
	// paginatedListCallbacks groups the three pagination query functions for a
	// given resource type T.
	paginatedListCallbacks[T any] struct {
		firstPage func(context.Context, pgx.Tx, uint64, uint64) ([]T, error)
		nextPage  func(context.Context, pgx.Tx, uint64, uint64, uint64) ([]T, error)
		prevPage  func(context.Context, pgx.Tx, uint64, uint64, uint64) ([]T, error)
	}
)

// cursorPaginatedList executes a cursor-paginated query and returns a JSON
// response with the items and first/last cursor values.
func cursorPaginatedList[T any](
	a *API,
	c *echo.Context,
	p paginatedListCallbacks[T],
	userID uint64,
	resultKey, description string,
	getID func(T) uint64,
) error {
	ctx := c.Request().Context()

	req := apiresp.CursorPaginationRequest{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	pagination := normalizePagination(req)

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var (
		items    []T
		queryErr error
	)

	switch {
	case pagination.FirstPage:
		items, queryErr = p.firstPage(ctx, tx, userID, pagination.PerPage)
	case pagination.Next:
		items, queryErr = p.nextPage(ctx, tx, userID, pagination.Cursor, pagination.PerPage)
	default:
		items, queryErr = p.prevPage(ctx, tx, userID, pagination.Cursor, pagination.PerPage)
	}
	if queryErr != nil {
		return handlePostgresError(c, queryErr)
	}

	var first, last uint64
	if len(items) > 0 {
		first = getID(items[0])
		last = getID(items[len(items)-1])
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: description,
		Result: map[string]any{
			resultKey: items,
			"first":   first,
			"last":    last,
		},
	})
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
	return cursorPaginatedList(a, c, paginatedListCallbacks[store.DeployedToken]{
		firstPage: a.store.ListTokensByUser,
		nextPage:  a.store.ListTokensByUserNext,
		prevPage:  a.store.ListTokensByUserPrevious,
	}, userID, "tokens", fmt.Sprintf("Confirmed tokens for user %d", userID),
		func(t store.DeployedToken) uint64 { return t.ID })
}

func (a *API) userPoolsHandler(c *echo.Context) error {
	userID, _, _ := getUserClaims(c)
	return cursorPaginatedList(a, c, paginatedListCallbacks[store.DeployedPool]{
		firstPage: a.store.ListPoolsByUser,
		nextPage:  a.store.ListPoolsByUserNext,
		prevPage:  a.store.ListPoolsByUserPrevious,
	}, userID, "pools", fmt.Sprintf("Confirmed pools for user %d", userID),
		func(t store.DeployedPool) uint64 { return t.ID })
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
		Description: fmt.Sprintf("Deployment history for user %d", userID),
		Result: map[string]any{
			"deployments": deployments,
		},
	})
}
