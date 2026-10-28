package api

import (
	"errors"
	"net/http"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

func setPrivateNoStore(c *echo.Context) {
	c.Response().Header().Set("Cache-Control", "private, no-store")
}

// voucherInfoHandler returns contact-bearing voucher metadata only after the
// caller has established a user session. Active vouchers are available to any
// signed-in user; retired rows retain their owner-or-administrator boundary.
func (a *API) voucherInfoHandler(c *echo.Context) error {
	address, handled, err := adminUserAddress(c)
	if handled || err != nil {
		return err
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	voucher, err := a.store.GetAdminVoucherByAddress(ctx, tx, address)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return voucherProfileNotFoundError(c)
		}
		return handlePostgresError(c, err)
	}
	if !voucher.Active {
		_, callerAddress, callerRole := getUserClaims(c)
		if !a.callerMayManageRecord(ctx, tx, callerAddress, callerRole, voucher.OwnerAddress) {
			return voucherProfileNotFoundError(c)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	setPrivateNoStore(c)
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok: true, Description: "Voucher information",
		Result: map[string]any{"voucher": voucher.MarketVoucherDetail},
	})
}

// poolInfoHandler applies the same visibility rule as voucherInfoHandler to a
// pool's complete catalog record.
func (a *API) poolInfoHandler(c *echo.Context) error {
	address, handled, err := adminUserAddress(c)
	if handled || err != nil {
		return err
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	pool, err := a.store.GetPoolByAddress(ctx, tx, address)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return poolProfileNotFoundError(c)
		}
		return handlePostgresError(c, err)
	}
	if !pool.Active {
		_, callerAddress, callerRole := getUserClaims(c)
		if !a.callerMayManageRecord(ctx, tx, callerAddress, callerRole, pool.OwnerAddress) {
			return poolProfileNotFoundError(c)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	setPrivateNoStore(c)
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok: true, Description: "Pool information",
		Result: map[string]any{"pool": pool},
	})
}

type (
	wantContactResponse struct {
		Text  *string `json:"text,omitempty"`
		Email *string `json:"email,omitempty"`
		Phone *string `json:"phone,omitempty"`
	}
)

// wantContactHandler exposes only the contact methods the Want creator selected
// and only while the Want is publicly active.
func (a *API) wantContactHandler(c *echo.Context) error {
	id, err := wantIDParam(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	want, err := a.store.GetWant(ctx, tx, id, nil)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if want == nil {
		return wantNotFound(c)
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	var contact *wantContactResponse
	if want.ContactText != nil || want.Email != nil || want.Phone != nil {
		contact = &wantContactResponse{
			Text: want.ContactText, Email: want.Email, Phone: want.Phone,
		}
	}
	setPrivateNoStore(c)
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok: true, Description: "Want contact",
		Result: map[string]any{"contact": contact},
	})
}
