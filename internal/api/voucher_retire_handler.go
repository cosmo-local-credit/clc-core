package api

import (
	"errors"
	"net/http"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// retireVoucherHandler takes a voucher out of the public catalog.
//
// Soft, not destructive: it flips `vouchers.active`, which every market, search
// and offering statement already filters on, so the voucher stops resolving
// everywhere the public reads while the reports and offerings that reference it
// keep their foreign keys (42.md 3.1 measured what deleting instead would cost —
// 65% of report→voucher links ride on rows a delete would take with them).
//
// One route for both callers, like the profile and display-name writes beside
// it: a superadmin satisfies canManageVoucher through the role branch, so
// mounting a second copy under /api/v2/admin would only create a handler to
// drift from. Restoring is the asymmetric half and does live under admin —
// an owner who retires by accident asks an operator, rather than discovering a
// toggle they can flip back and forth.
func (a *API) retireVoucherHandler(c *echo.Context) error {
	address, handled, err := offeringVoucherParam(c)
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

	// The lock makes the authorization decision and the write one transaction,
	// for the reason the display-name write gives: a concurrent ownership
	// correction must not leave a stale owner authorized.
	voucher, err := a.store.LockVoucherForOfferingCreate(ctx, tx, address)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return offeringVoucherNotFoundError(c)
		}
		return handlePostgresError(c, err)
	}
	// Activity before ownership, as the offering mutations order it: a retired
	// voucher is already in the asked-for state, and answering 409 to its owner
	// and 403 to everyone else would leak that the address exists.
	if !voucher.Active {
		return offeringConflictError(c, "voucher is already retired")
	}
	if !canManageVoucher(callerAddress, callerRole, voucher.OwnerAddress) {
		return handleForbiddenError(c, "only the voucher owner or a superadmin may retire this voucher")
	}

	if err := a.store.SetVoucherActive(ctx, tx, voucher.ID, false); err != nil {
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	// No saved row on the way out. The other catalog writes read back through
	// GetMarketVoucherByAddress so the caller can seed its cache, but that
	// statement filters `v.active` — here it would answer ErrNoRows for the row
	// this request just wrote. A client's correct next move is to drop the
	// voucher from its cache, not to re-read it.
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Voucher retired",
	})
}
