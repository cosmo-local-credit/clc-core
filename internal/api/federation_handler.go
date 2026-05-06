package api

import (
	"context"
	"net/http"
	"time"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/labstack/echo/v5"
)

func (a *API) federationSystemHandler(c *echo.Context) error {
	ctx, cancel := context.WithTimeout(c.Request().Context(), 5*time.Second)
	defer cancel()

	if err := a.federationStore.Pool().Ping(ctx); err != nil {
		return c.JSON(http.StatusServiceUnavailable, apiresp.ErrResponse{
			Ok:          false,
			Description: "Federation DB unreachable",
		})
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Federation DB connectivity check",
		Result: map[string]any{
			"status": "UP",
		},
	})
}
