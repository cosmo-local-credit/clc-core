package api

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

type (
	retirementWrite struct {
		Reason              string `json:"reason"`
		AcknowledgeBalances bool   `json:"acknowledgeBalances"`
	}
)

func retirementKind(c *echo.Context) string {
	if strings.Contains(c.Path(), "/vouchers/") {
		return "voucher"
	}
	return "pool"
}

func validRetirementCaller(c *echo.Context) bool {
	_, address, _ := getUserClaims(c)
	return common.IsHexAddress(address) && common.HexToAddress(address) != (common.Address{})
}
func retirementOK(c *echo.Context, result map[string]any) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, apiresp.OKResponse{Ok: true, Description: "Retirement", Result: result})
}
func retirementReason(c *echo.Context, required bool) (retirementWrite, error) {
	var body retirementWrite
	if c.Request().ContentLength != 0 {
		if err := c.Bind(&body); err != nil {
			return body, echo.NewHTTPError(http.StatusBadRequest, "Invalid retirement request")
		}
	}
	body.Reason = strings.TrimSpace(body.Reason)
	if (required && body.Reason == "") || utf8.RuneCountInString(body.Reason) > 2000 {
		return body, echo.NewHTTPError(http.StatusBadRequest, "Give a reason between 1 and 2000 characters")
	}
	return body, nil
}
func retirementPreviewError(c *echo.Context, err error) error {
	if errors.Is(err, errRetirementOwnerChanged) {
		return handleConflictError(c, err.Error())
	}
	return c.JSON(http.StatusServiceUnavailable, apiresp.ErrResponse{Ok: false, ErrCode: apiresp.ErrCodeInternalServerError, Description: "Could not check balances. Please retry."})
}

func (a *API) retirementPreviewHandler(c *echo.Context) error {
	if !validRetirementCaller(c) {
		return handleForbiddenError(c, "this endpoint requires an account")
	}
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
	resource, err := store.GetRetirementResource(ctx, tx, retirementKind(c), address, a.chainID, false)
	if err != nil {
		return handlePostgresError(c, err)
	}
	_, caller, role := getUserClaims(c)
	if !a.callerMayManageRecord(ctx, tx, caller, role, &resource.OwnerAddress) {
		return handleForbiddenError(c, "only the current owner or an admin may check retirement balances")
	}
	preview, err := a.retirementPreview(ctx, tx, resource)
	if err != nil {
		return retirementPreviewError(c, err)
	}
	return retirementOK(c, map[string]any{"preview": preview})
}

func (a *API) createRetirementRequestHandler(c *echo.Context) error {
	if !validRetirementCaller(c) {
		return handleForbiddenError(c, "this endpoint requires an account")
	}
	address, handled, err := adminUserAddress(c)
	if handled || err != nil {
		return err
	}
	body, err := retirementReason(c, true)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	resource, err := store.GetRetirementResource(ctx, tx, retirementKind(c), address, a.chainID, true)
	if err != nil {
		return handlePostgresError(c, err)
	}
	// A Safe signer requests on the Safe's behalf: the request records the
	// Safe as owner and the signer as requester, and an administrator still
	// decides it.
	_, caller, _ := getUserClaims(c)
	if !strings.EqualFold(a.actingOwner(ctx, tx, caller, &resource.OwnerAddress), resource.OwnerAddress) {
		return handleForbiddenError(c, "only the current owner may request retirement")
	}
	if !resource.Active {
		return handleConflictError(c, "resource is already retired")
	}
	pending, err := store.ListRetirementRequests(ctx, tx, store.RetirementFilter{Resource: &resource, Status: "pending"})
	if err != nil {
		return err
	}
	if len(pending) > 0 && strings.EqualFold(pending[0].OwnerAddress, resource.OwnerAddress) {
		return retirementOK(c, map[string]any{"request": pending[0]})
	}
	preview, err := a.retirementPreview(ctx, tx, resource)
	if err != nil {
		return retirementPreviewError(c, err)
	}
	for _, request := range pending {
		if _, err = store.DecideRetirementRequest(ctx, tx, resource, request.ID, "invalidated", caller, "Ownership changed", nil); err != nil {
			return err
		}
	}
	request, err := store.CreateRetirementRequest(ctx, tx, resource, caller, body.Reason, preview)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	return retirementOK(c, map[string]any{"request": request})
}

func (a *API) retirementResourceHistoryHandler(c *echo.Context) error {
	if !validRetirementCaller(c) {
		return handleForbiddenError(c, "this endpoint requires an account")
	}
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
	resource, err := store.GetRetirementResource(ctx, tx, retirementKind(c), address, a.chainID, false)
	if err != nil {
		return handlePostgresError(c, err)
	}
	_, caller, role := getUserClaims(c)
	if !a.callerMayManageRecord(ctx, tx, caller, role, &resource.OwnerAddress) {
		return handleForbiddenError(c, "only the current owner or an admin may read retirement history")
	}
	requests, err := store.ListRetirementRequests(ctx, tx, store.RetirementFilter{Resource: &resource})
	if err != nil {
		return err
	}
	events, err := store.ListRetirementEvents(ctx, tx, resource)
	if err != nil {
		return err
	}
	return retirementOK(c, map[string]any{"requests": requests, "events": events})
}

func (a *API) ownRetirementRequestsHandler(c *echo.Context) error {
	if !validRetirementCaller(c) {
		return handleForbiddenError(c, "this endpoint requires an account")
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, caller, _ := getUserClaims(c)
	requests, err := store.ListRetirementRequests(ctx, tx, store.RetirementFilter{OwnerAddress: caller})
	if err != nil {
		return err
	}
	return retirementOK(c, map[string]any{"requests": requests})
}

func retirementRequestID(c *echo.Context) (uint64, error) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		return 0, echo.NewHTTPError(http.StatusBadRequest, "Invalid retirement request ID")
	}
	return id, nil
}

func (a *API) adminRetirementRequestsHandler(c *echo.Context) error {
	status := c.QueryParam("status")
	if status == "" {
		status = "pending"
	}
	if !slices.Contains([]string{"pending", "all", "approved", "rejected", "cancelled", "invalidated"}, status) {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid retirement status")
	}
	kind := c.QueryParam("resourceType")
	if kind != "" && kind != "voucher" && kind != "pool" {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid resource type")
	}
	limit := uint64(25)
	before := uint64(0)
	var err error
	if c.QueryParam("perPage") != "" {
		limit, err = strconv.ParseUint(c.QueryParam("perPage"), 10, 64)
		if err != nil || limit < 1 || limit > 100 {
			return echo.NewHTTPError(http.StatusBadRequest, "perPage must be between 1 and 100")
		}
	}
	if c.QueryParam("cursor") != "" {
		before, err = strconv.ParseUint(c.QueryParam("cursor"), 10, 64)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "Invalid cursor")
		}
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	requests, err := store.ListRetirementRequests(ctx, tx, store.RetirementFilter{Status: status, ResourceType: kind, Before: before, Limit: limit + 1})
	if err != nil {
		return err
	}
	var next *uint64
	if uint64(len(requests)) > limit {
		requests = requests[:limit]
		id := requests[len(requests)-1].ID
		next = &id
	}
	var pendingCount uint64
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM retirement_requests WHERE status='pending'").Scan(&pendingCount); err != nil {
		return err
	}
	return retirementOK(c, map[string]any{"requests": requests, "pendingCount": pendingCount, "nextCursor": next})
}

func (a *API) adminRetirementRequestHandler(c *echo.Context) error {
	id, err := retirementRequestID(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	request, err := store.GetRetirementRequest(ctx, tx, id, false)
	if err != nil {
		return handlePostgresError(c, err)
	}
	resource, err := store.GetRetirementResource(ctx, tx, request.ResourceType, request.ResourceAddress, request.ChainID, false)
	if err != nil {
		return err
	}
	events, err := store.ListRetirementEvents(ctx, tx, resource)
	if err != nil {
		return err
	}
	return retirementOK(c, map[string]any{"request": request, "events": events})
}

// Lock the resource before the request everywhere. That serializes duplicate
// submissions and decisions without reversing restoration's quota/resource order.
func (a *API) decideRetirementRequestHandler(c *echo.Context) error {
	if !validRetirementCaller(c) {
		return handleForbiddenError(c, "this endpoint requires an account")
	}
	id, err := retirementRequestID(c)
	if err != nil {
		return err
	}
	action := "approved"
	if strings.HasSuffix(c.Path(), "/reject") {
		action = "rejected"
	}
	if strings.HasSuffix(c.Path(), "/cancel") {
		action = "cancelled"
	}
	body, err := retirementReason(c, action == "rejected")
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	request, err := store.GetRetirementRequest(ctx, tx, id, false)
	if err != nil {
		return handlePostgresError(c, err)
	}
	resource, err := store.GetRetirementResource(ctx, tx, request.ResourceType, request.ResourceAddress, request.ChainID, true)
	if err != nil {
		return err
	}
	_, caller, role := getUserClaims(c)
	if action == "cancelled" {
		if !strings.EqualFold(caller, request.RequesterAddress) ||
			!strings.EqualFold(a.actingOwner(ctx, tx, caller, &resource.OwnerAddress), resource.OwnerAddress) {
			return handleForbiddenError(c, "only the current owner who made this request may cancel it")
		}
	} else if !store.IsAdministrativeRole(role) {
		return handleForbiddenError(c, "only an admin may decide retirement requests")
	}
	request, err = store.GetRetirementRequest(ctx, tx, id, true)
	if err != nil {
		return err
	}
	if request.Status != "pending" {
		return handleConflictError(c, "retirement request is no longer pending")
	}
	var preview *store.RetirementPreview
	if !strings.EqualFold(request.OwnerAddress, resource.OwnerAddress) {
		action = "invalidated"
		body.Reason = "Ownership changed"
	}
	if action == "approved" {
		if !resource.Active {
			return handleConflictError(c, "resource is already retired")
		}
		checked, e := a.retirementPreview(ctx, tx, resource)
		switch {
		case errors.Is(e, errRetirementOwnerChanged):
			action = "invalidated"
			body.Reason = "Ownership changed on chain"
		case e != nil:
			return retirementPreviewError(c, e)
		default:
			preview = &checked
			if checked.HasPositiveBalances && !body.AcknowledgeBalances {
				return handleConflictError(c, "Confirm the remaining balances before approving retirement")
			}
			if err = store.SetRetirementResourceActive(ctx, tx, resource, false); err != nil {
				return err
			}
		}
	}
	request, err = store.DecideRetirementRequest(ctx, tx, resource, id, action, caller, body.Reason, preview)
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if action == "invalidated" {
		return handleConflictError(c, "Ownership changed. The request was invalidated; the current owner must submit a new request.")
	}
	return retirementOK(c, map[string]any{"request": request})
}

func (a *API) adminRestorePoolHandler(c *echo.Context) error {
	address, handled, err := adminUserAddress(c)
	if handled || err != nil {
		return err
	}
	body, err := retirementReason(c, true)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// RestorePoolWithinLimits takes creator quota locks before the pool lock.
	changed, err := a.store.RestorePoolWithinLimitsChanged(ctx, tx, address)
	if err != nil {
		return handleResourceLimitError(c, err)
	}
	resource, err := store.GetRetirementResource(ctx, tx, "pool", address, a.chainID, false)
	if err != nil {
		return handlePostgresError(c, err)
	}
	_, caller, _ := getUserClaims(c)
	if changed {
		if err = store.InsertRetirementEvent(ctx, tx, resource, nil, "restored", caller, body.Reason); err != nil {
			return err
		}
	}
	pool, err := a.store.GetPoolByAddress(ctx, tx, address)
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	return retirementOK(c, map[string]any{"pool": pool})
}
