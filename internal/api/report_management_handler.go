package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/labstack/echo/v5"
)

// Post management reads. Four new routes rather than a widening of
// GET /api/v2/reports, whose approved-only representation is already cached and
// consumed as the public catalog: returning different rows from the same URL per
// caller would give one cache identity two meanings.
//
//	GET /api/v2/users/me/reports      every status, authored by the caller
//	GET /api/v2/users/me/reports/:id  one complete own post, any status
//	GET /api/v2/admin/reports         every status, every author
//	GET /api/v2/admin/reports/:id     one complete post, any status
//
// All four answer Cache-Control: no-store.

const (
	errReportNextWithoutCursor reportCursorError = "next requires cursorUpdatedAt and cursorId"
	errReportNextValue         reportCursorError = "next must be true or false"
)

// reportPerPageParam applies the feed's page-size rule. Shared with
// reportsListHandler so the console cannot ask for an unmeasured page size.
func reportPerPageParam(c *echo.Context) (uint64, bool, error) {
	raw := c.Request().URL.Query().Get("perPage")
	if raw == "" {
		return reportsDefaultPerPage, false, nil
	}

	parsed, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, true, reportValidationError(c, "perPage must be a non-negative integer")
	}
	if parsed == 0 {
		return reportsDefaultPerPage, false, nil
	}
	return min(parsed, reportsMaxPerPage), false, nil
}

// parseReportPageDirection turns the cursor pair and `next` into one typed
// direction. `next` without a cursor is rejected rather than ignored: there is
// no page either side of the first one, so answering it anyway would teach a
// caller the parameter works.
func parseReportPageDirection(raw string, hasCursor bool) (store.CatalogPage, error) {
	if raw == "" {
		if hasCursor {
			return store.CatalogNextPage, nil
		}
		return store.CatalogFirstPage, nil
	}
	if !hasCursor {
		return store.CatalogFirstPage, errReportNextWithoutCursor
	}

	forward, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return store.CatalogFirstPage, errReportNextValue
	}
	if forward {
		return store.CatalogNextPage, nil
	}
	return store.CatalogPrevPage, nil
}

// reportStatusFilterParam reads the optional exact-status narrowing.
// Case-insensitive in, canonical out; an unknown value is a 400 rather than a
// silently broadened queue.
func reportStatusFilterParam(c *echo.Context) (string, error) {
	status, ok := store.NormalizeReportStatus(c.Request().URL.Query().Get("status"))
	if !ok {
		return "", fmt.Errorf("status must be one of %s", strings.Join(store.ReportStatuses, ", "))
	}
	return status, nil
}

// userReportsListHandler lists every post the authenticated wallet authored. The
// owner is the JWT's address and is not a parameter: an `author` here would be
// another way to ask for someone else's drafts.
func (a *API) userReportsListHandler(c *echo.Context) error {
	_, callerAddress, _ := getUserClaims(c)
	if callerAddress == "" {
		return handleForbiddenError(c, "a user credential is required to list your own posts")
	}
	return a.managedReportsList(c, callerAddress, false)
}

// adminReportsListHandler lists every post in every status. `author` is accepted
// here so the console's per-account panel and its global queue share one
// endpoint instead of the panel reading the approved-only catalog.
func (a *API) adminReportsListHandler(c *echo.Context) error {
	return a.managedReportsList(c, "", true)
}

// managedReportsList backs both lists; the forced-versus-parsed owner is the only
// difference between them.
func (a *API) managedReportsList(c *echo.Context, owner string, allowAuthorParam bool) error {
	query := c.Request().URL.Query()

	perPage, handled, err := reportPerPageParam(c)
	if handled || err != nil {
		return err
	}

	cursor, hasCursor, err := parseReportCursor(query.Get("cursorUpdatedAt"), query.Get("cursorId"))
	if err != nil {
		return reportValidationError(c, err.Error())
	}
	page, err := parseReportPageDirection(query.Get("next"), hasCursor)
	if err != nil {
		return reportValidationError(c, err.Error())
	}

	search, err := parseCatalogRelationshipSearch(c)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	status, err := reportStatusFilterParam(c)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	if allowAuthorParam {
		if owner, err = addressFilterParam(c, "author"); err != nil {
			return respondCatalogSearchError(c, err)
		}
	}
	search.filter.Owner = owner

	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if err := a.store.SetCatalogStatementTimeout(ctx, tx); err != nil {
		return handlePostgresError(c, err)
	}

	if handled, vErr := a.rejectUnknownTags(c, tx, search.filter.Tags); handled || vErr != nil {
		return vErr
	}
	if handled, vErr := a.resolveVoucherFilter(c, tx, &search); handled || vErr != nil {
		return vErr
	}

	reports, err := a.store.ListManagedReports(
		ctx, tx,
		store.ManagedReportFilter{Filter: search.filter, Status: status},
		cursor, page, perPage,
	)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if reports == nil {
		reports = []store.Report{}
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Reports",
		Result:      managedReportPageResult(reports),
	})
}

// managedReportPageResult returns the page plus both edge cursors. `first` and
// `last` rather than the feed's single `next`, because this list pages both
// ways; they are real rows' (updatedAt, id) pairs, so the strict row comparison
// stays duplicate-free across the tied timestamps the import produced. Both are
// null on an empty page.
func managedReportPageResult(reports []store.Report) map[string]any {
	result := map[string]any{"reports": reports, "first": nil, "last": nil}
	if len(reports) == 0 {
		return result
	}
	result["first"] = reportCursorResult(reports[0])
	result["last"] = reportCursorResult(reports[len(reports)-1])
	return result
}

func reportCursorResult(report store.Report) map[string]any {
	return map[string]any{
		"updatedAt": report.UpdatedAt.UTC().Format(time.RFC3339Nano),
		"id":        report.ID,
	}
}

// userReportByIDHandler serves one complete post the caller authored, in any
// status. Another author's post is a 404, not a 403: a 403 would confirm the id
// exists.
func (a *API) userReportByIDHandler(c *echo.Context) error {
	_, callerAddress, _ := getUserClaims(c)
	if callerAddress == "" {
		return handleForbiddenError(c, "a user credential is required to read your own posts")
	}
	return a.managedReportByID(c, callerAddress)
}

func (a *API) adminReportByIDHandler(c *echo.Context) error {
	return a.managedReportByID(c, "")
}

func (a *API) managedReportByID(c *echo.Context, owner string) error {
	id, handled, err := reportIDParam(c)
	if handled || err != nil {
		return err
	}

	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	report, err := a.store.GetManagedReport(ctx, tx, id, owner)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}
	if report == nil {
		return reportNotFoundError(c)
	}

	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Report",
		Result:      map[string]any{"report": report},
	})
}
