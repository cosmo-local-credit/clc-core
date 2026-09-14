package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// updatePoolProfileHandler lets the current pool owner edit the mutable half of
// their catalog row. The current owner is read under a row lock in the same
// transaction as the write, so a concurrent ownership correction cannot leave
// a stale owner authorized. A superadmin may edit any pool, including an
// imported row whose owner is not yet known.
func (a *API) updatePoolProfileHandler(c *echo.Context) error {
	address, handled, err := adminUserAddress(c)
	if handled || err != nil {
		return err
	}
	req, handled, err := a.bindPoolProfileRequest(c)
	if handled || err != nil {
		return err
	}

	_, callerAddress, callerRole := getUserClaims(c)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	pool, err := a.store.LockPoolForProfileUpdate(ctx, tx, address)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return poolProfileNotFoundError(c)
		}
		return handlePostgresError(c, err)
	}
	if !canManageCatalogRecord(callerAddress, callerRole, pool.OwnerAddress) {
		return handleForbiddenError(c,
			"only the pool owner or a superadmin may change this pool's details")
	}

	saved, handled, err := a.savePoolProfile(c, ctx, tx, address, req)
	if handled || err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	return respondPoolProfile(c, saved)
}

// bindPoolProfileRequest keeps the owner and admin routes on one wire contract.
// handled reports that a 400 has already been written.
func (a *API) bindPoolProfileRequest(c *echo.Context) (apiresp.UpdatePoolProfileRequest, bool, error) {
	var req apiresp.UpdatePoolProfileRequest
	if err := c.Bind(&req); err != nil {
		return req, true, handleBindError(c, err)
	}
	if req.DisplayName != nil {
		trimmed := strings.TrimSpace(*req.DisplayName)
		req.DisplayName = &trimmed
	}
	if err := c.Validate(req); err != nil {
		return req, true, a.handleValidateError(c, err)
	}

	if (req.GeoLat == nil) != (req.GeoLng == nil) {
		return req, true, c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "Validation failed: geoLat and geoLng must be provided together",
		})
	}
	return req, false, nil
}

// savePoolProfile applies the profile and tag set atomically, then reads the
// effective catalog row back so the client can seed its detail query directly.
func (a *API) savePoolProfile(
	c *echo.Context,
	ctx context.Context,
	tx pgx.Tx,
	address string,
	req apiresp.UpdatePoolProfileRequest,
) (store.Pool, bool, error) {
	if rejected, err := a.rejectUnknownTags(c, tx, req.Tags); rejected || err != nil {
		return store.Pool{}, true, err
	}

	poolID, err := a.store.UpdatePoolProfile(ctx, tx, address, store.PoolProfile{
		DisplayName:   req.DisplayName,
		Description:   req.Description,
		UnitOfAccount: req.UnitOfAccount,
		Email:         req.Email,
		Website:       req.Website,
		Phone:         req.Phone,
		Terms:         req.Terms,
		LocationName:  req.LocationName,
		ImageURL:      req.ImageURL,
		BannerURL:     req.BannerURL,
		GeoLat:        req.GeoLat,
		GeoLng:        req.GeoLng,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.Pool{}, true, poolProfileNotFoundError(c)
		}
		return store.Pool{}, true, handlePostgresError(c, err)
	}

	if err := a.store.ReplacePoolTags(ctx, tx, poolID, req.Tags); err != nil {
		return store.Pool{}, true, handlePostgresError(c, err)
	}

	pool, err := a.store.GetPoolByAddress(ctx, tx, address)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.Pool{}, true, poolProfileNotFoundError(c)
		}
		return store.Pool{}, true, handlePostgresError(c, err)
	}
	return pool, false, nil
}

func respondPoolProfile(c *echo.Context, pool store.Pool) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Pool profile updated",
		Result:      map[string]any{"pool": pool},
	})
}

func poolProfileNotFoundError(c *echo.Context) error {
	return c.JSON(http.StatusNotFound, apiresp.ErrResponse{
		Ok:          false,
		ErrCode:     apiresp.ErrCodeNotFound,
		Description: "pool not found",
	})
}
