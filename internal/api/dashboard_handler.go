package api

import (
	"context"
	"net/http"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/labstack/echo/v5"
)

func (a *API) adminDashboardStatsHandler(c *echo.Context) error {
	window, err := parseDashboardTimeWindow(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: err.Error(),
		})
	}
	if window.BucketSeconds < 1 {
		window.BucketSeconds = 60
	}

	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	snap, err := a.store.DashboardCatalogSnapshot(ctx, tx, window.Since, window.Until)
	if err != nil {
		return handlePostgresError(c, err)
	}
	catalogSeries, err := a.store.DashboardCatalogSeries(ctx, tx, window.Since, window.Until, window.BucketSeconds)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	var transactions *int64
	txSeries := []store.DashboardTransactionPoint{}
	if a.federationStore != nil {
		fedCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		series, fedErr := a.federationStore.DashboardTransactionSeries(fedCtx, window.Since, window.Until, window.BucketSeconds)
		if fedErr != nil {
			a.logg.Warn("dashboard stats: federation transaction series failed", "error", fedErr)
		} else {
			txSeries = series
			var n int64
			for _, point := range series {
				n += point.Transactions
			}
			transactions = &n
		}
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Admin dashboard totals",
		Result: map[string]any{
			"range": map[string]any{
				"preset": window.Preset,
				"since":  window.Since.UTC().Format(time.RFC3339),
				"until":  window.Until.UTC().Format(time.RFC3339),
				"bucket": formatBucketLabel(window.Bucket),
			},
			"pools":             snap.Pools,
			"vouchers":          snap.Vouchers,
			"accounts":          snap.Accounts,
			"reports":           snap.Reports,
			"transactions":      transactions,
			"catalogSeries":     catalogSeries,
			"transactionSeries": txSeries,
		},
	})
}
