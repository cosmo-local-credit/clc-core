package api

import (
	"net/http"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/labstack/echo/v5"
)

// Old clients must not bypass the owner request and administrator review.
func (a *API) retireVoucherHandler(c *echo.Context) error {
	if !validRetirementCaller(c) {
		return handleForbiddenError(c, "this endpoint requires an account")
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusGone, apiresp.ErrResponse{
		Ok:          false,
		ErrCode:     apiresp.ErrCodeConflict,
		Description: "Immediate voucher retirement is no longer available. Update the app and submit a retirement request with a reason.",
	})
}
