package api

import (
	"fmt"
	"net/http"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/labstack/echo/v5"
)

// poolsListHandler returns the network pool catalog (imported + clc-core
// deploys), cursor-paginated. Directory reads come from the pools catalog,
// not the deploy pipeline (MIGRATION.md §3.5); deploy-status APIs keep
// reading deployed_pools.
func (a *API) poolsListHandler(c *echo.Context) error {
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
		pools    []store.Pool
		queryErr error
	)

	switch {
	case pagination.FirstPage:
		pools, queryErr = a.store.ListPools(ctx, tx, pagination.PerPage)
	case pagination.Next:
		pools, queryErr = a.store.ListPoolsNext(ctx, tx, pagination.Cursor, pagination.PerPage)
	default:
		pools, queryErr = a.store.ListPoolsPrevious(ctx, tx, pagination.Cursor, pagination.PerPage)
	}
	if queryErr != nil {
		return handlePostgresError(c, queryErr)
	}

	if pools == nil {
		pools = []store.Pool{}
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
		Description: "Pools",
		Result: map[string]any{
			"pools": pools,
			"first": first,
			"last":  last,
		},
	})
}

// poolsByAddressHandler returns a single catalog pool by contract address
// (case-insensitive lookup; stored addresses are EIP-55).
func (a *API) poolsByAddressHandler(c *echo.Context) error {
	ctx := c.Request().Context()
	address := c.Param("address")

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	pool, err := a.store.GetPoolByAddress(ctx, tx, address)
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
