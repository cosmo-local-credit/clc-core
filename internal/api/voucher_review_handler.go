package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

const (
	reviewWindow = 7 * 24 * time.Hour
)

func reviewResponse(c *echo.Context, result any) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, map[string]any{"ok": true, "result": result})
}

func reviewFailure(c *echo.Context, status int, message string) error {
	return c.JSON(status, apiresp.ErrResponse{Ok: false, Description: message})
}

func reviewStoreError(c *echo.Context, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return reviewFailure(c, http.StatusNotFound, "review or voucher not found, or action is no longer available")
	}
	return handlePostgresError(c, err)
}

func reviewCursor(raw string) (uint64, error) {
	if raw == "" {
		return 0, nil
	}
	return strconv.ParseUint(raw, 10, 63)
}

func reviewDeadline(latest *time.Time, now time.Time) *time.Time {
	if latest == nil || latest.After(now) {
		return nil
	}
	until := latest.Add(reviewWindow)
	return &until
}

func (a *API) voucherReviewsListHandler(c *echo.Context) error {
	address, handled, err := offeringVoucherParam(c)
	if handled || err != nil {
		return err
	}
	stars := 0
	if raw := c.QueryParam("stars"); raw != "" {
		stars, err = strconv.Atoi(raw)
		if err != nil || stars < 1 || stars > 5 {
			return reviewFailure(c, http.StatusBadRequest, "stars must be an integer from 1 to 5")
		}
	}
	before, err := reviewCursor(c.QueryParam("before"))
	if err != nil {
		return reviewFailure(c, http.StatusBadRequest, "invalid review cursor")
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	voucher, err := store.GetReviewVoucher(ctx, tx, address, false)
	if err != nil {
		return reviewStoreError(c, err)
	}
	page, err := store.ListVoucherReviews(ctx, tx, voucher.ID, stars, before)
	if err != nil {
		return reviewStoreError(c, err)
	}
	return reviewResponse(c, page)
}

func (a *API) reviewAccess(c *echo.Context, tx pgx.Tx, voucher store.ReviewVoucher, caller string) (store.ReviewAccess, error) {
	ctx := c.Request().Context()
	access, err := store.GetVoucherReviewAccess(ctx, tx, voucher, caller)
	if err != nil || access.IsOwner || access.Hidden || voucher.OwnerAddress == nil {
		return access, err
	}
	// Reviews follow the existing single-chain deployment configuration.
	// The federation transfer source must index the configured chain.
	if a.federationStore == nil || a.chainID <= 0 || voucher.ChainID != a.chainID {
		return access, echo.NewHTTPError(http.StatusServiceUnavailable, "redemption verification is unavailable for this chain")
	}
	latest, err := store.LatestVoucherRedemption(ctx, a.federationStore, voucher.Address, caller, *voucher.OwnerAddress)
	if err != nil {
		return access, err
	}
	access.EditableUntil = reviewDeadline(latest, time.Now())
	return access, nil
}

func (a *API) voucherReviewAccessHandler(c *echo.Context) error {
	address, handled, err := offeringVoucherParam(c)
	if handled || err != nil {
		return err
	}
	_, caller, _ := getUserClaims(c)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	voucher, err := store.GetReviewVoucher(ctx, tx, address, false)
	if err != nil {
		return reviewStoreError(c, err)
	}
	access, err := a.reviewAccess(c, tx, voucher, caller)
	if err != nil {
		return err
	}
	return reviewResponse(c, access)
}

func (a *API) voucherReviewSaveHandler(c *echo.Context) error {
	address, handled, err := offeringVoucherParam(c)
	if handled || err != nil {
		return err
	}
	var req struct {
		Stars   int    `json:"stars"`
		Comment string `json:"comment"`
	}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	req.Comment = strings.TrimSpace(req.Comment)
	if req.Stars < 1 || req.Stars > 5 || utf8.RuneCountInString(req.Comment) > 2000 {
		return reviewFailure(c, http.StatusBadRequest, "choose 1–5 stars and a comment of at most 2000 characters")
	}
	_, caller, _ := getUserClaims(c)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	voucher, err := store.GetReviewVoucher(ctx, tx, address, true)
	if err != nil {
		return reviewStoreError(c, err)
	}
	access, err := a.reviewAccess(c, tx, voucher, caller)
	if err != nil {
		return err
	}
	if access.IsOwner || access.Hidden || access.EditableUntil == nil || !time.Now().Before(*access.EditableUntil) {
		return handleForbiddenError(c, "only the reviewer may write within seven days after redemption; owners cannot self-review")
	}
	if err := store.SaveVoucherReview(ctx, tx, voucher.ID, caller, req.Stars, req.Comment, *access.EditableUntil); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return handleForbiddenError(c, "the review editing window has closed")
		}
		return reviewStoreError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return reviewResponse(c, map[string]bool{"saved": true})
}

func (a *API) voucherReviewReplyHandler(c *echo.Context) error {
	return a.voucherReviewOwnerWrite(c, false)
}
func (a *API) voucherReviewFlagHandler(c *echo.Context) error {
	return a.voucherReviewOwnerWrite(c, true)
}

func (a *API) voucherReviewOwnerWrite(c *echo.Context, flag bool) error {
	address, handled, err := offeringVoucherParam(c)
	if handled || err != nil {
		return err
	}
	id, err := reviewCursor(c.Param("id"))
	if err != nil || id == 0 {
		return reviewFailure(c, http.StatusBadRequest, "invalid review ID")
	}
	var req struct {
		Reply  string `json:"reply"`
		Reason string `json:"reason"`
	}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	text, limit := strings.TrimSpace(req.Reply), 2000
	if flag {
		text, limit = strings.TrimSpace(req.Reason), 500
	}
	if text == "" || utf8.RuneCountInString(text) > limit {
		return reviewFailure(c, http.StatusBadRequest, "text is empty or too long")
	}
	_, caller, _ := getUserClaims(c)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	voucher, err := store.GetReviewVoucher(ctx, tx, address, true)
	if err != nil {
		return reviewStoreError(c, err)
	}
	if voucher.OwnerAddress == nil || !strings.EqualFold(caller, *voucher.OwnerAddress) {
		return handleForbiddenError(c, "only the current voucher owner may reply or flag a review")
	}
	if flag {
		err = store.FlagVoucherReview(ctx, tx, voucher.ID, id, caller, text)
	} else {
		err = store.SaveVoucherReviewReply(ctx, tx, voucher.ID, id, caller, text)
	}
	if err != nil {
		return reviewStoreError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return reviewResponse(c, map[string]bool{"saved": true})
}

// A flag is a durable notification to the admin moderation inbox. Delivery via
// web push/email is a separate integration; this checkout has no such sender.
func (a *API) reviewNotificationsHandler(c *echo.Context) error {
	before, err := reviewCursor(c.QueryParam("before"))
	if err != nil {
		return reviewFailure(c, http.StatusBadRequest, "invalid notification cursor")
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	flags, err := store.ListReviewFlags(ctx, tx, before)
	if err != nil {
		return err
	}
	var next *uint64
	if len(flags) > 20 {
		flags = flags[:20]
		id := flags[19].ID
		next = &id
	}
	return reviewResponse(c, map[string]any{"notifications": flags, "next": next})
}

func (a *API) reviewModerateHandler(c *echo.Context) error {
	id, err := reviewCursor(c.Param("id"))
	if err != nil || id == 0 {
		return reviewFailure(c, http.StatusBadRequest, "invalid notification ID")
	}
	var req struct {
		Action string `json:"action"`
	}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if req.Action != "hide" && req.Action != "dismiss" {
		return reviewFailure(c, http.StatusBadRequest, "action must be hide or dismiss")
	}
	_, caller, _ := getUserClaims(c)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := store.ResolveReviewFlag(ctx, tx, id, req.Action == "hide", caller); err != nil {
		return reviewStoreError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return reviewResponse(c, map[string]bool{"saved": true})
}
