package api

import (
	"errors"
	"net/http"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/labstack/echo/v5"
)

func handleResourceLimitError(c *echo.Context, err error) error {
	var limit *store.ResourceLimitError
	if !errors.As(err, &limit) {
		return handlePostgresError(c, err)
	}
	return c.JSON(http.StatusTooManyRequests, apiresp.ErrResponse{
		Ok: false, ErrCode: apiresp.ErrCodeLimitExceeded, Description: limit.Error(),
		Result: map[string]any{"resource": limit.Resource, "used": limit.Used, "limit": limit.Limit},
	})
}

func (a *API) userResourceUsageHandler(c *echo.Context) error {
	id, _, _ := getUserClaims(c)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	usage, err := a.store.GetUserResourceUsage(ctx, tx, id)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, apiresp.OKResponse{Ok: true, Result: map[string]any{"usage": usage}})
}
