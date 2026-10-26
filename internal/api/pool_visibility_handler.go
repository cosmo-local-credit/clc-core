package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

func (a *API) adminPoolCatalogHandler(c *echo.Context) error {
	return a.managedPoolsList(c, "")
}

func (a *API) ownerPoolsListHandler(c *echo.Context) error {
	_, owner, _ := getUserClaims(c)
	if !common.IsHexAddress(owner) || common.HexToAddress(owner) == (common.Address{}) {
		return handleForbiddenError(c, "a wallet user session is required")
	}
	return a.managedPoolsList(c, owner)
}

func (a *API) managedPoolsList(c *echo.Context, owner string) error {
	status := c.QueryParam("status")
	if status != "" && status != "all" && status != "active" && status != "retired" {
		return respondCatalogSearchError(c, errors.New("status must be all, active or retired"))
	}
	search, err := parseCatalogRelationshipSearch(c)
	if err != nil {
		return respondCatalogSearchError(c, err)
	}
	if owner == "" {
		owner, err = addressFilterParam(c, "owner")
		if err != nil {
			return respondCatalogSearchError(c, err)
		}
	}
	req := apiresp.CursorPaginationRequest{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	pagination := normalizePagination(req)
	c.Response().Header().Set("Cache-Control", "no-store")
	return adminPagedList(c, a, pagination, "pools", "Managed pools",
		func(p store.Pool) uint64 { return p.ID },
		func(ctx context.Context, tx pgx.Tx, page store.CatalogPage, cursor, limit uint64) ([]store.Pool, error) {
			if err := a.store.SetCatalogStatementTimeout(ctx, tx); err != nil {
				return nil, err
			}
			return a.store.ListManagedPools(ctx, tx, search.filter, status, owner, page, cursor, limit)
		})
}

func (a *API) adminPoolByAddressHandler(c *echo.Context) error {
	return a.managedPoolByAddress(c, false)
}

func (a *API) ownerPoolByAddressHandler(c *echo.Context) error {
	return a.managedPoolByAddress(c, true)
}

func (a *API) managedPoolByAddress(c *echo.Context, ownerOnly bool) error {
	address, handled, err := offeringVoucherParam(c)
	if handled || err != nil {
		return err
	}
	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	pool, err := a.store.GetPoolByAddress(ctx, tx, address)
	if err != nil {
		return handlePostgresError(c, err)
	}
	if ownerOnly {
		_, owner, _ := getUserClaims(c)
		if pool.OwnerAddress == nil || !strings.EqualFold(*pool.OwnerAddress, owner) {
			return poolProfileNotFoundError(c)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return handlePostgresError(c, err)
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, apiresp.OKResponse{Ok: true, Description: "Pool", Result: map[string]any{"pool": pool}})
}

func (a *API) ownerPoolVouchersListHandler(c *echo.Context) error {
	return a.poolVouchersList(c, true, true)
}
