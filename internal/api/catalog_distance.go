package api

import (
	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// Ordinary public voucher reads keep their existing contract. Nearby reads
// need the existing user session to resolve the saved profile location.
func (a *API) nearbyCatalogAuth() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		authenticated := a.flexibleAuthMiddleware()(a.requireUserAuth()(next))
		return func(c *echo.Context) error {
			if c.QueryParam("nearby") == "true" {
				c.Response().Header().Set("Cache-Control", "private, no-store")
				return authenticated(c)
			}
			return next(c)
		}
	}
}

func (a *API) resolveNearbyCatalog(c *echo.Context, tx pgx.Tx, search *catalogSearchRequest) (bool, error) {
	if !search.nearby {
		return false, nil
	}
	c.Response().Header().Set("Cache-Control", "private, no-store")
	if caller, _ := c.Get("callerType").(string); caller != "user" {
		return true, handleForbiddenError(c, "nearby browsing requires user authentication")
	}
	_, address, _ := getUserClaims(c)
	user, err := a.store.GetUserByAddress(c.Request().Context(), tx, address)
	if err != nil {
		return true, handlePostgresError(c, err)
	}
	search.filter.Nearby, err = store.NewCatalogLocation(user.GeoLat, user.GeoLng)
	if err != nil {
		return true, respondCatalogSearchError(c, err)
	}
	return false, nil
}
