package api

import (
	"errors"
	"golang.org/x/text/unicode/norm"
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
	}
}

func pendingSIWEEmail(address string) string {
	return "pending+" + strings.TrimPrefix(strings.ToLower(address), "0x") + "@clc.local"
}

func profileUpdateFromRequest(address string, req apiresp.UpdateProfileRequest) (store.UserProfileUpdate, error) {
	update := store.UserProfileUpdate{
		Address:           address,
		Username:          req.Username,
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
	if req.PublicProfile != nil {
		update.PublicProfile = &store.ProfileVisibility{
			Enabled: req.PublicProfile.Enabled, Avatar: req.PublicProfile.Avatar,
			Name: req.PublicProfile.Name, Bio: req.PublicProfile.Bio,
			Website: req.PublicProfile.Website, Email: req.PublicProfile.Email,
			Phone: req.PublicProfile.Phone, Country: req.PublicProfile.Country,
			LocationName:      req.PublicProfile.LocationName,
			PreciseLocation:   req.PublicProfile.PreciseLocation,
			PreferredCurrency: req.PublicProfile.PreferredCurrency,
			Vouchers:          req.PublicProfile.Vouchers, Offers: req.PublicProfile.Offers,
			Wants: req.PublicProfile.Wants, Pools: req.PublicProfile.Pools,
			Posts: req.PublicProfile.Posts, Catalogs: req.PublicProfile.Catalogs,
		}
	}

	if req.Username != nil {
		name := norm.NFC.String(strings.TrimSpace(*req.Username))
		if !store.ValidUsername(name) {
			return update, errors.New("username must contain 1–80 characters")
		}
		update.Username = &name
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
		currency := strings.Join(strings.Fields(*req.PreferredCurrency), " ")
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
			if isRetiredAddressError(err) {
				return addressRetired(c)
			}
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
	visibility, err := a.store.GetProfileVisibility(ctx, tx, addr)
	if err != nil {
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
			"username":          user.Username,
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
			"publicProfile":     visibility,
		},
	})
}

// profileUpdateHandler stores or updates optional profile fields.
func (a *API) profileUpdateHandler(c *echo.Context) error {
	var req apiresp.UpdateProfileRequest
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	_, addr, _ := getUserClaims(c)
	if req.VisibilityOnly {
		if req.PublicProfile == nil {
			return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
				Ok: false, ErrCode: apiresp.ErrCodeValidationFailed,
				Description: "publicProfile is required when visibilityOnly is true",
			})
		}
		return a.profileVisibilityUpdateHandler(c, addr, *req.PublicProfile)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

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

	if update.PreferredCurrency != nil {
		entry, handled, err := a.ensureUnitOfAccount(c, tx, *update.PreferredCurrency)
		if handled || err != nil {
			return err
		}
		update.PreferredCurrency = &entry.Value
	}

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
	visibility, err := a.store.GetProfileVisibility(ctx, tx, addr)
	if err != nil {
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
			"username":          user.Username,
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
			"publicProfile":     visibility,
		},
	})
}

// profileVisibilityUpdateHandler lets an approved legacy account change only
// publication consent. It deliberately ignores every base-profile field in an
// explicit visibility-only request so a stale form cannot erase or replace
// identity, contacts, location, avatar, or preferences.
func (a *API) profileVisibilityUpdateHandler(
	c *echo.Context,
	address string,
	requested apiresp.ProfileVisibility,
) error {
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	visibility := store.ProfileVisibility{
		Enabled: requested.Enabled, Avatar: requested.Avatar,
		Name: requested.Name, Bio: requested.Bio,
		Website: requested.Website, Email: requested.Email,
		Phone: requested.Phone, Country: requested.Country,
		LocationName:      requested.LocationName,
		PreciseLocation:   requested.PreciseLocation,
		PreferredCurrency: requested.PreferredCurrency,
		Vouchers:          requested.Vouchers, Offers: requested.Offers,
		Wants: requested.Wants, Pools: requested.Pools,
		Posts: requested.Posts, Catalogs: requested.Catalogs,
	}
	if err := a.store.UpdateProfileVisibility(ctx, tx, address, visibility); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, apiresp.ErrResponse{
				Ok: false, ErrCode: apiresp.ErrCodeNotFound, Description: "user not found",
			})
		}
		return err
	}
	user, err := a.store.GetUserByAddress(ctx, tx, address)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, apiresp.ErrResponse{
				Ok: false, ErrCode: apiresp.ErrCodeNotFound, Description: "user not found",
			})
		}
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok: true, Description: "Profile updated",
		Result: map[string]any{
			"address": user.EthereumAddress, "name": user.Name,
			"username": user.Username, "email": user.Email, "phone": user.Phone,
			"country": user.Country, "preferredCurrency": user.PreferredCurrency,
			"bio": user.Bio, "website": user.Website,
			"locationName": user.LocationName, "geoLat": user.GeoLat, "geoLng": user.GeoLng,
			"avatarUrl": user.AvatarURL, "publicProfile": visibility,
		},
	})
}
