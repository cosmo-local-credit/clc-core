package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

func (a *API) recipientRead(c *echo.Context, read func(pgx.Tx) (any, error)) error {
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	result, err := read(tx)
	if err != nil {
		if errors.Is(err, store.ErrAmbiguousRecipient) {
			return handleConflictError(c, err.Error())
		}
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, apiresp.OKResponse{Ok: true, Result: map[string]any{"recipient": result}})
}

func (a *API) recipientSearchHandler(c *echo.Context) error {
	query := strings.TrimSpace(c.QueryParam("q"))
	kind := c.QueryParam("kind")
	if query == "" || len([]rune(query)) > 120 || (kind != "" && kind != "user" && kind != "pool") {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{Ok: false, ErrCode: apiresp.ErrCodeValidationFailed, Description: "provide a search term and an optional user or pool kind"})
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	recipients, err := a.store.SearchRecipients(ctx, tx, query, a.chainID, kind)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, apiresp.OKResponse{Ok: true, Result: map[string]any{"recipients": recipients}})
}

func (a *API) recipientResolveHandler(c *echo.Context) error {
	return a.recipientRead(c, func(tx pgx.Tx) (any, error) {
		return a.store.ResolveRecipient(c.Request().Context(), tx, c.Param("name"), a.chainID, false)
	})
}

func (a *API) recipientReverseHandler(c *echo.Context) error {
	address := c.Param("address")
	if !common.IsHexAddress(address) {
		return handleBindError(c, &echo.HTTPError{Message: "invalid address"})
	}
	return a.recipientRead(c, func(tx pgx.Tx) (any, error) {
		return a.store.ResolveRecipient(c.Request().Context(), tx, address, a.chainID, true)
	})
}

func (a *API) usernameSuggestHandler(c *echo.Context) error {
	hint := strings.TrimSpace(c.QueryParam("hint"))
	if len([]rune(hint)) > 120 {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{Ok: false, ErrCode: apiresp.ErrCodeValidationFailed, Description: "username hint is too long"})
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	username, err := a.store.SuggestUsername(ctx, tx, hint)
	if errors.Is(err, store.ErrNoUsernameAvailable) {
		return handleConflictError(c, err.Error())
	}
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, apiresp.OKResponse{Ok: true, Result: map[string]any{"username": username}})
}
