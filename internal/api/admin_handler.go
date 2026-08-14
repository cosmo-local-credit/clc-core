package api

import (
	"context"
	"net/http"

	"github.com/cosmo-local-credit/clc-core/internal/store"
	apiresp "github.com/cosmo-local-credit/clc-core/pkg/api"
	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
)

// adminListUsersHandler serves the account directory: one cursor page of
// accounts, newest first, narrowed by an optional approval `status` and an
// optional `q` over name, email, phone and address.
//
// It used to answer with the whole table and no search, which left the console
// downloading every account to filter in the browser. Both jobs are the
// database's, and the whole-table answer had no upper bound: the response grew
// with the directory, and a client that paged it locally would eventually page a
// list it had only partly received. This is therefore a deliberately breaking
// change to the response — `users` is now at most `perPage` rows and `first` /
// `last` carry the cursor, exactly as every other list endpoint answers.
//
// Cursor and page-size handling come from the shared paginated list, which also
// bounds the read with the catalog statement timeout. `q` reuses the catalog's
// term rules — same minimum, maximum and error text as the public search — and
// `status` is parsed against the enum before any transaction opens, so an
// unknown value is a 400 here rather than a failed cast inside PostgreSQL.
func (a *API) adminListUsersHandler(c *echo.Context) error {
	req := apiresp.CursorPaginationRequest{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	pagination := normalizePagination(req)

	filter, err := store.NewUserFilter(c.QueryParam("q"), c.QueryParam("status"))
	if err != nil {
		return respondCatalogSearchError(c, err)
	}

	return cursorPaginatedCatalogList(c, a, paginatedCatalogListCallbacks[store.User]{
		firstPage: func(ctx context.Context, tx pgx.Tx, limit uint64) ([]store.User, error) {
			return a.store.ListUsersFiltered(ctx, tx, filter, store.CatalogFirstPage, 0, limit)
		},
		nextPage: func(ctx context.Context, tx pgx.Tx, cursor, limit uint64) ([]store.User, error) {
			return a.store.ListUsersFiltered(ctx, tx, filter, store.CatalogNextPage, cursor, limit)
		},
		prevPage: func(ctx context.Context, tx pgx.Tx, cursor, limit uint64) ([]store.User, error) {
			return a.store.ListUsersFiltered(ctx, tx, filter, store.CatalogPrevPage, cursor, limit)
		},
	}, pagination, "users", "Users list", func(u store.User) uint64 { return u.ID }, catalogListHooks{})
}

func (a *API) adminApproveUserHandler(c *echo.Context) error {
	return a.adminSetUserStatus(c, "APPROVED")
}

func (a *API) adminRejectUserHandler(c *echo.Context) error {
	return a.adminSetUserStatus(c, "REJECTED")
}

func (a *API) adminSetUserStatus(c *echo.Context, status string) error {
	var param apiresp.EthAddressParam
	if err := c.Bind(&param); err != nil {
		return handleBindError(c, err)
	}
	if !common.IsHexAddress(param.Address) {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "invalid Ethereum address",
		})
	}

	address := common.HexToAddress(param.Address).Hex()
	ctx := c.Request().Context()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	user, err := a.store.UpdateUserStatus(ctx, tx, address, status)
	if err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "User status updated to " + status,
		Result: map[string]any{
			"user": user,
		},
	})
}

func (a *API) adminUpdateUserLimitsHandler(c *echo.Context) error {
	var param apiresp.EthAddressParam
	if err := c.Bind(&param); err != nil {
		return handleBindError(c, err)
	}
	if !common.IsHexAddress(param.Address) {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "invalid Ethereum address",
		})
	}

	var req apiresp.UpdateUserLimitsRequest
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	address := common.HexToAddress(param.Address).Hex()
	ctx := c.Request().Context()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	user, err := a.store.UpdateUserLimits(ctx, tx, address, req.MaxPools, req.MaxTokens)
	if err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "User limits updated",
		Result: map[string]any{
			"user": user,
		},
	})
}

func (a *API) adminListTokensHandler(c *echo.Context) error {
	ctx := c.Request().Context()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tokens, err := a.store.ListAllTokens(ctx, tx)
	if err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "All deployed tokens",
		Result: map[string]any{
			"tokens": tokens,
		},
	})
}

func (a *API) adminListPoolsHandler(c *echo.Context) error {
	ctx := c.Request().Context()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	pools, err := a.store.ListAllPools(ctx, tx)
	if err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "All deployed pools",
		Result: map[string]any{
			"pools": pools,
		},
	})
}

func (a *API) adminListDeploymentsHandler(c *echo.Context) error {
	ctx := c.Request().Context()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	deployments, err := a.store.ListAllDeployments(ctx, tx)
	if err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "All deployment history",
		Result: map[string]any{
			"deployments": deployments,
		},
	})
}
