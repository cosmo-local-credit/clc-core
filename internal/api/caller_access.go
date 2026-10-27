package api

import (
	"strings"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// callerIsStaff reports whether the caller is a service token or a
// administrator, the callers that can act on any account.
func callerIsStaff(c *echo.Context) bool {
	callerType, _ := c.Get("callerType").(string)
	if callerType == "service" {
		return true
	}
	role, _ := c.Get("callerRole").(string)
	return callerType == "user" && store.IsAdministrativeRole(role)
}

// callerMayActOnAddress reports whether the caller can read or act on the
// account at address: staff always, any other user only on their own address.
func callerMayActOnAddress(c *echo.Context, address string) bool {
	if callerIsStaff(c) {
		return true
	}
	if callerType, _ := c.Get("callerType").(string); callerType != "user" {
		return false
	}
	caller, _ := c.Get("callerAddress").(string)
	return caller != "" && strings.EqualFold(caller, address)
}

// requireStaff rejects callers that are neither a service token nor a
// administrator.
func (a *API) requireStaff() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if !callerIsStaff(c) {
				return handleForbiddenError(c, "service token or admin access required")
			}
			return next(c)
		}
	}
}

// requireDeploymentOwner passes administrators and the user who started the
// deployment. Any other caller gets the same not-found answer as for an
// unknown address, so the route does not reveal that the record exists.
func (a *API) requireDeploymentOwner(c *echo.Context, tx pgx.Tx, trackingID string) error {
	if role, _ := c.Get("callerRole").(string); store.IsAdministrativeRole(role) {
		return nil
	}
	userID, _ := c.Get("callerUserID").(uint64)
	if _, err := a.store.GetUserDeploymentByTrackingID(c.Request().Context(), tx, trackingID, userID); err != nil {
		return handlePostgresError(c, err)
	}
	return nil
}
