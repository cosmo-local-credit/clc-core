package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/internal/util"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

func (a *API) nonceHandler(c *echo.Context) error {
	nonce := a.siweNonces.Generate()
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Nonce generated for SIWE challenge",
		Result: map[string]any{
			"nonce": nonce,
		},
	})
}

func (a *API) signupHandler(c *echo.Context) error {
	var req apiresp.SignupRequest
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	// 1. Verify SIWE signature — recover the signer address.
	signerAddr, err := util.VerifySIWESignature(req.Message, req.Signature)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "signature verification failed: " + err.Error(),
		})
	}

	// 2. Confirm the address embedded in the message matches the recovered signer.
	msgAddr, err := util.ParseSIWEAddress(req.Message)
	if err != nil || !strings.EqualFold(signerAddr, msgAddr) {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "address mismatch: signer does not match claimed address",
		})
	}

	// 3. Consume nonce (one-time use, 5-min TTL).
	nonce, err := util.ParseSIWENonce(req.Message)
	if err != nil || !a.siweNonces.Consume(nonce) {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "invalid or expired nonce",
		})
	}

	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// 4. Guard against duplicate registrations.
	_, lookupErr := a.store.GetUserByAddress(ctx, tx, signerAddr)
	if lookupErr == nil {
		return c.JSON(http.StatusConflict, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "address already registered",
		})
	}
	if !errors.Is(lookupErr, pgx.ErrNoRows) {
		return lookupErr
	}

	// 5. Persist the new user.
	user, err := a.store.InsertUser(ctx, tx, store.User{
		EthereumAddress: signerAddr,
		Name:            req.Name,
		Email:           strings.ToLower(req.Email),
		Role:            "USER",
		Status:          "PENDING",
		MaxPools:        10,
		MaxTokens:       20,
	})
	if err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Registration successful",
		Result: map[string]any{
			"userId": user.ID,
			"status": user.Status,
			"role":   user.Role,
		},
	})
}

func (a *API) signinHandler(c *echo.Context) error {
	var req apiresp.SigninRequest
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	// 1. Verify SIWE signature.
	signerAddr, err := util.VerifySIWESignature(req.Message, req.Signature)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "signature verification failed: " + err.Error(),
		})
	}

	// 2. Address consistency check.
	msgAddr, err := util.ParseSIWEAddress(req.Message)
	if err != nil || !strings.EqualFold(signerAddr, msgAddr) {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "address mismatch: signer does not match claimed address",
		})
	}

	// 3. Consume nonce.
	nonce, err := util.ParseSIWENonce(req.Message)
	if err != nil || !a.siweNonces.Consume(nonce) {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "invalid or expired nonce",
		})
	}

	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// 4. Look up the registered user.
	user, err := a.store.GetUserByAddress(ctx, tx, signerAddr)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, apiresp.ErrResponse{
				Ok:          false,
				ErrCode:     apiresp.ErrCodeNotFound,
				Description: "address not registered, please sign up first",
			})
		}
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	// 5. Gate on approval status.
	switch user.Status {
	case "PENDING":
		return c.JSON(http.StatusForbidden, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeForbidden,
			Description: "account is pending approval by a superadmin",
		})
	case "REJECTED":
		return c.JSON(http.StatusForbidden, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeForbidden,
			Description: "account has been rejected",
		})
	}

	// 6. Issue JWT and set cookie.
	tokenStr, err := a.generateUserToken(user)
	if err != nil {
		return err
	}
	a.setUserAuthCookie(c, tokenStr)

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Signed in successfully",
		Result: map[string]any{
			"userId": user.ID,
			"role":   user.Role,
		},
	})
}

func (a *API) logoutHandler(c *echo.Context) error {
	clearUserAuthCookie(c)
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Signed out successfully",
		Result:      map[string]any{},
	})
}

// sessionHandler returns the current session's address and chainId.
// Returns 204 No Content when no valid session cookie is present.
func (a *API) sessionHandler(c *echo.Context) error {
	cookie, err := c.Cookie(userAuthCookieName)
	if err != nil || cookie.Value == "" {
		return c.NoContent(http.StatusNoContent)
	}

	token, parseErr := a.parseUserJWT(cookie.Value)
	if parseErr != nil {
		return c.NoContent(http.StatusNoContent)
	}

	return c.JSON(http.StatusOK, map[string]any{
		"address": token.EthereumAddress,
		"chainId": a.chainID,
	})
}
