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

// JWTCustomClaims is kept for backward-compatible EdDSA service token validation.
type JWTCustomClaims struct {
	PublicKey string `json:"publicKey"`
	Service   bool   `json:"service"`
	jwt.RegisteredClaims
}

// UserJWTClaims holds claims for user-facing JWTs (HMAC-SHA256, cookie-delivered).
type UserJWTClaims struct {
	UserID          string `json:"userId"`
	EthereumAddress string `json:"ethereumAddress"`
	Role            string `json:"role"`
	jwt.RegisteredClaims
}

// generateUserToken creates a signed HS256 JWT for a user session valid for 30 days.
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
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(a.jwtSecret)
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

// flexibleAuthMiddleware satisfies auth with either a user JWT cookie (HS256)
// or a service JWT (EdDSA). It sets the following context keys for downstream handlers:
//
//	"callerType"    → "user" or "service"
//	"callerUserID"  → UUID string (user callers only)
//	"callerAddress" → lowercase Ethereum address
//	"callerRole"    → "USER", "SUPERADMIN", or "SERVICE"
//
// For service callers it additionally sets the legacy keys "publicKey" and "service"
// so existing handlers that rely on them continue to work unchanged.
func (a *API) flexibleAuthMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			// --- 1. Try user JWT from __clc_auth cookie ---
			if cookie, err := c.Cookie(userAuthCookieName); err == nil && cookie.Value != "" {
				token, parseErr := jwt.ParseWithClaims(cookie.Value, &UserJWTClaims{},
					func(t *jwt.Token) (interface{}, error) {
						if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
							return nil, errors.New("unexpected signing method")
						}
						return a.jwtSecret, nil
					},
				)
				if parseErr == nil && token.Valid {
					if claims, ok := token.Claims.(*UserJWTClaims); ok {
						c.Set("callerType", "user")
						c.Set("callerUserID", claims.UserID)
						c.Set("callerAddress", claims.EthereumAddress)
						c.Set("callerRole", claims.Role)
						return next(c)
					}
				}
			}

			// --- 2. Fall back to service JWT (Authorization header or legacy cookie) ---
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
						// Legacy keys kept for any future handler that relies on them.
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
// Both USER and SUPERADMIN user-JWT callers pass this guard.
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

// parseUserJWT is a thin helper used by handlers that need to inspect the
// user cookie without going through the full middleware chain (e.g. sessionHandler).
func parseUserJWT(tokenStr string, secret []byte) (*UserJWTClaims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &UserJWTClaims{},
		func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, errors.New("unexpected signing method")
			}
			return secret, nil
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

// getUserClaims is a convenience helper that extracts the caller's identity from context.
// Returns empty strings for service-token callers.
func getUserClaims(c *echo.Context) (userID, address, role string) {
	userID, _ = c.Get("callerUserID").(string)
	address, _ = c.Get("callerAddress").(string)
	role, _ = c.Get("callerRole").(string)
	return
}
