package api

import (
	"net/http"
	"regexp"
	"strings"

	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/labstack/echo/v5"
)

var (
	symbolPrefixPattern = regexp.MustCompile(`^[A-Za-z0-9]{1,6}$`)
)

// symbolsLookupHandler returns the occupied candidates under one generated
// symbol stem. It is an advisory read rather than a reservation; catalog rows
// and open deployments are the source of occupancy.
func (a *API) symbolsLookupHandler(c *echo.Context) error {
	kind := c.QueryParam("kind")
	prefix := c.QueryParam("prefix")
	if (kind != "voucher" && kind != "pool") || !symbolPrefixPattern.MatchString(prefix) {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "Validation failed: kind must be voucher or pool and prefix must be 1-6 letters or digits",
		})
	}
	prefix = strings.ToUpper(prefix)

	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var taken []string
	if kind == "voucher" {
		taken, err = a.store.ListTakenVoucherSymbols(ctx, tx, prefix)
	} else {
		taken, err = a.store.ListTakenPoolSymbols(ctx, tx, prefix)
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
		Description: "Taken symbols",
		Result: map[string]any{
			"kind":   kind,
			"prefix": prefix,
			"taken":  taken,
		},
	})
}
