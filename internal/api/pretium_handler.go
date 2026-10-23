package api

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/cosmo-local-credit/clc-core/internal/pretium"
	pretiumapi "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/labstack/echo/v5"
)

func (a *API) pretiumTriggerOnrampHandler(c *echo.Context) error {
	req := pretiumapi.PretiumTriggerOnrampRequest{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	if !callerMayActOnAddress(c, req.Address) {
		return handleForbiddenError(c, "address does not belong to the caller")
	}

	resp, statusCode, err := a.pretium.TriggerOnramp(c.Request().Context(), req)
	if err != nil {
		return handlePretiumError(c, err)
	}

	return c.JSON(statusCode, resp)
}

func (a *API) pretiumLinkHandler(c *echo.Context) error {
	req := pretiumapi.PretiumLinkRequest{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	if !callerMayActOnAddress(c, req.PublicKey) {
		return handleForbiddenError(c, "publicKey does not belong to the caller")
	}

	resp, statusCode, err := a.pretium.CreateLink(c.Request().Context(), req)
	if err != nil {
		return handlePretiumError(c, err)
	}

	return c.JSON(statusCode, resp)
}

func (a *API) pretiumPhoneLinksHandler(c *echo.Context) error {
	req := pretiumapi.PhoneNumberParam{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	resp, statusCode, err := a.pretium.GetLinks(c.Request().Context(), url.PathEscape(req.PhoneNumber))
	if err != nil {
		return handlePretiumError(c, err)
	}

	return c.JSON(statusCode, resp)
}

func (a *API) pretiumLinkByAddressHandler(c *echo.Context) error {
	req := pretiumapi.AccountAddressParam{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	if !callerMayActOnAddress(c, req.Address) {
		return handleForbiddenError(c, "address does not belong to the caller")
	}

	resp, statusCode, err := a.pretium.GetLinkByAddress(c.Request().Context(), url.PathEscape(req.Address))
	if err != nil {
		return handlePretiumError(c, err)
	}

	return c.JSON(statusCode, resp)
}

func (a *API) pretiumTransactionsHandler(c *echo.Context) error {
	req := pretiumapi.PhoneNumberParam{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	resp, statusCode, err := a.pretium.GetTransactions(c.Request().Context(), url.PathEscape(req.PhoneNumber))
	if err != nil {
		return handlePretiumError(c, err)
	}

	return c.JSON(statusCode, resp)
}

func (a *API) pretiumTransactionsByAddressHandler(c *echo.Context) error {
	req := pretiumapi.AccountAddressParam{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	if !callerMayActOnAddress(c, req.Address) {
		return handleForbiddenError(c, "address does not belong to the caller")
	}

	resp, statusCode, err := a.pretium.GetTransactionsByAddress(c.Request().Context(), url.PathEscape(req.Address))
	if err != nil {
		return handlePretiumError(c, err)
	}

	return c.JSON(statusCode, resp)
}

func (a *API) pretiumRecentTransactionsHandler(c *echo.Context) error {
	resp, statusCode, err := a.pretium.GetRecentTransactions(c.Request().Context())
	if err != nil {
		return handlePretiumError(c, err)
	}

	return c.JSON(statusCode, resp)
}

func (a *API) pretiumRatesHandler(c *echo.Context) error {
	resp, statusCode, err := a.pretium.GetRates(c.Request().Context())
	if err != nil {
		return handlePretiumError(c, err)
	}

	return c.JSON(statusCode, resp)
}

func handlePretiumError(c *echo.Context, err error) error {
	var apiErr *pretium.APIError
	if !errors.As(err, &apiErr) {
		return err
	}

	statusCode := apiErr.StatusCode
	if statusCode >= http.StatusInternalServerError {
		statusCode = http.StatusBadGateway
	}

	return c.JSON(statusCode, pretiumapi.ErrResponse{
		Ok:          false,
		ErrCode:     pretiumErrCode(statusCode),
		Description: apiErr.Description,
	})
}

func pretiumErrCode(statusCode int) string {
	switch statusCode {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return pretiumapi.ErrCodeValidationFailed
	case http.StatusForbidden:
		return pretiumapi.ErrCodeForbidden
	case http.StatusNotFound:
		return pretiumapi.ErrCodeNotFound
	case http.StatusConflict:
		return pretiumapi.ErrCodeConflict
	case http.StatusBadGateway:
		return pretiumapi.ErrCodeInternalServerError
	default:
		if errCode := errCodeForStatus(statusCode); errCode != "" {
			return errCode
		}
		return pretiumapi.ErrCodeInternalServerError
	}
}
