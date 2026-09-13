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

// updateVoucherProfileHandler lets the current voucher owner edit the whole
// off-chain record their deploy wrote — the same fields the create flow asked
// for, on the same assign-all terms as the admin route. Chain facts (contract
// name, symbol, decimals, owner) stay out of it; the display name is the one
// label that overrides a chain value, and only for catalog reads.
//
// Authorization is the offering rule: the locked voucher row's current owner,
// or a superadmin. Locking and writing in one transaction means an ownership
// correction landing mid-request cannot leave a stale owner authorized.
func (a *API) updateVoucherProfileHandler(c *echo.Context) error {
	address, handled, err := offeringVoucherParam(c)
	if handled || err != nil {
		return err
	}
	req, handled, err := a.bindVoucherProfileRequest(c)
	if handled || err != nil {
		return err
	}
	if req.DisplayName != nil {
		trimmed := strings.TrimSpace(*req.DisplayName)
		req.DisplayName = &trimmed
	}

	_, callerAddress, callerRole := getUserClaims(c)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	voucher, err := a.store.LockVoucherForOfferingCreate(ctx, tx, address)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return offeringVoucherNotFoundError(c)
		}
		return handlePostgresError(c, err)
	}
	if !voucher.Active {
		return offeringConflictError(c, "voucher is retired and its details cannot be changed")
	}
	if !canManageVoucher(callerAddress, callerRole, voucher.OwnerAddress) {
		return handleForbiddenError(c, "only the voucher owner or a superadmin may change this voucher's details")
	}

	saved, handled, err := a.saveVoucherProfile(c, ctx, tx, address, req)
	if handled || err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Voucher profile updated",
		Result: map[string]any{
			"voucher": saved,
		},
	})
}

// bindVoucherProfileRequest decodes and validates a profile write. `handled`
// reports that a 400 has already been written.
func (a *API) bindVoucherProfileRequest(c *echo.Context) (apiresp.UpdateVoucherProfileRequest, bool, error) {
	var req apiresp.UpdateVoucherProfileRequest
	if err := c.Bind(&req); err != nil {
		return req, true, handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return req, true, a.handleValidateError(c, err)
	}

	// A label without coordinates, or coordinates without a label, describes a
	// place nobody can point at. vouchers_geo_valid backstops this at the
	// database, but a 400 names the field the caller got wrong.
	if (req.GeoLat == nil) != (req.GeoLng == nil) {
		return req, true, c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "Validation failed: geoLat and geoLng must be provided together",
		})
	}
	return req, false, nil
}

// saveVoucherProfile writes a validated profile inside the caller's transaction
// and reads the public detail row back so the response can seed the caller's
// cache. `handled` reports that an error response has already been written.
//
// The read-back is also what proves the write landed as intended — the update
// statement returns an id, not a row, and `get-market-voucher-by-address`
// filters on `v.active`, so an inactive voucher 404s here after a successful
// update rather than reporting a save the catalog will not serve.
func (a *API) saveVoucherProfile(
	c *echo.Context,
	ctx context.Context,
	tx pgx.Tx,
	address string,
	req apiresp.UpdateVoucherProfileRequest,
) (store.MarketVoucherDetail, bool, error) {
	// Same vocabulary check the catalog search and the deploy endpoints apply, so
	// a tag cannot enter through here that no filter could ever select.
	if rejected, err := a.rejectUnknownTags(c, tx, req.Tags); rejected || err != nil {
		return store.MarketVoucherDetail{}, true, err
	}

	voucherID, err := a.store.UpdateVoucherProfile(ctx, tx, address, store.VoucherProfile{
		DisplayName:  req.DisplayName,
		Description:  req.Description,
		VoucherUOA:   req.VoucherUoa,
		VoucherValue: req.VoucherValue,
		Email:        req.Email,
		Website:      req.Website,
		Phone:        req.Phone,
		Terms:        req.Terms,
		LocationName: req.LocationName,
		GeoLat:       req.GeoLat,
		GeoLng:       req.GeoLng,
		IconURL:      req.IconURL,
		BannerURL:    req.BannerURL,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.MarketVoucherDetail{}, true, voucherProfileNotFoundError(c)
		}
		return store.MarketVoucherDetail{}, true, handlePostgresError(c, err)
	}

	if err := a.store.ReplaceVoucherTags(ctx, tx, voucherID, req.Tags); err != nil {
		return store.MarketVoucherDetail{}, true, handlePostgresError(c, err)
	}

	voucher, err := a.store.GetMarketVoucherByAddress(ctx, tx, address)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.MarketVoucherDetail{}, true, voucherProfileNotFoundError(c)
		}
		return store.MarketVoucherDetail{}, true, handlePostgresError(c, err)
	}
	return voucher, false, nil
}

func voucherProfileNotFoundError(c *echo.Context) error {
	return c.JSON(http.StatusNotFound, apiresp.ErrResponse{
		Ok:          false,
		ErrCode:     apiresp.ErrCodeNotFound,
		Description: "voucher not found",
	})
}
