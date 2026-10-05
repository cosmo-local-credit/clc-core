package api

import (
	"net/http"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/labstack/echo/v5"
)

// Per-kind caps, mirroring vouchers_display_name_valid (migration 030) and
// pools_display_name_valid (migration 032). A name this endpoint calls free but
// the deploy would reject on length is worse than no answer at all.
const (
	maxVoucherNameLength = 120
	maxPoolNameLength    = 80
)

// namesLookupHandler answers whether a voucher or pool label is still free. Like
// symbolsLookupHandler it is an advisory read rather than a reservation — the
// deploy transaction is the claim, and it re-runs this check against the same
// tables. The client uses it to tell the user before they have walked the whole
// creation wizard.
//
// exclude names an address whose own label should not count as a conflict, so a
// rename form can check the value already in its field.
func (a *API) namesLookupHandler(c *echo.Context) error {
	kind := c.QueryParam("kind")
	if kind != "voucher" && kind != "pool" {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "Validation failed: kind must be voucher or pool",
		})
	}

	// Normalized first: padding must not buy extra characters against the cap,
	// and the response echoes back the form the comparison actually used.
	name := store.NormalizeCatalogTerm(c.QueryParam("name"))
	limit := maxVoucherNameLength
	if kind == "pool" {
		limit = maxPoolNameLength
	}
	if name == "" || len([]rune(name)) > limit {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "Validation failed: name must not be blank and must be within the length limit for this kind",
		})
	}

	var exclude *string
	if raw := c.QueryParam("exclude"); raw != "" {
		if !common.IsHexAddress(raw) {
			return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
				Ok:          false,
				ErrCode:     apiresp.ErrCodeValidationFailed,
				Description: "Validation failed: exclude must be a contract address",
			})
		}
		exclude = &raw
	}

	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var taken bool
	if kind == "voucher" {
		taken, err = a.store.VoucherDisplayNameTaken(ctx, tx, name, exclude)
	} else {
		taken, err = a.store.PoolDisplayNameTaken(ctx, tx, name, exclude)
	}
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Name availability",
		Result: map[string]any{
			"kind":      kind,
			"name":      name,
			"available": !taken,
		},
	})
}
