package api

import (
	"errors"

	"github.com/cosmo-local-credit/clc-core/internal/util"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
)

// domainCORSConfig allows configured app/admin origins and active verified
// space hosts. Preflight permission lasts five minutes; actual requests still
// check the origin, and handlers enforce their own authorization rules.
func (a *API) domainCORSConfig(allowedOrigins []string) middleware.CORSConfig {
	return middleware.CORSConfig{
		AllowCredentials: true,
		AllowHeaders:     []string{echo.HeaderOrigin, echo.HeaderContentType, echo.HeaderAccept, echo.HeaderAuthorization, domainIDHeader, "If-Match"},
		ExposeHeaders:    []string{"ETag"},
		MaxAge:           300,
		UnsafeAllowOriginFunc: func(c *echo.Context, origin string) (string, bool, error) {
			if util.OriginAllowed(origin, allowedOrigins) {
				return origin, true, nil
			}
			host, err := domainOriginHostname(origin)
			if err != nil || a.store == nil {
				return "", false, nil
			}
			ctx := c.Request().Context()
			tx, err := a.store.Pool().Begin(ctx)
			if err != nil {
				return "", false, err
			}
			defer tx.Rollback(ctx)
			_, err = a.store.GetActiveDomainByHostname(ctx, tx, host)
			if errors.Is(err, pgx.ErrNoRows) {
				return "", false, nil
			}
			if err != nil {
				return "", false, err
			}
			return origin, true, nil
		},
	}
}
