package api

import (
	"errors"
	"net/http"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

func (a *API) customHTTPErrorHandler(c *echo.Context, err error) {
	if r, rErr := echo.UnwrapResponse(c.Response()); rErr == nil && r.Committed {
		return
	}

	var hErr *echo.HTTPError
	if errors.As(err, &hErr) {
		c.JSON(hErr.Code, apiresp.ErrResponse{
			Ok:          false,
			Description: hErr.Message,
		})
		return
	}

	a.logg.Error("api: echo error", "path", c.Path(), "err", err)
	c.JSON(http.StatusInternalServerError, apiresp.ErrResponse{
		Ok:          false,
		Description: "Internal server error",
		ErrCode:     apiresp.ErrCodeInternalServerError,
	})
}

func handleBindError(c *echo.Context) error {
	return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
		Ok:          false,
		ErrCode:     apiresp.ErrCodeInvalidJSON,
		Description: "Invalid or malformed request",
	})
}

func handleValidateError(c *echo.Context) error {
	return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
		Ok:          false,
		ErrCode:     apiresp.ErrCodeValidationFailed,
		Description: "Validation failed on one or more fields",
	})
}

func handleJWTAuthError(c *echo.Context, errorReason string) error {
	return c.JSON(http.StatusUnauthorized, apiresp.ErrResponse{
		Ok:          false,
		ErrCode:     apiresp.ErrJWTAuth,
		Description: "JWT authentication failed: " + errorReason,
	})
}

func handlePostgresError(c *echo.Context, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return c.JSON(http.StatusNotFound, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrNoRecordFound,
			Description: "Record(s) not found",
		})
	}

	return err
}
