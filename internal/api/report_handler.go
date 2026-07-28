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

	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var reports []store.Report
	if hasCursor {
		reports, err = a.store.ListReportsNext(ctx, tx, cursor, perPage)
	} else {
		reports, err = a.store.ListReports(ctx, tx, perPage)
	}
	if err != nil {
		return handlePostgresError(c, err)
	}
	if reports == nil {
		reports = []store.Report{}
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	// A short page is the end of the feed, so no cursor is offered and the
	// client has an unambiguous stop signal rather than an emptiness heuristic.
	result := map[string]any{"reports": reports, "next": nil}
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
