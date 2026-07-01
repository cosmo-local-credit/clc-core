package api

import (
	"github.com/cosmo-local-credit/clc-core/internal/util"
	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
)

func newCORSConfig(allowedOrigins []string) middleware.CORSConfig {
	return middleware.CORSConfig{
		AllowOrigins:     allowedOrigins,
		AllowCredentials: true,
		AllowHeaders:     []string{echo.HeaderOrigin, echo.HeaderContentType, echo.HeaderAccept, echo.HeaderAuthorization},
		MaxAge:           86400,
		UnsafeAllowOriginFunc: func(c *echo.Context, origin string) (string, bool, error) {
			if util.OriginAllowed(origin, allowedOrigins) {
				return origin, true, nil
			}
			return "", false, nil
		},
	}
}
