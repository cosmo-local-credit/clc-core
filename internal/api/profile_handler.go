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

func profileUpdateFromRequest(address string, req apiresp.UpdateProfileRequest) (store.UserProfileUpdate, error) {
	update := store.UserProfileUpdate{
		Address:           address,
		Name:              req.Name,
		Email:             req.Email,
		Phone:             req.Phone,
		Country:           req.Country,
		PreferredCurrency: req.PreferredCurrency,
		Bio:               req.Bio,
		Website:           req.Website,
		ClearLocation:     req.ClearLocation,
		GeoLat:            req.GeoLat,
		GeoLng:            req.GeoLng,
		ClearAvatar:       req.ClearAvatar,
		AvatarURL:         req.AvatarURL,
	}

	if req.ClearLocation && (req.LocationName != nil || req.GeoLat != nil || req.GeoLng != nil) {
		return update, errors.New("location fields and clearLocation cannot both be supplied")
	}
	if req.ClearAvatar && req.AvatarURL != nil {
		return update, errors.New("avatarUrl and clearAvatar cannot both be supplied")
	}
	if (req.GeoLat == nil) != (req.GeoLng == nil) {
		return update, errors.New("geoLat and geoLng must be provided together")
	}
	if req.LocationName != nil {
		locationName := strings.TrimSpace(*req.LocationName)
		if locationName == "" {
			return update, errors.New("locationName cannot be blank")
		}
		update.LocationName = &locationName
	}
	if req.PreferredCurrency != nil {
		currency := strings.ToUpper(strings.TrimSpace(*req.PreferredCurrency))
		update.PreferredCurrency = &currency
	}
	if req.Bio != nil {
		bio := strings.TrimSpace(*req.Bio)
		update.Bio = &bio
	}
	if req.Website != nil {
		website := strings.TrimSpace(*req.Website)
		update.Website = &website
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
			"address":           user.EthereumAddress,
			"name":              user.Name,
			"email":             user.Email,
			"phone":             user.Phone,
			"country":           user.Country,
			"preferredCurrency": user.PreferredCurrency,
			"bio":               user.Bio,
			"website":           user.Website,
			"locationName":      user.LocationName,
			"geoLat":            user.GeoLat,
			"geoLng":            user.GeoLng,
			"avatarUrl":         user.AvatarURL,
			"createdAt":         user.CreatedAt.Format(time.RFC3339),
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
		// email/phone are unique across users: claiming one already bound to
		// another account is a caller-fixable 409, not an internal error.
		if conflict, ok := userIdentityConflict(err); ok {
			return c.JSON(http.StatusConflict, conflict)
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
			"address":           user.EthereumAddress,
			"name":              user.Name,
			"email":             user.Email,
			"phone":             user.Phone,
			"country":           user.Country,
			"preferredCurrency": user.PreferredCurrency,
			"bio":               user.Bio,
			"website":           user.Website,
			"locationName":      user.LocationName,
			"geoLat":            user.GeoLat,
			"geoLng":            user.GeoLng,
			"avatarUrl":         user.AvatarURL,
		},
	})
}
