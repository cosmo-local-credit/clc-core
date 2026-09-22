package api

import (
	"errors"
	"net/http"
	"strings"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// updateVoucherDisplayNameHandler lets the current voucher owner correct the
// catalog label without redeploying the ERC20. It owns one field; the whole
// record is edited through updateVoucherProfileHandler.
func (a *API) updateVoucherDisplayNameHandler(c *echo.Context) error {
	address, handled, err := offeringVoucherParam(c)
	if handled || err != nil {
		return err
	}

	var req apiresp.UpdateVoucherDisplayNameRequest
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}
	if req.DisplayName == "" {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "Validation failed: displayName must not be blank",
		})
	}

	_, callerAddress, callerRole := getUserClaims(c)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// The lock makes the authorization decision and the following write one
	// transaction. A concurrent ownership correction cannot leave a stale owner
	// authorized after their ownership has changed.
	voucher, err := a.store.LockVoucherForOfferingCreate(ctx, tx, address)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return offeringVoucherNotFoundError(c)
		}
		return handlePostgresError(c, err)
	}
	if !voucher.Active {
		return handleConflictError(c, "voucher is retired and its display name cannot be changed")
	}
	if !canManageVoucher(callerAddress, callerRole, voucher.OwnerAddress) {
		return handleForbiddenError(c, "only the voucher owner or a superadmin may change this voucher's display name")
	}

	// Excluding this voucher's own address lets the owner re-save the label they
	// already hold, and lets them reset it to the contract name their own deploy
	// row carries.
	taken, err := a.store.VoucherDisplayNameTaken(ctx, tx, req.DisplayName, &address)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if taken {
		return handleConflictError(c, "a voucher with this name already exists")
	}

	if err := a.store.UpdateVoucherDisplayName(ctx, tx, voucher.ID, req.DisplayName); err != nil {
		return handlePostgresError(c, err)
	}

	// Read the public detail row in the same transaction so the response can seed
	// the caller's cache without a follow-up read that might be answered stale.
	saved, err := a.store.GetMarketVoucherByAddress(ctx, tx, address)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Voucher display name updated",
		Result: map[string]any{
			"voucher": saved,
		},
	})
}
