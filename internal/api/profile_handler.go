package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/internal/util"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// siweVerifyHandler is a unified sign-in / sign-up endpoint.
//
// If the address is new it is auto-created and immediately APPROVED when
// running in non-production mode (prod = false), allowing first-time WebAuthn
// wallet holders to obtain a JWT without a manual admin step.
func (a *API) siweVerifyHandler(c *echo.Context) error {
	var req apiresp.SIWEVerifyRequest
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	signerAddr, err := util.VerifySIWESignature(req.Message, req.Signature)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "signature verification failed: " + err.Error(),
		})
	}

	msgAddr, err := util.ParseSIWEAddress(req.Message)
	if err != nil || !strings.EqualFold(signerAddr, msgAddr) {
		return c.JSON(http.StatusUnauthorized, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "address mismatch: signer does not match claimed address",
		})
	}

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

	user, lookupErr := a.store.GetUserByAddress(ctx, tx, signerAddr)
	if lookupErr != nil {
		if !errors.Is(lookupErr, pgx.ErrNoRows) {
			return lookupErr
		}

		// First-time: auto-register.
		// WebAuthn users prove key ownership via the SIWE signature — that IS
		// the authorisation proof, so always approve immediately rather than
		// gating on an admin review step.
		status := "APPROVED"
		user, err = a.store.InsertUser(ctx, tx, store.User{
			EthereumAddress: signerAddr,
			Name:            "",
			Email:           "",
			Role:            "USER",
			Status:          status,
			MaxPools:        10,
			MaxTokens:       20,
		})
		if err != nil {
			return err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	if user.Status == "PENDING" {
		return c.JSON(http.StatusForbidden, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeForbidden,
			Description: "account is pending approval",
		})
	}
	if user.Status == "REJECTED" {
		return c.JSON(http.StatusForbidden, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeForbidden,
			Description: "account has been rejected",
		})
	}

	tokenStr, err := a.generateUserToken(user)
	if err != nil {
		return err
	}
	a.setUserAuthCookie(c, tokenStr)

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Authenticated successfully",
		Result: map[string]any{
			"token":     tokenStr,
			"address":   user.EthereumAddress,
			"expiresAt": time.Now().Add(userCookieDuration).Format(time.RFC3339),
		},
	})
}

// profileGetHandler returns the authenticated user's profile.
func (a *API) profileGetHandler(c *echo.Context) error {
	_, addr, _ := getUserClaims(c)
	ctx := c.Request().Context()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	user, err := a.store.GetUserByAddress(ctx, tx, addr)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, apiresp.ErrResponse{
				Ok:          false,
				ErrCode:     apiresp.ErrCodeNotFound,
				Description: "user not found",
			})
		}
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "User profile",
		Result: map[string]any{
			"address":   user.EthereumAddress,
			"name":      user.Name,
			"email":     user.Email,
			"phone":     user.Phone,
			"country":   user.Country,
			"createdAt": user.CreatedAt.Format(time.RFC3339),
		},
	})
}

// profileUpdateHandler stores or updates optional profile fields.
func (a *API) profileUpdateHandler(c *echo.Context) error {
	var req apiresp.UpdateProfileRequest
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	_, addr, _ := getUserClaims(c)
	ctx := c.Request().Context()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	user, err := a.store.UpdateUserProfile(ctx, tx, addr, req.Name, req.Email, req.Phone, req.Country)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, apiresp.ErrResponse{
				Ok:          false,
				ErrCode:     apiresp.ErrCodeNotFound,
				Description: "user not found",
			})
		}
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Profile updated",
		Result: map[string]any{
			"address": user.EthereumAddress,
			"name":    user.Name,
			"email":   user.Email,
			"phone":   user.Phone,
			"country": user.Country,
		},
	})
}
