package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v5"
)

const (
	userAuthCookieName    = "__clc_auth"
	serviceAuthCookieName = "__ge_cust_auth"
	userCookieDuration    = 30 * 24 * time.Hour
)

// JWTCustomClaims holds claims for EdDSA service tokens.
type JWTCustomClaims struct {
	PublicKey string `json:"publicKey"`
	Service   bool   `json:"service"`
	jwt.RegisteredClaims
}

// UserJWTClaims holds claims for user-facing EdDSA JWTs delivered via cookie.
type UserJWTClaims struct {
	UserID          string `json:"userId"`
	EthereumAddress string `json:"ethereumAddress"`
	Role            string `json:"role"`
	jwt.RegisteredClaims
}

// generateUserToken creates a signed EdDSA JWT for a user session valid for 30 days.
func (a *API) generateUserToken(user store.User) (string, error) {
	claims := UserJWTClaims{
		UserID:          user.ID,
		EthereumAddress: user.EthereumAddress,
		Role:            user.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(userCookieDuration)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	return token.SignedString(a.signingKey)
}

// setUserAuthCookie writes an HttpOnly cookie carrying the user JWT.
// Secure is only set in production mode so local HTTP dev still works.
func (a *API) setUserAuthCookie(c *echo.Context, tokenStr string) {
	c.SetCookie(&http.Cookie{
		Name:     userAuthCookieName,
		Value:    tokenStr,
		Expires:  time.Now().Add(userCookieDuration),
		HttpOnly: true,
		Secure:   a.prod,
		Path:     "/",
		SameSite: http.SameSiteStrictMode,
	})
}

// clearUserAuthCookie immediately expires the auth cookie.
func clearUserAuthCookie(c *echo.Context) {
	c.SetCookie(&http.Cookie{
		Name:     userAuthCookieName,
		Value:    "",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Path:     "/",
		SameSite: http.SameSiteStrictMode,
	})
}

// flexibleAuthMiddleware accepts either a user JWT cookie (EdDSA) or a service
// JWT (EdDSA, Authorization header or legacy cookie). It sets the following
// context keys for downstream handlers:
//
//	"callerType"    → "user" or "service"
//	"callerUserID"  → UUID string (user callers only)
//	"callerAddress" → lowercase Ethereum address
//	"callerRole"    → "USER", "SUPERADMIN", or "SERVICE"
func (a *API) flexibleAuthMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			// 1. Try user JWT from __clc_auth cookie.
			if cookie, err := c.Cookie(userAuthCookieName); err == nil && cookie.Value != "" {
				if claims, parseErr := a.parseUserJWT(cookie.Value); parseErr == nil {
					c.Set("callerType", "user")
					c.Set("callerUserID", claims.UserID)
					c.Set("callerAddress", claims.EthereumAddress)
					c.Set("callerRole", claims.Role)
					return next(c)
				}
			}

			// 2. Fall back to service JWT (Authorization header or legacy cookie).
			tokenStr := ""
			if authHeader := c.Request().Header.Get("Authorization"); strings.HasPrefix(authHeader, "Bearer ") {
				tokenStr = strings.TrimPrefix(authHeader, "Bearer ")
			} else if cookie, err := c.Cookie(serviceAuthCookieName); err == nil {
				tokenStr = cookie.Value
			}

			if tokenStr != "" {
				token, parseErr := jwt.ParseWithClaims(tokenStr, &JWTCustomClaims{},
					func(t *jwt.Token) (interface{}, error) {
						if _, ok := t.Method.(*jwt.SigningMethodEd25519); !ok {
							return nil, errors.New("unexpected signing method")
						}
						return a.verifyingKey, nil
					},
				)
				if parseErr == nil && token.Valid {
					if claims, ok := token.Claims.(*JWTCustomClaims); ok {
						c.Set("callerType", "service")
						c.Set("callerAddress", strings.ToLower(claims.PublicKey))
						c.Set("callerRole", "SERVICE")
						c.Set("publicKey", claims.PublicKey)
						c.Set("service", claims.Service)
						return next(c)
					}
				}
			}

			return handleJWTAuthError(c, "valid authentication required")
		}
	}
}

// requireUserAuth rejects requests that arrived via a service JWT.
// Both USER and SUPERADMIN callers pass this guard.
func (a *API) requireUserAuth() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if callerType, _ := c.Get("callerType").(string); callerType != "user" {
				return handleForbiddenError(c, "this endpoint requires user authentication")
			}
			return next(c)
		}
	}
}

// requireSuperAdmin rejects any caller whose role is not SUPERADMIN.
func (a *API) requireSuperAdmin() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if callerRole, _ := c.Get("callerRole").(string); callerRole != "SUPERADMIN" {
				return handleForbiddenError(c, "superadmin access required")
			}
			return next(c)
		}
	}
}

// parseUserJWT validates a user JWT cookie value using the API's EdDSA public key.
func (a *API) parseUserJWT(tokenStr string) (*UserJWTClaims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &UserJWTClaims{},
		func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodEd25519); !ok {
				return nil, errors.New("unexpected signing method")
			}
			return a.verifyingKey, nil
		},
	)
	if err != nil || !token.Valid {
		return nil, errors.New("invalid token")
	}
	claims, ok := token.Claims.(*UserJWTClaims)
	if !ok {
		return nil, errors.New("invalid claims")
	}
	return claims, nil
}

// getUserClaims extracts the caller's identity from context.
// Returns empty strings for service-token callers.
func getUserClaims(c *echo.Context) (userID, address, role string) {
	userID, _ = c.Get("callerUserID").(string)
	address, _ = c.Get("callerAddress").(string)
	role, _ = c.Get("callerRole").(string)
	return
}
