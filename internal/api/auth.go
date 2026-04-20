package api

import (
	"errors"

	"github.com/golang-jwt/jwt/v5"
	echojwt "github.com/labstack/echo-jwt/v5"
	"github.com/labstack/echo/v5"
)

type JWTCustomClaims struct {
	PublicKey string `json:"publicKey"`
	Service   bool   `json:"service"`
	jwt.RegisteredClaims
}

func (a *API) apiJWTAuthConfig() echojwt.Config {
	return echojwt.Config{
		ErrorHandler: func(c *echo.Context, err error) error {
			var reason = "an unknown JWT error occurred"

			if errors.Is(err, echojwt.ErrJWTMissing) {
				reason = "token missing from Authorization header or cookie"
			} else if errors.Is(err, echojwt.ErrJWTInvalid) {
				reason = "token is invalid or expired"
			} else {
				a.logg.Error("Unknown JWT error caught", "error", err)
			}
			return handleJWTAuthError(c, reason)
		},
		TokenLookup:   "header:Authorization:Bearer ,cookie:__ge_cust_auth",
		SigningMethod: jwt.SigningMethodEdDSA.Alg(),
		SigningKey:    a.verifyingKey,
	}
}

func (a *API) authStatusMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			token, ok := c.Get("user").(*jwt.Token)
			if !ok {
				return handleJWTAuthError(c, "JWT token missing or invalid")
			}
			claims, ok := token.Claims.(jwt.MapClaims)
			if !ok {
				return handleJWTAuthError(c, "JWT invalid claims")
			}
			pubKey, ok := claims["publicKey"].(string)
			if !ok {
				return handleJWTAuthError(c, "public key missing in JWT claims")
			}
			serviceKey, ok := claims["service"].(bool)
			if !ok {
				return handleJWTAuthError(c, "service key missing in JWT claims")
			}

			c.Set("publicKey", pubKey)
			c.Set("service", serviceKey)

			return next(c)
		}
	}
}
