package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/labstack/echo/v5"
)

type paginatedFederationListCallbacks[T any] struct {
	firstPage func(context.Context, string, uint64) ([]T, error)
	nextPage  func(context.Context, string, uint64, uint64) ([]T, error)
	prevPage  func(context.Context, string, uint64, uint64) ([]T, error)
}

func cursorPaginatedFederationList[T any](
	a *API,
	c *echo.Context,
	p paginatedFederationListCallbacks[T],
	address string,
	resultKey, description string,
	getID func(T) uint64,
) error {
	req := apiresp.CursorPaginationRequest{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	pagination := normalizePagination(req)
	ctx := c.Request().Context()

	var (
		items    []T
		queryErr error
	)

	switch {
	case pagination.FirstPage:
		items, queryErr = p.firstPage(ctx, address, pagination.PerPage)
	case pagination.Next:
		items, queryErr = p.nextPage(ctx, address, pagination.Cursor, pagination.PerPage)
	default:
		items, queryErr = p.prevPage(ctx, address, pagination.Cursor, pagination.PerPage)
	}
	if queryErr != nil {
		return handlePostgresError(c, queryErr)
	}

	var first, last uint64
	if len(items) > 0 {
		first = getID(items[0])
		last = getID(items[len(items)-1])
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: description,
		Result: map[string]any{
			resultKey: items,
			"first":   first,
			"last":    last,
		},
	})
}

func (a *API) accountLatestTokensHandler(c *echo.Context) error {
	req := apiresp.AccountAddressParam{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	ctx := c.Request().Context()
	tokens, err := a.federationStore.ListUserTokenHoldings(ctx, req.Address, 5)
	if err != nil {
		return handlePostgresError(c, err)
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: fmt.Sprintf("Latest token balances for %s", req.Address),
		Result: map[string]any{
			"tokens": tokens,
		},
	})
}

func (a *API) accountTokensHandler(c *echo.Context) error {
	req := apiresp.OTXByAccountRequest{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	return cursorPaginatedFederationList(a, c, paginatedFederationListCallbacks[store.FederationTokenBalance]{
		firstPage: a.federationStore.ListUserTokenHoldings,
		nextPage:  a.federationStore.ListUserTokenHoldingsNext,
		prevPage:  a.federationStore.ListUserTokenHoldingsPrevious,
	}, req.Address, "tokens", fmt.Sprintf("Token balances for %s", req.Address),
		func(t store.FederationTokenBalance) uint64 { return t.ID })
}

func (a *API) accountTransfersHandler(c *echo.Context) error {
	req := apiresp.OTXByAccountRequest{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	return cursorPaginatedFederationList(a, c, paginatedFederationListCallbacks[store.FederationTransfer]{
		firstPage: a.federationStore.ListUserTransferHistory,
		nextPage:  a.federationStore.ListUserTransferHistoryNext,
		prevPage:  a.federationStore.ListUserTransferHistoryPrevious,
	}, req.Address, "transfers", fmt.Sprintf("Transfer history for %s", req.Address),
		func(t store.FederationTransfer) uint64 { return t.ID })
}
