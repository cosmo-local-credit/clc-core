package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

type (
	// reportCursorError is a cursor validation failure, phrased for the client.
	reportCursorError string

	publicReportResponse struct {
		ID           uint64                   `json:"id"`
		Title        string                   `json:"title"`
		Description  string                   `json:"description"`
		ImageURL     *string                  `json:"imageUrl"`
		GeoLat       *float64                 `json:"geoLat"`
		GeoLng       *float64                 `json:"geoLng"`
		PeriodFrom   *time.Time               `json:"periodFrom"`
		PeriodTo     *time.Time               `json:"periodTo"`
		CreatedAt    time.Time                `json:"createdAt"`
		UpdatedAt    time.Time                `json:"updatedAt"`
		Tags         []string                 `json:"tags"`
		Vouchers     []store.ReportVoucher    `json:"vouchers"`
		Pools        []store.ReportPool       `json:"pools"`
		Body         *string                  `json:"body,omitempty"`
		HostName     *string                  `json:"hostName,omitempty"`
		Participants *string                  `json:"participants,omitempty"`
		TimeTaken    *string                  `json:"timeTaken,omitempty"`
		Images       []store.ReportImage      `json:"images,omitempty"`
		Attachments  []store.ReportAttachment `json:"attachments,omitempty"`
	}

	reportWriteRelations struct {
		vouchers []store.VoucherRef
		pools    []store.PoolRef
	}
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

	perPage, handled, err := reportPerPageParam(c)
	if handled || err != nil {
		return err
	}

	cursor, hasCursor, err := parseReportCursor(query.Get("cursorUpdatedAt"), query.Get("cursorId"))
	if err != nil {
		return reportValidationError(c, err.Error())
	}

	if _, exists := query["author"]; exists {
		return reportValidationError(c, "author is not available on public reports")
	}
	search, err := parseReportRelationshipSearch(c)
	if err != nil {
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
	if handled, vErr := a.resolveNearbyCatalog(c, tx, &search); handled || vErr != nil {
		return vErr
	}
	if handled, vErr := a.rejectUnknownTags(c, tx, search.filter.Tags); handled || vErr != nil {
		return vErr
	}
	if handled, vErr := a.resolveVoucherFilter(c, tx, &search); handled || vErr != nil {
		return vErr
	}
	if handled, vErr := a.resolvePoolFilter(c, tx, &search); handled || vErr != nil {
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
	var poolFacets []store.PoolFacet
	if search.facets && !hasCursor {
		facets, err = a.store.ListReportTagFacets(ctx, tx, search.filter)
		if err != nil {
			return handlePostgresError(c, err)
		}
		voucherFacets, err = a.store.ListReportVoucherFacets(ctx, tx, search.filter)
		if err != nil {
			return handlePostgresError(c, err)
		}
		poolFacets, err = a.store.ListReportPoolFacets(ctx, tx, search.filter)
		if err != nil {
			return handlePostgresError(c, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	// A short page is the end of the feed, so no cursor is offered and the
	// client has an unambiguous stop signal rather than an emptiness heuristic.
	publicReports := make([]publicReportResponse, 0, len(reports))
	for _, report := range reports {
		publicReports = append(publicReports, publicReport(report))
	}
	result := map[string]any{"reports": publicReports, "next": nil}
	if facets != nil {
		result["tagFacets"] = facets
	}
	if voucherFacets != nil {
		result["voucherFacets"] = voucherFacets
	}
	if poolFacets != nil {
		result["poolFacets"] = poolFacets
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
		Result:      map[string]any{"report": publicReport(*report)},
	})
}

// reportCreateHandler creates a post attributed to the authenticated wallet.
// authorAddress is never accepted from JSON: the JWT identity is the provenance
// record, and allowing a caller to supply it would defeat the owner check on
// later edits. Every post is published as it is created: there is no review
// queue, and a superadmin's remaining levers are reject and delete.
func (a *API) reportCreateHandler(c *echo.Context) error {
	report, relations, handled, err := a.bindReportWrite(c)
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

	if handled, err := a.resolveReportWriteRelations(c, tx, &report, relations, callerAddress, callerRole); handled || err != nil {
		return err
	}

	id, err := a.store.CreateReport(ctx, tx, report, callerAddress, store.ReportStatusApproved)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := a.store.ReplaceReportRelations(ctx, tx, id, report); err != nil {
		return handlePostgresError(c, err)
	}

	created, err := a.store.GetReportForMutation(ctx, tx, id)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if created == nil {
		return fmt.Errorf("created report %d could not be read back", id)
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	return c.JSON(http.StatusCreated, apiresp.OKResponse{
		Ok:          true,
		Description: "Report created",
		Result:      map[string]any{"report": created},
	})
}

// reportApproveHandler is the only path that promotes an existing post to
// APPROVED, and the route is guarded by requireSuperAdmin. Content edits use a
// different store statement that never assigns status, so authors can edit both
// submitted and approved posts without changing their review state.
func (a *API) reportApproveHandler(c *echo.Context) error {
	id, handled, err := reportIDParam(c)
	if handled || err != nil {
		return err
	}

	_, callerAddress, _ := getUserClaims(c)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if err := a.store.ApproveReport(ctx, tx, id, callerAddress); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return reportNotFoundError(c)
		}
		return handlePostgresError(c, err)
	}
	approved, err := a.store.GetReportForMutation(ctx, tx, id)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if approved == nil {
		return fmt.Errorf("approved report %d could not be read back", id)
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Report approved",
		Result:      map[string]any{"report": approved},
	})
}

// reportUpdateHandler is assign-all PUT: scalars and all four relation sets are
// one atomic replacement. The immutable original author may edit their own
// post, and a superadmin may edit any post. LockReportAuthor makes that decision
// against the same row version the transaction updates.
//
// An author's edit publishes the post again; a superadmin's preserves its
// status. See reportUpdateStatus for why.
func (a *API) reportUpdateHandler(c *echo.Context) error {
	id, handled, err := reportIDParam(c)
	if handled || err != nil {
		return err
	}

	report, relations, handled, err := a.bindReportWrite(c)
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

	authorAddress, err := a.store.LockReportAuthor(ctx, tx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return reportNotFoundError(c)
		}
		return handlePostgresError(c, err)
	}
	if !canUpdateReport(callerAddress, callerRole, authorAddress) {
		return handleForbiddenError(c, "only the original author or a superadmin may edit this report")
	}

	// Database-backed relation validation follows authorization, so a caller
	// who cannot edit this row cannot use it to probe tag or voucher existence.
	if handled, err := a.resolveReportWriteRelations(c, tx, &report, relations, callerAddress, callerRole); handled || err != nil {
		return err
	}
	if err := a.store.UpdateReport(ctx, tx, id, report, callerAddress, reportUpdateStatus(callerRole)); err != nil {
		return handlePostgresError(c, err)
	}
	if err := a.store.ReplaceReportRelations(ctx, tx, id, report); err != nil {
		return handlePostgresError(c, err)
	}

	updated, err := a.store.GetReportForMutation(ctx, tx, id)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if updated == nil {
		return fmt.Errorf("updated report %d could not be read back", id)
	}

	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Report updated",
		Result:      map[string]any{"report": updated},
	})
}

// reportDeleteHandler is mounted behind requireSuperAdmin. Authors can correct
// their own post but cannot erase the catalog record; deletion is the one post
// mutation reserved for operators and cascades to every report relation.
func (a *API) reportDeleteHandler(c *echo.Context) error {
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

	if err := a.store.DeleteReport(ctx, tx, id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return reportNotFoundError(c)
		}
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Report deleted",
		Result:      map[string]any{"id": id},
	})
}

func reportIDParam(c *echo.Context) (id uint64, handled bool, err error) {
	id, err = strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		return 0, true, reportValidationError(c, "id must be a positive integer")
	}
	return id, false, nil
}

func reportNotFoundError(c *echo.Context) error {
	return c.JSON(http.StatusNotFound, apiresp.ErrResponse{
		Ok:          false,
		ErrCode:     apiresp.ErrCodeNotFound,
		Description: "report not found",
	})
}

func canUpdateReport(callerAddress, callerRole, authorAddress string) bool {
	return callerRole == "SUPERADMIN" ||
		(callerAddress != "" && strings.EqualFold(callerAddress, authorAddress))
}

// reportUpdateStatus decides what an authorized edit does to workflow state.
//
// A superadmin returns nil, which update-report's COALESCE reads as "keep the
// current status": a moderator's correction must not flip a rejected post back
// to published. Any other authorized caller is the author, and posts are
// auto-approved, so an author's edit publishes the result the same way their
// create did — including a legacy SUBMITTED post, which would otherwise wait in
// a queue nobody works any more.
func reportUpdateStatus(callerRole string) *string {
	if callerRole == "SUPERADMIN" {
		return nil
	}
	approved := store.ReportStatusApproved
	return &approved
}

func publicReport(report store.Report) publicReportResponse {
	return publicReportResponse{
		ID:           report.ID,
		Title:        report.Title,
		Description:  report.Description,
		ImageURL:     report.ImageURL,
		GeoLat:       report.GeoLat,
		GeoLng:       report.GeoLng,
		PeriodFrom:   report.PeriodFrom,
		PeriodTo:     report.PeriodTo,
		CreatedAt:    report.CreatedAt,
		UpdatedAt:    report.UpdatedAt,
		Tags:         report.Tags,
		Vouchers:     report.Vouchers,
		Pools:        report.Pools,
		Body:         report.Body,
		HostName:     report.HostName,
		Participants: report.Participants,
		TimeTaken:    report.TimeTaken,
		Images:       report.Images,
		Attachments:  report.Attachments,
	}
}

// bindReportWrite performs request-shape validation first, then the cross-field
// and normalisation rules validator tags cannot express. It reports handled=true
// after writing a response, matching the catalog parsing helpers beside it.
func (a *API) bindReportWrite(
	c *echo.Context,
) (store.ReportWrite, reportWriteRelations, bool, error) {
	var req apiresp.ReportWriteRequest
	if err := c.Bind(&req); err != nil {
		return store.ReportWrite{}, reportWriteRelations{}, true, handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return store.ReportWrite{}, reportWriteRelations{}, true, a.handleValidateError(c, err)
	}

	report, relations, err := normalizeReportWrite(req)
	if err != nil {
		return store.ReportWrite{}, reportWriteRelations{}, true, reportValidationError(c, err.Error())
	}
	return report, relations, false, nil
}

func normalizeReportWrite(req apiresp.ReportWriteRequest) (store.ReportWrite, reportWriteRelations, error) {
	title := strings.TrimSpace(req.Title)
	if title == "" {
		return store.ReportWrite{}, reportWriteRelations{}, fmt.Errorf("title must not be blank")
	}
	if (req.GeoLat == nil) != (req.GeoLng == nil) {
		return store.ReportWrite{}, reportWriteRelations{}, fmt.Errorf("geoLat and geoLng must be provided together")
	}
	if req.PeriodFrom != nil && req.PeriodTo != nil && req.PeriodFrom.After(*req.PeriodTo) {
		return store.ReportWrite{}, reportWriteRelations{}, fmt.Errorf("periodFrom must be before or equal to periodTo")
	}

	imageURL, err := optionalHTTPURL(req.ImageURL, "imageUrl")
	if err != nil {
		return store.ReportWrite{}, reportWriteRelations{}, err
	}

	// All five collections reject duplicates after normalization. Tags and
	// vouchers used to dedupe quietly while images failed and attachments were
	// unchecked — one wire contract with three answers, which no client
	// validator could mirror.
	tagSet := make(map[string]struct{}, len(req.Tags))
	tags := make([]string, 0, len(req.Tags))
	for _, raw := range req.Tags {
		tag := strings.TrimSpace(raw)
		if tag == "" {
			return store.ReportWrite{}, reportWriteRelations{}, fmt.Errorf("tags must not contain blank values")
		}
		if _, duplicate := tagSet[tag]; duplicate {
			return store.ReportWrite{}, reportWriteRelations{}, fmt.Errorf("tags must not contain duplicate values")
		}
		tagSet[tag] = struct{}{}
		tags = append(tags, tag)
	}
	slices.Sort(tags)

	voucherSet := make(map[string]struct{}, len(req.Vouchers))
	voucherRefs := make([]store.VoucherRef, 0, len(req.Vouchers))
	for _, requested := range req.Vouchers {
		if requested.ChainID <= 0 || !common.IsHexAddress(requested.Address) {
			return store.ReportWrite{}, reportWriteRelations{}, fmt.Errorf(
				"vouchers must contain positive chainId values and valid hex addresses")
		}
		address := common.HexToAddress(requested.Address).Hex()
		key := strconv.FormatInt(requested.ChainID, 10) + ":" + strings.ToLower(address)
		if _, duplicate := voucherSet[key]; duplicate {
			return store.ReportWrite{}, reportWriteRelations{}, fmt.Errorf(
				"vouchers must not contain duplicate chainId and address pairs")
		}
		voucherSet[key] = struct{}{}
		voucherRefs = append(voucherRefs, store.VoucherRef{
			ChainID: requested.ChainID,
			Address: address,
		})
	}
	slices.SortFunc(voucherRefs, func(left, right store.VoucherRef) int {
		if left.ChainID < right.ChainID {
			return -1
		}
		if left.ChainID > right.ChainID {
			return 1
		}
		return strings.Compare(strings.ToLower(left.Address), strings.ToLower(right.Address))
	})

	poolSet := make(map[string]struct{}, len(req.Pools))
	poolRefs := make([]store.PoolRef, 0, len(req.Pools))
	for _, requested := range req.Pools {
		if requested.ChainID <= 0 || !common.IsHexAddress(requested.Address) {
			return store.ReportWrite{}, reportWriteRelations{}, fmt.Errorf(
				"pools must contain positive chainId values and valid hex addresses")
		}
		address := common.HexToAddress(requested.Address).Hex()
		key := strconv.FormatInt(requested.ChainID, 10) + ":" + strings.ToLower(address)
		if _, duplicate := poolSet[key]; duplicate {
			return store.ReportWrite{}, reportWriteRelations{}, fmt.Errorf(
				"pools must not contain duplicate chainId and address pairs")
		}
		poolSet[key] = struct{}{}
		poolRefs = append(poolRefs, store.PoolRef{ChainID: requested.ChainID, Address: address})
	}
	slices.SortFunc(poolRefs, func(left, right store.PoolRef) int {
		if left.ChainID < right.ChainID {
			return -1
		}
		if left.ChainID > right.ChainID {
			return 1
		}
		return strings.Compare(strings.ToLower(left.Address), strings.ToLower(right.Address))
	})

	if len(voucherRefs) == 0 && len(poolRefs) == 0 {
		return store.ReportWrite{}, reportWriteRelations{}, fmt.Errorf(
			"at least one voucher or pool must be linked")
	}

	images := make([]store.ReportImage, 0, len(req.Images))
	imageSet := make(map[string]struct{}, len(req.Images))
	for index, requested := range req.Images {
		mediaURL, err := requiredHTTPURL(requested.URL, fmt.Sprintf("images[%d].url", index))
		if err != nil {
			return store.ReportWrite{}, reportWriteRelations{}, err
		}
		if _, duplicate := imageSet[mediaURL]; duplicate {
			return store.ReportWrite{}, reportWriteRelations{}, fmt.Errorf("images must not contain duplicate URLs")
		}
		imageSet[mediaURL] = struct{}{}
		images = append(images, store.ReportImage{
			URL:       mediaURL,
			Width:     requested.Width,
			Height:    requested.Height,
			MaxWidth:  requested.MaxWidth,
			MaxHeight: requested.MaxHeight,
			ByteSize:  requested.ByteSize,
			Caption:   optionalText(requested.Caption),
		})
	}

	attachments := make([]store.ReportAttachment, 0, len(req.Attachments))
	// Keyed on (kind, url): the same URL can be both a FILE download and an
	// EMBED player reference, which are two rows.
	attachmentSet := make(map[string]struct{}, len(req.Attachments))
	for index, requested := range req.Attachments {
		mediaURL, err := requiredHTTPURL(requested.URL, fmt.Sprintf("attachments[%d].url", index))
		if err != nil {
			return store.ReportWrite{}, reportWriteRelations{}, err
		}
		key := requested.Kind + "\x00" + mediaURL
		if _, duplicate := attachmentSet[key]; duplicate {
			return store.ReportWrite{}, reportWriteRelations{}, fmt.Errorf(
				"attachments must not contain duplicate kind and URL pairs")
		}
		attachmentSet[key] = struct{}{}
		attachments = append(attachments, store.ReportAttachment{
			Kind:     requested.Kind,
			URL:      mediaURL,
			Filename: optionalText(requested.Filename),
			ByteSize: requested.ByteSize,
		})
	}

	return store.ReportWrite{
		Title:        title,
		Body:         req.Body,
		Description:  req.Description,
		ImageURL:     imageURL,
		GeoLat:       req.GeoLat,
		GeoLng:       req.GeoLng,
		PeriodFrom:   req.PeriodFrom,
		PeriodTo:     req.PeriodTo,
		HostName:     optionalText(req.HostName),
		Participants: optionalText(req.Participants),
		TimeTaken:    optionalText(req.TimeTaken),
		Tags:         tags,
		Images:       images,
		Attachments:  attachments,
	}, reportWriteRelations{vouchers: voucherRefs, pools: poolRefs}, nil
}

func optionalText(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func optionalHTTPURL(value *string, field string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	normalized, err := requiredHTTPURL(*value, field)
	if err != nil {
		return nil, err
	}
	return &normalized, nil
}

func requiredHTTPURL(value, field string) (string, error) {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("%s must be an http(s) URL", field)
	}
	return value, nil
}

// resolveReportWriteRelations validates vocabulary-backed fields and turns the
// write's voucher and pool references into the rows they name. A superadmin may
// link any catalog resource; anyone else may link only resources they currently
// own, so a post cannot speak for a voucher or pool its author does not own.
// That rule lives here rather than in the client because the pickers are a
// convenience, not a gate.
func (a *API) resolveReportWriteRelations(
	c *echo.Context,
	tx pgx.Tx,
	report *store.ReportWrite,
	relations reportWriteRelations,
	callerAddress, callerRole string,
) (bool, error) {
	tags, handled, err := a.ensureTokenTags(c, tx, report.Tags)
	if handled || err != nil {
		return handled, err
	}
	report.Tags = tags
	ids, err := a.store.ResolveVoucherRefs(c.Request().Context(), tx, relations.vouchers)
	if err != nil {
		return true, handlePostgresError(c, err)
	}
	if len(ids) != len(relations.vouchers) {
		return true, respondCatalogSearchError(c, errUnknownVoucherRef)
	}
	if callerRole != "SUPERADMIN" {
		owned, err := a.store.CountVouchersOwnedBy(c.Request().Context(), tx, ids, callerAddress)
		if err != nil {
			return true, handlePostgresError(c, err)
		}
		if owned != len(ids) {
			return true, handleForbiddenError(c, "only vouchers you own can be linked to a post")
		}
	}
	report.VoucherIDs = ids

	poolIDs, err := a.store.ResolvePoolRefs(c.Request().Context(), tx, relations.pools)
	if err != nil {
		return true, handlePostgresError(c, err)
	}
	if len(poolIDs) != len(relations.pools) {
		return true, respondCatalogSearchError(c, errUnknownPoolRef)
	}
	if callerRole != "SUPERADMIN" {
		owned, err := a.store.CountPoolsOwnedBy(c.Request().Context(), tx, poolIDs, callerAddress)
		if err != nil {
			return true, handlePostgresError(c, err)
		}
		if owned != len(poolIDs) {
			return true, handleForbiddenError(c, "only pools you own can be linked to a post")
		}
	}
	report.PoolIDs = poolIDs
	return false, nil
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
