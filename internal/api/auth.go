package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v5"
)

const (
	userAuthCookieName    = "__clc_auth"
	serviceAuthCookieName = "__ge_cust_auth"
	userCookieDuration    = 30 * 24 * time.Hour
)

type (
	// JWTCustomClaims holds claims for EdDSA service tokens.
	JWTCustomClaims struct {
		jwt.RegisteredClaims

		PublicKey string `json:"publicKey"`
		Service   bool   `json:"service"`
	}

	// UserJWTClaims holds claims for user-facing EdDSA JWTs delivered via cookie.
	UserJWTClaims struct {
		jwt.RegisteredClaims

		UserID          uint64 `json:"userId"`
		EthereumAddress string `json:"ethereumAddress"`
		Role            string `json:"role"`
	}
)

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

// authCookieSameSite picks the SameSite policy for the session cookie.
//
// In production the web app and API are served from different sites (e.g.
// *.pages.dev previews, or app.cosmolocal.credit calling the API on
// cosmolocal.credit), so the cookie must be SameSite=None to be sent on
// cross-site XHR; None requires Secure, which prod already sets. Locally
// (non-prod HTTP, same-origin) we use Lax so the cookie still works without
// Secure.
func (a *API) authCookieSameSite() http.SameSite {
	if a.prod {
		return http.SameSiteNoneMode
	}
	return http.SameSiteLaxMode
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
		SameSite: a.authCookieSameSite(),
	})
}

// clearUserAuthCookie immediately expires the auth cookie. Attributes must
// match setUserAuthCookie so the browser overwrites the existing cookie.
func (a *API) clearUserAuthCookie(c *echo.Context) {
	c.SetCookie(&http.Cookie{
		Name:     userAuthCookieName,
		Value:    "",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   a.prod,
		Path:     "/",
		SameSite: a.authCookieSameSite(),
	})
}

// storageTokenHandler hands the caller a readable copy of their own session
// token.
//
// The image service takes a `clc-core` JWT in an Authorization header. It reads
// no cookies, and it is served from another host, so the session cookie can
// neither be read by JavaScript nor be sent there. Every sign-in path except
// POST /auth/siwe/verify returns only { userId, role }, and putting the token in
// those responses would still not help after a reload: the cookie survives one,
// an in-memory token does not, and the user would find uploads broken with no
// recovery but signing out and back in. So this endpoint exists to answer "give
// me a token for the session I already have".
//
// It echoes the token that authenticated the request rather than minting a new
// one. Calling generateUserToken here would issue a fresh 30-day token on every
// upload, rolling the session forward for as long as someone keeps uploading —
// a session that never expires.
//
// Nothing is scoped or shortened, which is a deliberate trade recorded here: this
// is the full session token, flexibleAuthMiddleware accepts a user JWT in an
// Authorization header as well as in the cookie, and there is no blocklist, so a
// copy that leaks is API access as that user until it expires. The client is
// required to keep it in memory only. If that trade needs reversing, this handler
// is where a scoped, short-lived token would be minted instead, and no client
// changes with it.
func (a *API) storageTokenHandler(c *echo.Context) error {
	tokenStr := ""
	if cookie, err := c.Cookie(userAuthCookieName); err == nil {
		tokenStr = cookie.Value
	}
	// A caller already authenticating by header gets that same token back, so the
	// endpoint means one thing however the request arrived.
	if tokenStr == "" {
		if header := c.Request().Header.Get(echo.HeaderAuthorization); strings.HasPrefix(header, "Bearer ") {
			tokenStr = strings.TrimPrefix(header, "Bearer ")
		}
	}

	claims, err := a.parseUserJWT(tokenStr)
	if err != nil || claims.ExpiresAt == nil {
		// The middleware already authenticated this request, so reaching here
		// means the credential was a service token or an unparseable user token.
		return c.JSON(http.StatusUnauthorized, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrJWTAuth,
			Description: "a user session is required",
		})
	}

	// Never stored by a proxy or the browser cache: it is a bearer credential.
	c.Response().Header().Set("Cache-Control", "no-store")

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Storage token issued",
		Result: map[string]any{
			"token":     tokenStr,
			"expiresAt": claims.ExpiresAt.UTC().Format(time.RFC3339),
		},
	})
}

// flexibleAuthMiddleware accepts either a user JWT cookie (EdDSA) or a service
// JWT (EdDSA, Authorization header or legacy cookie). It sets the following
// context keys for downstream handlers:
//
//	"callerType"    → "user" or "service"
//	"callerUserID"  → uint64 user id (user callers only)
//	"callerAddress" → lowercase Ethereum address
//	"callerRole"    → "USER", "ADMIN", "SUPERADMIN", or "SERVICE"
func (a *API) flexibleAuthMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if a.authenticateCaller(c) {
				return next(c)
			}
			return handleJWTAuthError(c, "valid authentication required")
		}
	}
}

// authenticateCaller sets the context keys flexibleAuthMiddleware documents
// from the first valid credential on the request, and reports whether it
// found one. It writes no response, so a public route can fall back to
// treating the caller as signed out.
func (a *API) authenticateCaller(c *echo.Context) bool {
	// 1. Try user JWT from __clc_auth cookie.
	if cookie, err := c.Cookie(userAuthCookieName); err == nil && cookie.Value != "" {
		if claims, parseErr := a.parseUserJWT(cookie.Value); parseErr == nil {
			c.Set("callerType", "user")
			c.Set("callerUserID", claims.UserID)
			c.Set("callerAddress", common.HexToAddress(claims.EthereumAddress).Hex())
			c.Set("callerRole", claims.Role)
			return true
		}
	}

	// 2. Extract token from Authorization header or legacy cookie.
	tokenStr := ""
	if authHeader := c.Request().Header.Get("Authorization"); strings.HasPrefix(authHeader, "Bearer ") {
		tokenStr = strings.TrimPrefix(authHeader, "Bearer ")
	} else if cookie, err := c.Cookie(serviceAuthCookieName); err == nil {
		tokenStr = cookie.Value
	}

	if tokenStr == "" {
		return false
	}

	// 2a. Try service JWT first. A genuine service token also
	// unmarshals successfully as a UserJWTClaims (same signing
	// key, unknown fields ignored by json.Unmarshal), so service
	// tokens must be checked — and gated on claims.Service — before
	// falling back to the permissive user-JWT parse below.
	token, parseErr := jwt.ParseWithClaims(tokenStr, &JWTCustomClaims{},
		func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodEd25519); !ok {
				return nil, errors.New("unexpected signing method")
			}
			return a.verifyingKey, nil
		},
	)
	if parseErr == nil && token.Valid {
		if claims, ok := token.Claims.(*JWTCustomClaims); ok && claims.Service {
			c.Set("callerType", "service")
			c.Set("callerAddress", strings.ToLower(claims.PublicKey))
			c.Set("callerRole", "SERVICE")
			c.Set("publicKey", claims.PublicKey)
			c.Set("service", claims.Service)
			return true
		}
	}

	// 2b. Fall back to user JWT — allows clients to send Bearer <user-jwt>.
	if claims, parseErr := a.parseUserJWT(tokenStr); parseErr == nil {
		c.Set("callerType", "user")
		c.Set("callerUserID", claims.UserID)
		c.Set("callerAddress", common.HexToAddress(claims.EthereumAddress).Hex())
		c.Set("callerRole", claims.Role)
		return true
	}

	return false
}

// requireUserAuth rejects requests that arrived via a service JWT.
// USER, ADMIN, and SUPERADMIN callers pass this guard.
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

// revalidatePrivilegedRole makes the database authoritative for long-lived
// administrative sessions. A stale ADMIN/SUPERADMIN JWT is never enough to
// authorize a protected request after the account has been demoted or rejected.
func (a *API) revalidatePrivilegedRole() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if callerType, _ := c.Get("callerType").(string); callerType != "user" {
				return next(c)
			}
			role, _ := c.Get("callerRole").(string)
			if !store.IsAdministrativeRole(role) {
				return next(c)
			}
			if a.store == nil {
				return handleJWTAuthError(c, "current account status could not be verified")
			}

			ctx := c.Request().Context()
			tx, err := a.store.Pool().Begin(ctx)
			if err != nil {
				return err
			}
			defer tx.Rollback(ctx)

			userID, _ := c.Get("callerUserID").(uint64)
			user, err := a.store.GetUserByID(ctx, tx, userID)
			claimedAddress, _ := c.Get("callerAddress").(string)
			if err != nil || user.Status != "APPROVED" ||
				!strings.EqualFold(user.EthereumAddress, claimedAddress) {
				return handleJWTAuthError(c, "current account status could not be verified")
			}
			c.Set("callerAddress", common.HexToAddress(user.EthereumAddress).Hex())
			c.Set("callerRole", user.Role)
			return next(c)
		}
	}
}

// requireAdmin permits either administrative role.
func (a *API) requireAdmin() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if role, _ := c.Get("callerRole").(string); !store.IsAdministrativeRole(role) {
				return handleForbiddenError(c, "admin access required")
			}
			return next(c)
		}
	}
}

// requireSuperAdmin rejects any caller whose role is not SUPERADMIN.
func (a *API) requireSuperAdmin() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if callerRole, _ := c.Get("callerRole").(string); !store.IsSuperadminRole(callerRole) {
				return handleForbiddenError(c, "superadmin access required")
			}
			return next(c)
		}
	}
}

// parseUserJWT validates a user JWT cookie value using the API's EdDSA public key.
func (a *API) parseUserJWT(tokenStr string) (*UserJWTClaims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &UserJWTClaims{},
		func(t *jwt.Token) (any, error) {
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
// Returns zero values for service-token callers.
func getUserClaims(c *echo.Context) (userID uint64, address, role string) {
	userID, _ = c.Get("callerUserID").(uint64)
	address, _ = c.Get("callerAddress").(string)
	role, _ = c.Get("callerRole").(string)
	return
}
