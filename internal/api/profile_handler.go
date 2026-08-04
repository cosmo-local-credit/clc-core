package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/cosmo-local-credit/clc-core/internal/util"
	"github.com/cosmo-local-credit/clc-core/internal/worker"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
	"github.com/riverqueue/river"
)

func newPendingSIWEUser(address string) store.User {
	return store.User{
		EthereumAddress: address,
		Name:            "",
		Email:           pendingSIWEEmail(address),
		Role:            "USER",
		Status:          "APPROVED",
		MaxPools:        10,
		MaxTokens:       20,
	}
}

func pendingSIWEEmail(address string) string {
	return "pending+" + strings.TrimPrefix(strings.ToLower(address), "0x") + "@clc.local"
}

func userLocationResult(user store.User) any {
	if user.LocationName == "" || user.GeoLat == nil || user.GeoLng == nil {
		return nil
	}

	return map[string]any{
		"locationName": user.LocationName,
		"lat":          *user.GeoLat,
		"lng":          *user.GeoLng,
	}
}

func profileUpdateFromRequest(address string, req apiresp.UpdateProfileRequest) (store.UserProfileUpdate, error) {
	update := store.UserProfileUpdate{
		Address: address,
		Name:    req.Name,
		Email:   req.Email,
		Phone:   req.Phone,
		Country: req.Country,
	}

	if req.Location != nil && req.ClearLocation {
		return update, errors.New("location and clearLocation cannot both be supplied")
	}

	if req.Location != nil {
		req.Location.LocationName = strings.TrimSpace(req.Location.LocationName)
		if req.Location.LocationName == "" {
			return update, errors.New("locationName cannot be blank")
		}

		update.UpdateLocation = true
		update.LocationName = req.Location.LocationName
		update.GeoLat = &req.Location.Lat
		update.GeoLng = &req.Location.Lng
	} else if req.ClearLocation {
		update.UpdateLocation = true
	}

	return update, nil
}

// siweVerifyHandler is a unified sign-in / sign-up endpoint.
//
// If the address is new it is auto-created as PENDING. A background worker may
// later approve it if the address exists in the legacy federation database; an
// admin can also approve it manually.
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

	enqueueLegacyApproval := false
	user, lookupErr := a.store.GetUserByAddress(ctx, tx, signerAddr)
	if lookupErr != nil {
		if !errors.Is(lookupErr, pgx.ErrNoRows) {
			return lookupErr
		}

		// First-time: auto-register as inactive. Approval happens either by an
		// admin or asynchronously after checking the legacy federation DB.
		user, err = a.store.InsertUser(ctx, tx, newPendingSIWEUser(signerAddr))
		if err != nil {
			return err
		}
		enqueueLegacyApproval = true
	} else if user.Status == "PENDING" {
		// Retry the async legacy approval check on later attempts in case the
		// original job failed or federation DB was unavailable during signup.
		enqueueLegacyApproval = true
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	if enqueueLegacyApproval && a.queueClient != nil && a.federationStore != nil {
		if _, err := a.queueClient.Insert(ctx, worker.ApproveLegacyUserArgs{
			Address: signerAddr,
		}, &river.InsertOpts{MaxAttempts: 5}); err != nil {
			a.logg.Warn("failed to enqueue legacy user approval job", "address", signerAddr, "error", err)
		}
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
			"location":  userLocationResult(user),
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
	update, err := profileUpdateFromRequest(addr, req)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: err.Error(),
		})
	}
	ctx := c.Request().Context()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	user, err := a.store.UpdateUserProfile(ctx, tx, update)
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
			"address":  user.EthereumAddress,
			"name":     user.Name,
			"email":    user.Email,
			"phone":    user.Phone,
			"country":  user.Country,
			"location": userLocationResult(user),
		},
	})
}
