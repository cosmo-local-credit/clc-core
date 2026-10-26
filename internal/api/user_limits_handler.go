package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

func handleResourceLimitError(c *echo.Context, err error) error {
	var limit *store.ResourceLimitError
	if !errors.As(err, &limit) {
		return handlePostgresError(c, err)
	}
	return c.JSON(http.StatusTooManyRequests, apiresp.ErrResponse{
		Ok: false, ErrCode: apiresp.ErrCodeLimitExceeded, Description: limit.Error(),
		Result: map[string]any{"resource": limit.Resource, "used": limit.Used, "limit": limit.Limit},
	})
}

func (a *API) userResourceUsageHandler(c *echo.Context) error {
	id, callerAddress, callerRole := getUserClaims(c)
	voucherAddress := strings.TrimSpace(c.QueryParam("voucher"))
	if voucherAddress != "" && !common.IsHexAddress(voucherAddress) {
		return respondCatalogSearchError(c, errors.New("voucher must be a valid address"))
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var usage store.UserResourceUsage
	if voucherAddress == "" {
		usage, err = a.store.GetUserResourceUsage(ctx, tx, id)
	} else {
		voucher, readErr := a.store.GetAdminVoucherByAddress(ctx, tx, voucherAddress)
		if errors.Is(readErr, pgx.ErrNoRows) {
			return offeringVoucherNotFoundError(c)
		}
		if readErr != nil {
			return handlePostgresError(c, readErr)
		}
		if callerRole != "SUPERADMIN" &&
			(voucher.OwnerAddress == nil || !strings.EqualFold(*voucher.OwnerAddress, a.actingOwner(ctx, tx, callerAddress, voucher.OwnerAddress))) {
			return handleForbiddenError(c, "only the voucher owner or a superadmin may read its offering allowance")
		}
		usage, err = a.store.GetUserResourceUsageForVoucher(ctx, tx, id, voucherAddress)
	}
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, apiresp.OKResponse{Ok: true, Result: map[string]any{"usage": usage}})
}
