package api

import (
	"context"
	"errors"
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

// adminUserAddress reads the checksummed account address from the path.
//
// Read with c.Param rather than bound into an EthAddressParam, because binding
// consumes the request body: echo's binder reads it while looking for fields to
// fill, so a handler that binds the path parameter and then binds a body gets an
// empty second read. That is what made PUT /admin/users/:address/limits answer
// 400 for every well-formed request, including one carrying the documented
// payload. Handlers here take the address this way and bind at most once.
//
// It reports handled = true when it has already written the error response,
// matching bindCatalogAddressPagination's contract.
func adminUserAddress(c *echo.Context) (address string, handled bool, err error) {
	raw := c.Param("address")
	if !common.IsHexAddress(raw) {
		return "", true, c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "invalid Ethereum address",
		})
	}
	return common.HexToAddress(raw).Hex(), false, nil
}

func (a *API) adminApproveUserHandler(c *echo.Context) error {
	return a.adminSetUserStatus(c, "APPROVED")
}

func (a *API) adminRejectUserHandler(c *echo.Context) error {
	return a.adminSetUserStatus(c, "REJECTED")
}

func (a *API) adminSetUserStatus(c *echo.Context, status string) error {
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
	address, handled, err := adminUserAddress(c)
	if handled || err != nil {
		return err
	}

	var req apiresp.UpdateUserLimitsRequest
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

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

// adminUpdateUserProfileHandler lets a superadmin correct another account's
// identity and contact details.
//
// The self-service handler at PUT /auth/profile edits the signed-in account and
// nothing else — it takes its address from the JWT claims — so an operator fixing
// a mistyped phone number on someone else's account has had no endpoint at all.
// This is that endpoint, and it is deliberately the same request shape and the
// same store call, so one set of validation rules and one geo both-or-neither
// rule govern a profile however it was edited.
//
// It carries no status, role or limits: those have their own endpoints, and an
// operator correcting a name must not be able to grant a role by editing a form
// field.
func (a *API) adminUpdateUserProfileHandler(c *echo.Context) error {
	address, handled, err := adminUserAddress(c)
	if handled || err != nil {
		return err
	}

	var req apiresp.UpdateProfileRequest
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	if err := c.Validate(req); err != nil {
		return a.handleValidateError(c, err)
	}

	// A label without coordinates, or coordinates without a label, describes a
	// place nobody can point at. users_geo_valid backstops this at the database,
	// but a 400 names the field the caller got wrong.
	if (req.GeoLat == nil) != (req.GeoLng == nil) {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: "Validation failed: geoLat and geoLng must be provided together",
		})
	}

	ctx := c.Request().Context()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	user, err := a.store.UpdateUserProfile(
		ctx, tx, address,
		req.Name, req.Email, req.Phone, req.Country,
		req.LocationName, req.GeoLat, req.GeoLng,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, apiresp.ErrResponse{
				Ok:          false,
				ErrCode:     apiresp.ErrCodeNotFound,
				Description: "user not found",
			})
		}
		// email and phone are unique across accounts, so claiming one that
		// belongs to another account is a caller-fixable 409.
		if conflict, ok := userIdentityConflict(err); ok {
			return c.JSON(http.StatusConflict, conflict)
		}
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	// The whole row, matching every other admin user mutation, so a console can
	// write the response straight into its cache.
	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "User profile updated",
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
