package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/labstack/echo/v5"
)

type (
	// reportCursorError is a cursor validation failure, phrased for the client.
	reportCursorError string
)

const (
	// reportsDefaultPerPage matches what the feed renders in one screenful.
	reportsDefaultPerPage = 20
	reportsMaxPerPage     = 100

	errReportCursorIncomplete reportCursorError = "cursorUpdatedAt and cursorId must be sent together"
	errReportCursorTime       reportCursorError = "cursorUpdatedAt must be an RFC3339 timestamp"
	errReportCursorID         reportCursorError = "cursorId must be a positive integer"
)

// reportsListHandler serves the approved report feed, newest update first.
//
// It does not use the shared cursor helper: that helper keys on a single
// numeric id, and this feed orders by updated_at. On the imported catalog those
// two orders agree on 4 of 3,730 rows, so an id cursor would silently skip and
// repeat rows. The cursor is therefore the (updatedAt, id) pair the last row
// carried, and it is echoed back as `next`.
//
// Optional `q`, `tags`, `tagMatch`, `vouchers`, `voucherMatch`, and `facets` narrow the feed
// (SEARCH_FILTER_PLAN.md Slice 4). `q` matches title and description; report body
// search stays parked. Report tags are the reports' own `report_tags`, which each
// row already returns, so a filtered row carries the tag that matched it. The
// response shape is unchanged when the parameters are absent and gains the
// additive `tagFacets` and `voucherFacets` keys when `facets=true` is asked for
// on a first page.
func (a *API) reportsListHandler(c *echo.Context) error {
	query := c.Request().URL.Query()

	perPage := uint64(reportsDefaultPerPage)
	if raw := query.Get("perPage"); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return reportValidationError(c, "perPage must be a non-negative integer")
		}
		if parsed > 0 {
			perPage = min(parsed, reportsMaxPerPage)
		}
	}

	cursor, hasCursor, err := parseReportCursor(query.Get("cursorUpdatedAt"), query.Get("cursorId"))
	if err != nil {
		return reportValidationError(c, err.Error())
	}

	search, err := parseCatalogRelationshipSearch(c)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	if search.filter.Owner, err = addressFilterParam(c, "author"); err != nil {
		return respondCatalogSearchError(c, err)
	}

	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Bound the read at the database, as the catalog lists do: a pathological plan must not
	// hold the connection past the timeout PostgreSQL does not impose by default.
	if err := a.store.SetCatalogStatementTimeout(ctx, tx); err != nil {
		return handlePostgresError(c, err)
	}

	// Unknown tags fail the whole request before anything is read. Dropping them silently
	// would answer a different question than the caller asked and report a facet count for a
	// filter that was never applied.
	if handled, vErr := a.rejectUnknownTags(c, tx, search.filter.Tags); handled || vErr != nil {
		return vErr
	}
	if handled, vErr := a.resolveVoucherFilter(c, tx, &search); handled || vErr != nil {
		return vErr
	}

	reports, err := a.store.ListReportsFiltered(ctx, tx, search.filter, cursor, hasCursor, perPage)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if reports == nil {
		reports = []store.Report{}
	}

	// A facet describes the whole filtered set rather than the page, so it is computed for the
	// first page only and the key is simply absent from cursor pages.
	var facets []store.TagFacet
	var voucherFacets []store.VoucherFacet
	if search.facets && !hasCursor {
		facets, err = a.store.ListReportTagFacets(ctx, tx, search.filter)
		if err != nil {
			return handlePostgresError(c, err)
		}
		voucherFacets, err = a.store.ListReportVoucherFacets(ctx, tx, search.filter)
		if err != nil {
			return handlePostgresError(c, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	// A short page is the end of the feed, so no cursor is offered and the
	// client has an unambiguous stop signal rather than an emptiness heuristic.
	result := map[string]any{"reports": reports, "next": nil}
	if facets != nil {
		result["tagFacets"] = facets
	}
	if voucherFacets != nil {
		result["voucherFacets"] = voucherFacets
	}
	if uint64(len(reports)) == perPage {
		last := reports[len(reports)-1]
		result["next"] = map[string]any{
			"updatedAt": last.UpdatedAt.UTC().Format(time.RFC3339Nano),
			"id":        last.ID,
		}
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Reports",
		Result:      result,
	})
}

// reportByIDHandler serves one approved report with its extracted images and
// field-report form. An unapproved report is a 404, not a 403: whether a draft
// exists is not public information.
func (a *API) reportByIDHandler(c *echo.Context) error {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		return reportValidationError(c, "id must be a positive integer")
	}

	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	report, err := a.store.GetReport(ctx, tx, id)
	if err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	if report == nil {
		return c.JSON(http.StatusNotFound, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeNotFound,
			Description: "report not found",
		})
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Report",
		Result:      map[string]any{"report": report},
	})
}

// parseReportCursor requires both halves of the keyset position or neither.
// Accepting one alone would quietly return the first page again.
func parseReportCursor(updatedAt, id string) (store.ReportCursor, bool, error) {
	if updatedAt == "" && id == "" {
		return store.ReportCursor{}, false, nil
	}
	if updatedAt == "" || id == "" {
		return store.ReportCursor{}, false, errReportCursorIncomplete
	}

	parsedTime, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return store.ReportCursor{}, false, errReportCursorTime
	}
	parsedID, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		return store.ReportCursor{}, false, errReportCursorID
	}
	return store.ReportCursor{UpdatedAt: parsedTime, ID: parsedID}, true, nil
}

func (e reportCursorError) Error() string { return string(e) }

func reportValidationError(c *echo.Context, description string) error {
	return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
		Ok:          false,
		ErrCode:     apiresp.ErrCodeValidationFailed,
		Description: description,
	})
}
