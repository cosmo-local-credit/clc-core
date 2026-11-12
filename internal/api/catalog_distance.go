package api

import (
	"crypto/sha256"
	"fmt"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

func catalogSortOriginIdentity(location *store.CatalogLocation) string {
	if location == nil {
		return ""
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%.8f,%.8f", location.Lat, location.Lng))
	return fmt.Sprintf("%x", sum[:12])
}

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
			if c.QueryParam("sort") == "nearest" {
				// Nearest discovery is still public. A valid user session lets the
				// handler use the saved profile location; a guest falls back to the
				// already-public viewport center.
				a.authenticateCaller(c)
				c.Response().Header().Set("Cache-Control", "private, no-store")
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

// resolveNearestCatalog selects an ordering origin without changing resource
// eligibility. A valid signed-in profile wins; otherwise the exact viewport
// center is used. Guest GPS coordinates never cross this boundary separately.
func (a *API) resolveNearestCatalog(c *echo.Context, tx pgx.Tx, search *catalogSearchRequest) (bool, error) {
	if !search.nearest {
		return false, nil
	}
	if caller, _ := c.Get("callerType").(string); caller == "user" {
		_, address, _ := getUserClaims(c)
		user, err := a.store.GetUserByAddress(c.Request().Context(), tx, address)
		if err != nil {
			return true, handlePostgresError(c, err)
		}
		if location, locationErr := store.NewCatalogLocation(user.GeoLat, user.GeoLng); locationErr == nil {
			search.filter.SortOrigin = location
			return false, nil
		}
	}
	search.filter.SortOrigin = store.CatalogBoundsCenter(search.filter.Bounds)
	return false, nil
}
