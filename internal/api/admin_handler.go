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
// adminPagedList wires a store list that pages by CatalogPage into the shared
// cursor-paginated response, so a handler names its resource once instead of
// repeating the three directions.
func adminPagedList[T any](
	c *echo.Context,
	a *API,
	pagination paginationParams,
	resultKey, description string,
	getID func(T) uint64,
	list func(context.Context, pgx.Tx, store.CatalogPage, uint64, uint64) ([]T, error),
) error {
	return cursorPaginatedCatalogList(c, a, paginatedCatalogListCallbacks[T]{
		firstPage: func(ctx context.Context, tx pgx.Tx, limit uint64) ([]T, error) {
			return list(ctx, tx, store.CatalogFirstPage, 0, limit)
		},
		nextPage: func(ctx context.Context, tx pgx.Tx, cursor, limit uint64) ([]T, error) {
			return list(ctx, tx, store.CatalogNextPage, cursor, limit)
		},
		prevPage: func(ctx context.Context, tx pgx.Tx, cursor, limit uint64) ([]T, error) {
			return list(ctx, tx, store.CatalogPrevPage, cursor, limit)
		},
	}, pagination, resultKey, description, getID, catalogListHooks{})
}

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

	return adminPagedList(c, a, pagination, "users", "Users list",
		func(u store.User) uint64 { return u.ID },
		func(ctx context.Context, tx pgx.Tx, page store.CatalogPage, cursor, limit uint64) ([]store.User, error) {
			return a.store.ListUsersFiltered(ctx, tx, filter, page, cursor, limit)
		})
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

	user, err := a.store.UpdateUserLimits(ctx, tx, address, store.UserLimitsUpdate{MaxPools: req.MaxPools, MaxTokens: req.MaxTokens, MaxCatalogs: req.MaxCatalogs, MaxOffers: req.MaxOffers})
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
// identity, contact details, and profile preferences.
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

	// The same mapper the self-service handler uses, so one set of rules governs a
	// profile however it was edited — including clearLocation and clearAvatar.
	update, err := profileUpdateFromRequest(address, req)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apiresp.ErrResponse{
			Ok:          false,
			ErrCode:     apiresp.ErrCodeValidationFailed,
			Description: err.Error(),
		})
	}

	user, err := a.store.UpdateUserProfile(ctx, tx, update)
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

// adminAccountPoolsHandler lists the pools an account belongs to.
//
// Membership is derived: nothing records a user joining a pool, but the catalog
// records which vouchers a pool allows, so an account belongs to the pools that
// allow a voucher it owns.
func (a *API) adminAccountPoolsHandler(c *echo.Context) error {
	address, handled, err := adminUserAddress(c)
	if handled || err != nil {
		return err
	}

	req := apiresp.CursorPaginationRequest{}
	if err := c.Bind(&req); err != nil {
		return handleBindError(c, err)
	}
	pagination := normalizePagination(req)

	return adminPagedList(c, a, pagination, "pools", "Account pool memberships",
		func(p store.Pool) uint64 { return p.ID },
		func(ctx context.Context, tx pgx.Tx, page store.CatalogPage, cursor, limit uint64) ([]store.Pool, error) {
			return a.store.ListAccountPools(ctx, tx, address, page, cursor, limit)
		})
}

// adminAccountStatsHandler answers the account screen's figures.
//
// The chain totals come from the federation database and are null when it is not
// configured. Null rather than zero: an operator must be able to tell "no
// transactions" from "no answer", and a zero would read as the first.
func (a *API) adminAccountStatsHandler(c *echo.Context) error {
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

	counts, err := a.store.AccountRelationshipCounts(ctx, tx, address)
	if err != nil {
		return handlePostgresError(c, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	result := map[string]any{
		"reports":         counts.Reports,
		"vouchers":        counts.Vouchers,
		"poolMemberships": counts.PoolMemberships,
		"transactions":    nil,
		"swaps":           nil,
		"partners":        nil,
	}

	if a.federationStore != nil {
		activity, actErr := a.federationStore.AccountActivityCounts(ctx, address)
		if actErr != nil {
			// The catalog half is already known and useful on its own, so a
			// federation failure degrades the response rather than failing it.
			a.logg.Error("admin account stats: federation counts", "address", address, "error", actErr)
		} else {
			result["transactions"] = activity.Transactions
			result["swaps"] = activity.Swaps
			result["partners"] = activity.Partners
		}
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Account statistics",
		Result:      result,
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

// adminUpdatePoolProfileHandler lets a superadmin correct a pool's off-chain
// record.
//
// Pools had no write endpoint at all: `upsert-pool-from-deploy` fills what the
// deploy knew and the importer fills the rest, so a mistyped steward email or a
// missing location could only be fixed in the database by hand.
//
// It edits catalog metadata and nothing else. Name, symbol, decimals and every
// dependency address mirror SwapPool, and letting an operator retype them here
// would produce a catalog that disagrees with the chain — with the catalog
// winning on every screen that reads it. Those change by transacting, not by
// editing a form.
//
// Assign-all, like the account profile handler beside it: the caller sends the
// whole record and every column is written, so clearing a field means sending it
// empty. Tags are replaced in the same transaction, so a rejected tag value
// leaves the profile edit unapplied rather than half-saved.
func (a *API) adminUpdatePoolProfileHandler(c *echo.Context) error {
	address, handled, err := adminUserAddress(c)
	if handled || err != nil {
		return err
	}
	req, handled, err := a.bindPoolProfileRequest(c)
	if handled || err != nil {
		return err
	}

	ctx := c.Request().Context()

	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	pool, handled, err := a.savePoolProfile(c, ctx, tx, address, req)
	if handled || err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	return respondPoolProfile(c, pool)
}

// adminUpdateVoucherProfileHandler lets a superadmin correct a voucher's
// off-chain record.
//
// The counterpart to adminUpdatePoolProfileHandler, and it exists for the same
// reason: `upsert-voucher-from-deploy` fills what the deploy knew and the
// importer fills the rest, so a mistyped issuer email, a wrong unit of account or
// a missing pin could only be fixed in the database by hand.
//
// It edits catalog metadata and nothing else, through the same write the
// owner-facing /vouchers/:address/profile route uses. DisplayName is the sole
// public label an operator may correct; the deployed name, symbol, decimals and
// owner remain chain facts. Letting an operator retype those facts here would produce a
// catalog that disagrees with the chain — with the catalog winning on every screen
// that reads it. `active` is absent too: retiring a voucher is a different
// operation from correcting its prose, and folding the two together would let a
// description edit hide a voucher by accident.
//
// Assign-all, like the pool handler above: the caller sends the whole record and
// every column is written, so clearing a field means sending it empty. Tags are
// replaced in the same transaction, so a rejected tag value leaves the profile
// edit unapplied rather than half-saved.
func (a *API) adminUpdateVoucherProfileHandler(c *echo.Context) error {
	address, handled, err := adminUserAddress(c)
	if handled || err != nil {
		return err
	}
	req, handled, err := a.bindVoucherProfileRequest(c)
	if handled || err != nil {
		return err
	}

	ctx := c.Request().Context()
	tx, err := a.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// The public detail row rather than an admin-only shape: it is what the
	// console already caches for this voucher, so the response can be written
	// straight into that cache instead of being refetched through a second
	// endpoint that would have to agree with this one.
	voucher, handled, err := a.saveVoucherProfile(c, ctx, tx, address, req)
	if handled || err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	return c.JSON(http.StatusOK, apiresp.OKResponse{
		Ok:          true,
		Description: "Voucher profile updated",
		Result: map[string]any{
			"voucher": voucher,
		},
	})
}
