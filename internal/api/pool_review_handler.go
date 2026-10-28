package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

func poolReviewAddress(c *echo.Context) (string, error) {
	address := c.Param("address")
	if !common.IsHexAddress(address) {
		return "", reviewFailure(c, http.StatusBadRequest, "address must be a valid hex address")
	}
	return address, nil
}

func (a *API) poolReviewsListHandler(c *echo.Context) error {
	address, err := poolReviewAddress(c)
	if err != nil {
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
	pool, err := store.GetReviewPool(ctx, tx, address, false)
	if err != nil {
		return reviewStoreError(c, err)
	}
	page, err := store.ListPoolReviews(ctx, tx, pool.ID, stars, before)
	if err != nil {
		return reviewStoreError(c, err)
	}
	return reviewResponse(c, page)
}

func (a *API) poolReviewAccess(c *echo.Context, tx pgx.Tx, pool store.ReviewPool, caller string) (store.ReviewAccess, error) {
	ctx := c.Request().Context()
	isOwner := pool.OwnerAddress != nil && strings.EqualFold(a.actingOwner(ctx, tx, caller, pool.OwnerAddress), *pool.OwnerAddress)
	access, err := store.GetPoolReviewAccess(ctx, tx, pool, caller, isOwner)
	if err != nil || access.IsOwner || access.Hidden || pool.OwnerAddress == nil {
		return access, err
	}
	if a.federationStore == nil || a.chainID <= 0 || pool.ChainID != a.chainID {
		return access, echo.NewHTTPError(http.StatusServiceUnavailable, "swap verification is unavailable for this chain")
	}
	latest, err := a.federationStore.LatestPoolReviewSwap(ctx, pool.Address, caller)
	if err != nil {
		return access, err
	}
	access.EditableUntil = reviewDeadline(latest, time.Now())
	return access, nil
}

func (a *API) poolReviewAccessHandler(c *echo.Context) error {
	address, err := poolReviewAddress(c)
	if err != nil {
		return err
	}
	_, caller, _ := getUserClaims(c)
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	pool, err := store.GetReviewPool(ctx, tx, address, false)
	if err != nil {
		return reviewStoreError(c, err)
	}
	access, err := a.poolReviewAccess(c, tx, pool, caller)
	if err != nil {
		return err
	}
	return reviewResponse(c, access)
}

func (a *API) poolReviewSaveHandler(c *echo.Context) error {
	address, err := poolReviewAddress(c)
	if err != nil {
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
	pool, err := store.GetReviewPool(ctx, tx, address, true)
	if err != nil {
		return reviewStoreError(c, err)
	}
	access, err := a.poolReviewAccess(c, tx, pool, caller)
	if err != nil {
		return err
	}
	if access.IsOwner || access.Hidden || access.EditableUntil == nil || !time.Now().Before(*access.EditableUntil) {
		return handleForbiddenError(c, "only the reviewer may write within seven days after a swap; owners cannot self-review")
	}
	if err := store.SavePoolReview(ctx, tx, pool.ID, caller, req.Stars, req.Comment, *access.EditableUntil); err != nil {
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

func (a *API) poolReviewReplyHandler(c *echo.Context) error {
	return a.poolReviewOwnerWrite(c, false)
}

func (a *API) poolReviewFlagHandler(c *echo.Context) error {
	return a.poolReviewOwnerWrite(c, true)
}

func (a *API) poolReviewOwnerWrite(c *echo.Context, flag bool) error {
	address, err := poolReviewAddress(c)
	if err != nil {
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
	pool, err := store.GetReviewPool(ctx, tx, address, true)
	if err != nil {
		return reviewStoreError(c, err)
	}
	if pool.OwnerAddress == nil || !strings.EqualFold(a.actingOwner(ctx, tx, caller, pool.OwnerAddress), *pool.OwnerAddress) {
		return handleForbiddenError(c, "only the current pool owner may reply or flag a review")
	}
	if flag {
		err = store.FlagPoolReview(ctx, tx, pool.ID, id, caller, text)
	} else {
		err = store.SavePoolReviewReply(ctx, tx, pool.ID, id, caller, text)
	}
	if err != nil {
		return reviewStoreError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return reviewResponse(c, map[string]bool{"saved": true})
}
