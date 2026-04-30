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

	status := http.StatusInternalServerError
	description := "Internal server error"
	errCode := apiresp.ErrCodeInternalServerError

	var sc echo.HTTPStatusCoder
	if errors.As(err, &sc) {
		status = sc.StatusCode()
		description = http.StatusText(status)
		errCode = errCodeForStatus(status)

		var hErr *echo.HTTPError
		if errors.As(err, &hErr) && hErr.Message != "" {
			description = hErr.Message
		}
	}

	if status >= http.StatusInternalServerError {
		a.logg.Error("API: internal error", "path", c.Path(), "err", err)
	}

	c.JSON(status, apiresp.ErrResponse{
		Ok:          false,
		Description: description,
		ErrCode:     errCode,
	})
}

func handleBindError(c *echo.Context, err error) error {
	if jErr := c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
		Ok:          false,
		ErrCode:     apiresp.ErrCodeInvalidJSON,
		Description: "Invalid or malformed request",
	}); jErr != nil {
		return jErr
	}
	return err
}

func (a *API) handleValidateError(c *echo.Context, err error) error {
	if jErr := c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
		Ok:          false,
		ErrCode:     apiresp.ErrCodeValidationFailed,
		Description: "Validation failed on one or more fields",
	}); jErr != nil {
		return jErr
	}
	return err
}

func handleJWTAuthError(c *echo.Context, errorReason string) error {
	if jErr := c.JSON(http.StatusUnauthorized, apiresp.ErrResponse{
		Ok:          false,
		ErrCode:     apiresp.ErrJWTAuth,
		Description: "JWT authentication failed: " + errorReason,
	}); jErr != nil {
		return jErr
	}
	return errors.New(errorReason)
}

func errCodeForStatus(status int) string {
	switch status {
	case http.StatusNotFound:
		return apiresp.ErrCodeNotFound
	case http.StatusMethodNotAllowed:
		return apiresp.ErrCodeMethodNotAllowed
	case http.StatusRequestTimeout:
		return apiresp.ErrCodeRequestTimeout
	case http.StatusRequestEntityTooLarge:
		return apiresp.ErrCodePayloadTooLarge
	case http.StatusUnauthorized:
		return apiresp.ErrJWTAuth
	case http.StatusInternalServerError:
		return apiresp.ErrCodeInternalServerError
	}
	return ""
}

func handleForbiddenError(c *echo.Context, reason string) error {
	if jErr := c.JSON(http.StatusForbidden, apiresp.ErrResponse{
		Ok:          false,
		ErrCode:     apiresp.ErrCodeForbidden,
		Description: reason,
	}); jErr != nil {
		return jErr
	}
	return errors.New(reason)
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
