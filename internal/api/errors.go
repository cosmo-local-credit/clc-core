package api

import (
	"errors"
	"net/http"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/labstack/echo/v5"
)

const (
	// pgUniqueViolation is the PostgreSQL error code for a UNIQUE constraint
	// violation. Writes to users can trip users_blockchain_address_unique,
	// users_email_unique or users_phone_unique — either raced past a
	// duplicate-identity pre-check or never pre-checked at all; we translate
	// the violation into a 409 conflict rather than surfacing a generic 500 E01.
	pgUniqueViolation = "23505"
)

// userIdentityConflict maps a UNIQUE violation on the users table onto a 409
// response, keyed by constraint name so the caller learns *which* identity
// field collided instead of a generic conflict. Reports false for any other
// error, which the caller should keep propagating.
func userIdentityConflict(err error) (apiresp.ErrResponse, bool) {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != pgUniqueViolation {
		return apiresp.ErrResponse{}, false
	}

	resp := apiresp.ErrResponse{
		Ok:          false,
		ErrCode:     apiresp.ErrCodeConflict,
		Description: "identity already linked to another account",
	}
	switch pgErr.ConstraintName {
	case "identity_claims_name_key":
		resp.Description = "username or pool name is already in use"
	case "users_email_unique":
		resp.ErrCode = apiresp.ErrCodeEmailConflict
		resp.Description = "email already linked to another account"
	case "users_phone_unique":
		resp.ErrCode = apiresp.ErrCodePhoneConflict
		resp.Description = "phone number already linked to another account"
	}
	return resp, true
}

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

	_ = c.JSON(status, apiresp.ErrResponse{
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

// handleConflictError is the 409 for a request that is well-formed but collides
// with state the server already holds -- a name someone else took, an offering
// that already exists. Distinct from a 400: nothing about the request needs
// correcting except the value that clashed.
func handleConflictError(c *echo.Context, description string) error {
	return c.JSON(http.StatusConflict, apiresp.ErrResponse{
		Ok:          false,
		ErrCode:     apiresp.ErrCodeConflict,
		Description: description,
	})
}

func handlePostgresError(c *echo.Context, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
		switch pgErr.ConstraintName {
		case "catalogs_one_per_owner":
			return handleConflictError(c, "you already have a catalog")
		case "identity_claims_name_key", "identity_claims_owner_key":
			return handleConflictError(c, "name or symbol is already in use")
		case "token_tag_pkey", "token_tag_value_lower_idx", "offering_unit_pkey", "offering_unit_value_lower_idx":
			return c.JSON(http.StatusConflict, apiresp.ErrResponse{
				Ok: false, ErrCode: apiresp.ErrCodeConflict,
				Description: "a vocabulary value with this name already exists",
			})
		}
	}

	if errors.Is(err, pgx.ErrNoRows) {
		return c.JSON(http.StatusNotFound, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrNoRecordFound,
			Description: "Record(s) not found",
		})
	}

	return err
}
